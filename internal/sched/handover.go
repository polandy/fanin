package sched

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// What a handover carries over from the task's earlier runs (spec: docs/dev/spec/handover.md).
const (
	// HandoverErrors is how many of the latest errors a handover lists.
	HandoverErrors = 5
	// HandoverTailLines is how many lines of the spent run's tail a handover keeps.
	HandoverTailLines = 40
)

// RolledOverDetail leads the detail of a run that was replaced by a fresh one.
const RolledOverDetail = "rolled over: "

// The notes a handover lists as open.
const (
	openPR       = "PR #%d is open; carry it on instead of opening a new one."
	openBlocked  = "The run was blocked: %s"
	openUnpushed = "The run was asked to push its commits and did not report; check for unpushed commits first."
)

// errorDetails lead the run_state details a handover lists as errors.
var errorDetails = []string{StalledDetail, SpawnFailedDetail, TimeoutDetail, RolledOverDetail, NudgeFailedDetail}

// handOver replaces a spent run by a fresh run in the same slot that carries the handover in its prompt; once the
// task has used max_runs_per_task runs, the task moves to fallback instead and the worker stays open.
func (s *Scheduler) handOver(ctx context.Context, p config.Project, run model.Run, reason string,
	fallback model.TaskState) error {
	runs, err := s.d.Store.Runs(ctx, run.TaskID)
	if err != nil {
		return fmt.Errorf("sched: read the runs of %s: %w", run.TaskID, err)
	}
	if limit := p.Budgets.MaxRunsPerTask; len(runs) >= limit {
		prefix := BlockedDetail
		if fallback == model.TaskPaused {
			prefix = PausedDetail
		}
		return s.toOwner(ctx, p, run, fallback, fmt.Sprintf("%s%s: %s; %d of %d runs used", prefix,
			run.Worker.Agent, reason, len(runs), limit))
	}
	task, err := s.d.Store.Task(ctx, run.TaskID)
	if err != nil {
		return fmt.Errorf("sched: read task %s: %w", run.TaskID, err)
	}
	ho, err := s.handover(ctx, run, reason)
	if err != nil {
		return err
	}
	spec, err := s.spec(p, task, run.Hint, "", ho.Markdown())
	if err != nil {
		return err
	}
	timers, err := s.taskTimers(ctx, run.TaskID)
	if err != nil {
		return err
	}
	next := model.Run{TaskID: task.ID, Slot: run.Slot, Branch: run.Branch, Hint: run.Hint,
		Token: model.Token(rand.Text())}
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.SetRunState(ctx, run.ID, model.RunRolledOver, RolledOverDetail+reason); err != nil {
			return err
		}
		cur, err := tx.Task(ctx, task.ID)
		if err != nil {
			return err
		}
		if cur.State == model.TaskGating {
			if err := tx.SetTaskState(ctx, task.ID, model.TaskRunning, RolledOverDetail+reason); err != nil {
				return err
			}
		}
		if err := disarm(ctx, tx, timers); err != nil {
			return err
		}
		n, err := nextRunNumber(ctx, tx, task.ID)
		if err != nil {
			return err
		}
		next.ID, next.Number, next.Worker = RunID(task.ID, n), n, model.Worker{Agent: AgentName(task.ID, n)}
		return tx.CreateRun(ctx, next)
	})
	if err != nil {
		return fmt.Errorf("sched: hand run %s over: %w", run.ID, err)
	}
	log := s.logFor(p.Name, run.TaskID, run.ID)
	log.InfoContext(ctx, "sched: run handed over", "agent", run.Worker.Agent, "next", next.Worker.Agent,
		"reason", reason)
	s.unwatch(run.ID)
	old := handle(p.Name, run)
	safego.Go(ctx, log, func(ctx context.Context) {
		if err := s.d.Runtime.Stop(ctx, old); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "sched: cannot stop the spent worker; its pane stays open", "agent", old.Agent,
				"err", err)
		}
	})
	s.launch(ctx, p, next, spec)
	return nil
}

// handover gathers what run leaves its follow-up run.
func (s *Scheduler) handover(ctx context.Context, run model.Run, reason string) (prompt.Handover, error) {
	evs, err := s.d.Store.Events(ctx, run.TaskID)
	if err != nil {
		return prompt.Handover{}, fmt.Errorf("sched: read the events of %s: %w", run.TaskID, err)
	}
	h := prompt.Handover{FromRun: run.Number, Reason: reason, Branch: run.Branch,
		Tail: lastLines(run.Tail, HandoverTailLines)}
	for _, ev := range evs {
		switch {
		case ev.RunID == run.ID && ev.Type == model.EventProgress:
			h.State = ev.Detail
		case ev.Type == model.EventRunState && hasAnyPrefix(ev.Detail, errorDetails):
			h.Errors = append(h.Errors, fmt.Sprintf("run %s: %s", ev.RunID, ev.Detail))
		}
	}
	h.Errors = h.Errors[max(0, len(h.Errors)-HandoverErrors):]
	if res := run.Result; res != nil {
		if h.State == "" {
			h.State = fmt.Sprintf("Last report: %s %s", res.Kind, res.Text)
		}
		switch {
		case res.Kind == model.ResultDonePR && res.PR > 0:
			h.Open = append(h.Open, fmt.Sprintf(openPR, res.PR))
		case res.Kind == model.ResultBlocked:
			h.Open = append(h.Open, fmt.Sprintf(openBlocked, res.Text))
		}
	}
	if run.Budget.SecureNudges > 0 {
		h.Open = append(h.Open, openUnpushed)
	}
	return h, nil
}

// launch writes the run's files and starts its worker in the background; a failure fails the run (SP-8).
func (s *Scheduler) launch(ctx context.Context, p config.Project, run model.Run, spec runtime.RunSpec) {
	spec.RunID, spec.Agent = run.ID, run.Worker.Agent
	paths, err := s.d.Files.Prepare(ctx, p, run)
	if err != nil {
		s.spawnFailed(ctx, p, run, err)
		return
	}
	spec.MCPConfig, spec.Settings = paths.MCPConfig, paths.Settings
	log := s.logFor(p.Name, run.TaskID, run.ID)
	log.InfoContext(ctx, "sched: spawning", "agent", spec.Agent, "slot", run.Slot, "model", spec.Model)
	safego.Go(ctx, log, func(ctx context.Context) {
		h, err := s.d.Runtime.Start(ctx, spec)
		s.post(ctx, func(ctx context.Context) { s.started(ctx, p, run, h, err) })
	})
}

// handle is the runtime handle of the run's stored worker.
func handle(project string, run model.Run) runtime.RunHandle {
	return runtime.RunHandle{TaskID: run.TaskID, RunID: run.ID, Project: project, Host: run.Worker.Host,
		Agent: run.Worker.Agent, Workspace: run.Worker.Workspace, Tab: run.Worker.Tab, Pane: run.Worker.Pane}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
