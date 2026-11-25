package sched

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/hook"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// The verdict notes of a PR task (SCENARIOS GH-13, GT-7, GT-8, OC-7).
const (
	// GreenDetail leads the gate verdict of a PR that merges without the owner.
	GreenDetail = "green "
	// AutoNote ends the verdict of a green GitHub PR.
	AutoNote = "auto"
	// DocsAutoNote ends the verdict of a green Gitea docs-only PR.
	DocsAutoNote = "docs-only, auto"
	// OCAutoNote ends the verdict of a green opencode PR under oc_merge auto.
	OCAutoNote = "opencode, auto"
	// OCManualNote ends the verdict of a green opencode PR under oc_merge manual.
	OCManualNote = "oc_merge manual, the owner's go merges"
	// NoCINote ends the verdict of a Gitea PR without any check.
	NoCINote = "no CI, the owner's go merges"
	// PreGoFailedDetail leads the question of a PR whose pre_go_check failed or could not run.
	PreGoFailedDetail = "pre-go check "
)

// The nudges a red PR gets (SCENARIOS GH-10, CF-2); fix and conflict nudges count against fix_rounds_per_run.
const (
	// ReviewNudges is how many review reds of one run get a nudge; the next one pauses the task.
	ReviewNudges = 3
	// ConflictNudges is how many conflicts of one task get a nudge; the next one pauses the task.
	ConflictNudges = 1
)

// finishPR ends or gates a running PR task by its reported result; other tasks are left alone.
func (s *Scheduler) finishPR(ctx context.Context, p config.Project, ev model.Event) error {
	task, err := s.d.Store.Task(ctx, ev.TaskID)
	if err != nil {
		return fmt.Errorf("sched: read task %s: %w", ev.TaskID, err)
	}
	if task.Shape != model.ShapePR || task.State != model.TaskRunning {
		return nil
	}
	run, err := s.d.Store.Run(ctx, ev.RunID)
	if err != nil {
		return fmt.Errorf("sched: read run %s: %w", ev.RunID, err)
	}
	if run.State != model.RunReported || run.Result == nil {
		return nil
	}
	res := *run.Result
	switch res.Kind {
	case model.ResultDonePR:
		return s.checkHead(ctx, p, run, res.PR)
	case model.ResultIssue:
		return s.closeAnalysis(ctx, p, run, AnalysisIssueDetail+issueRef(res))
	case model.ResultNothing:
		return s.closeAnalysis(ctx, p, run, IdleDetail+res.Text)
	case model.ResultBlocked:
		return s.blockAnalysis(ctx, p, run, res.Text)
	}
	return s.blockAnalysis(ctx, p, run, fmt.Sprintf("reported %s without a PR; a PR task ends with %s", res.Kind,
		model.ResultDonePR))
}

// checkHead reads the PR once in the background; headRead checks its head before the gate (SCENARIOS GH-14).
func (s *Scheduler) checkHead(ctx context.Context, p config.Project, run model.Run, pr int) error {
	if s.d.Gates == nil {
		return s.startGate(ctx, p, run, pr, nil)
	}
	ref := "#" + strconv.Itoa(pr)
	r, target, err := s.d.Gates.For(p, pr)
	if err != nil {
		return s.askOwner(ctx, p, run, GateErrorDetail+ref+": "+err.Error())
	}
	log := s.logFor(p.Name, run.TaskID, run.ID)
	safego.Go(ctx, log, func(ctx context.Context) {
		head, err := r.PR(ctx, target.Repo, target.Number)
		s.post(ctx, func(ctx context.Context) {
			if err == nil {
				err = s.headRead(ctx, p, run, pr, head)
			} else {
				err = s.askOwner(ctx, p, run, fmt.Sprintf("%s%s: head unreadable: %v", GateErrorDetail, ref, err))
			}
			if err != nil {
				s.fail(ctx, fmt.Errorf("sched: check the head of %s of %s: %w", ref, run.TaskID, err))
			}
		})
	})
	return nil
}

// headRead blocks a PR whose head is the base, another task's or not the run's branch; else the run adopts the
// head and the gate starts from this read.
func (s *Scheduler) headRead(ctx context.Context, p config.Project, run model.Run, pr int, head forge.PR) error {
	task, err := s.d.Store.Task(ctx, run.TaskID)
	if err != nil || task.State != model.TaskRunning {
		return err
	}
	why, err := s.headClaim(ctx, p, run, pr, head.HeadBranch)
	if err != nil || why != "" {
		if err != nil {
			return err
		}
		return s.blockAnalysis(ctx, p, run, why)
	}
	if run.Branch == "" {
		err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunBranch(ctx, run.ID, head.HeadBranch)
		})
		if err != nil {
			return fmt.Errorf("sched: adopt the head of %s: %w", run.ID, err)
		}
		run.Branch = head.HeadBranch
	}
	return s.startGate(ctx, p, run, pr, &head)
}

// headClaim names why the PR may not be gated for run, or "" when its head is the run's own branch.
func (s *Scheduler) headClaim(ctx context.Context, p config.Project, run model.Run, pr int, head string) (string,
	error) {
	ref := "#" + strconv.Itoa(pr)
	switch {
	case head == "" || head == p.Base:
		return fmt.Sprintf("%s heads the base branch %s; push a feature branch and open the PR from it", ref, p.Base),
			nil
	case run.Branch != "" && run.Branch != head:
		return fmt.Sprintf("%s heads %s, not the run's branch %s", ref, head, run.Branch), nil
	}
	tasks, err := s.d.Store.ProjectTasks(ctx, p.Name)
	if err != nil {
		return "", fmt.Errorf("sched: read the tasks of %s: %w", p.Name, err)
	}
	for _, t := range tasks {
		if t.ID == run.TaskID || t.State.Terminal() {
			continue
		}
		runs, err := s.d.Store.Runs(ctx, t.ID)
		if err != nil {
			return "", fmt.Errorf("sched: read the runs of %s: %w", t.ID, err)
		}
		if len(runs) == 0 {
			continue
		}
		last := runs[len(runs)-1]
		if last.Branch == head || (last.Result != nil && last.Result.Kind == model.ResultDonePR && last.Result.PR == pr) {
			return fmt.Sprintf("%s is claimed by task %s", ref, t.ID), nil
		}
	}
	return "", nil
}

// gatedPR applies a final gate answer of a PR task: green merges or waits for the go, red nudges or stops.
func (s *Scheduler) gatedPR(ctx context.Context, p config.Project, run model.Run, task model.Task, ref string,
	res gate.Result) error {
	if res.Verdict == gate.Red {
		return s.redPR(ctx, p, run, ref, res)
	}
	auto, note, ok := mergeRule(p, task, res.Verdict)
	if !ok {
		return s.blockAnalysis(ctx, p, run, ref+" "+res.String())
	}
	if auto {
		if err := s.verdict(ctx, p, run, GreenDetail+ref+": "+res.String()+" ("+note+")"); err != nil {
			return err
		}
		return s.startMerge(ctx, p, run.TaskID, false)
	}
	return s.ready(ctx, p, run, ref, res.String()+"; "+note)
}

// ready records the ready verdict of a PR that waits for the owner's go. With a pre_go_check the check runs first
// and its output ends the verdict; a failed or missing check asks the owner and never merges (SCENARIOS GT-8, FP-6).
func (s *Scheduler) ready(ctx context.Context, p config.Project, run model.Run, ref, line string) error {
	if p.PreGoCheck == "" {
		return s.readyVerdict(ctx, p, run, ref, line)
	}
	if s.d.PreGo == nil || run.Branch == "" {
		return s.askOwner(ctx, p, run, fmt.Sprintf("%s%s: pre_go_check %q of project %s cannot run without a PreGo "+
			"runner and the run's branch; set both", PreGoFailedDetail, ref, p.PreGoCheck, p.Name))
	}
	pr, err := strconv.Atoi(strings.TrimPrefix(ref, "#"))
	if err != nil {
		return fmt.Errorf("sched: PR reference %q of task %s is not #<number>: %w", ref, run.TaskID, err)
	}
	req := hook.PreGoRequest{Project: p.Name, PR: pr, Path: p.Path, Base: p.Base, Branch: run.Branch,
		Command: p.PreGoCheck, Dir: s.d.StateDir}
	s.checking[run.TaskID] = true
	log := s.logFor(p.Name, run.TaskID, run.ID)
	safego.Go(ctx, log, func(ctx context.Context) {
		out, err := s.d.PreGo.Run(ctx, req)
		s.post(ctx, func(ctx context.Context) {
			delete(s.checking, run.TaskID)
			if ctx.Err() != nil {
				return
			}
			switch {
			case err != nil:
				err = s.askOwner(ctx, p, run, PreGoFailedDetail+ref+": "+err.Error())
			case out != "":
				err = s.readyVerdict(ctx, p, run, ref, line+"; "+out)
			default:
				err = s.readyVerdict(ctx, p, run, ref, line)
			}
			if err != nil {
				s.fail(ctx, fmt.Errorf("sched: pre-go check of %s for %s: %w", ref, run.TaskID, err))
			}
		})
	})
	return nil
}

func (s *Scheduler) readyVerdict(ctx context.Context, p config.Project, run model.Run, ref, line string) error {
	if err := s.verdict(ctx, p, run, ReadyDetail+ref+": "+line); err != nil {
		return err
	}
	if s.goAgain[run.TaskID] {
		return s.startMerge(ctx, p, run.TaskID, false)
	}
	return nil
}

// mergeRule says whether a final verdict v merges without the owner and the note of its verdict; ok is false for
// a verdict that never merges.
func mergeRule(p config.Project, task model.Task, v gate.Verdict) (auto bool, note string, ok bool) {
	switch v {
	case gate.GreenDocs:
		return true, DocsAutoNote, true
	case gate.GreenCode:
		return false, GateCodeNote, true
	case gate.NoCI:
		return false, NoCINote, true
	case gate.Green:
		if cmp.Or(task.WorkerKind, p.WorkerKind) != intake.KindOpencode {
			return true, AutoNote, true
		}
		if p.OCMerge == config.OCMergeManual {
			return false, OCManualNote, true
		}
		return true, OCAutoNote, true
	}
	return false, "", false
}

// redPR sends the worker back with a fix, conflict or review nudge while its budget lasts; a closed PR or a gate
// that timed out goes to the owner (SCENARIOS GH-9, GH-10, CF-1, CF-2).
func (s *Scheduler) redPR(ctx context.Context, p config.Project, run model.Run, ref string, res gate.Result) error {
	why := ref + " " + res.String()
	cur, err := s.d.Store.Run(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("sched: read run %s: %w", run.ID, err)
	}
	paused := PausedDetail + run.Worker.Agent + ": " + why
	switch res.Cause {
	case gate.CauseReview:
		if cur.Budget.ReviewNudges >= ReviewNudges {
			return s.toOwner(ctx, p, run, model.TaskPaused, fmt.Sprintf("%s; review nudges spent: %d of %d", paused,
				cur.Budget.ReviewNudges, ReviewNudges))
		}
		return s.nudgeRed(ctx, p, cur, prompt.NudgeReview, why, func(b *model.Budget) { b.ReviewNudges++ })
	case gate.CauseConflict, gate.CauseCI:
	default:
		return s.blockAnalysis(ctx, p, run, why)
	}
	name := prompt.NudgeFix
	if res.Cause == gate.CauseConflict {
		n, err := s.nudgesOf(ctx, run.TaskID, prompt.NudgeConflict)
		if err != nil {
			return err
		}
		if n >= ConflictNudges {
			return s.toOwner(ctx, p, run, model.TaskPaused, fmt.Sprintf("%s; still conflicting after %d nudge; "+
				"merge origin/%s by hand", paused, n, p.Base))
		}
		name = prompt.NudgeConflict
	}
	b := cur.Budget
	b.FixRounds++
	if reason := spent(b, p.Budgets); reason != "" {
		err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunCounts(ctx, cur.ID, b)
		})
		if err != nil {
			return fmt.Errorf("sched: count the fix round of %s: %w", cur.ID, err)
		}
		return s.handOver(ctx, p, cur, reason, model.TaskPaused)
	}
	return s.nudgeRed(ctx, p, cur, name, why, func(b *model.Budget) { b.FixRounds++ })
}

// nudgeRed sends the run back to work and nudges it with the gate's reasons.
func (s *Scheduler) nudgeRed(ctx context.Context, p config.Project, run model.Run, name prompt.Name, why string,
	count func(*model.Budget)) error {
	if err := s.backToWork(ctx, p, run, why); err != nil {
		return err
	}
	run.State = model.RunWorking
	return s.nudgeWith(ctx, p, run, name, why, count)
}

// backToWork moves the run to working and its task to running and arms the stall and timeout timers again.
func (s *Scheduler) backToWork(ctx context.Context, p config.Project, run model.Run, detail string) error {
	now := s.d.Clock.Now()
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.SetRunState(ctx, run.ID, model.RunWorking, detail); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, run.TaskID, model.TaskRunning, detail); err != nil {
			return err
		}
		return armTimers(ctx, tx, p, run.TaskID, now, TimerStall, TimerTimeout)
	})
	if err != nil {
		return fmt.Errorf("sched: send %s back to work: %w", run.TaskID, err)
	}
	return nil
}

// nudgesOf counts the nudges called name the task got over all its runs.
func (s *Scheduler) nudgesOf(ctx context.Context, id model.TaskID, name prompt.Name) (int, error) {
	evs, err := s.d.Store.Events(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("sched: read the events of %s: %w", id, err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == model.EventWorkerNudged && ev.Detail == string(name) {
			n++
		}
	}
	return n, nil
}

// headReader answers the gate's first PR read with the head the scheduler already read.
type headReader struct {
	gate.Reader
	head *forge.PR
}

func (h *headReader) PR(ctx context.Context, repo string, n int) (forge.PR, error) {
	if h.head != nil {
		pr := *h.head
		h.head = nil
		return pr, nil
	}
	return h.Reader.PR(ctx, repo, n)
}

// issueRef is the issue a result filed, by URL or number.
func issueRef(res model.Result) string {
	if res.IssueURL != "" {
		return res.IssueURL
	}
	return "#" + strconv.Itoa(res.Issue)
}
