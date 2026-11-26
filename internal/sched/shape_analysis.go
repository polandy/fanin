package sched

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// The details and verdict notes of an ended analysis (SCENARIOS FP-3, GH-11, GH-12).
const (
	// AnalysisIssueDetail leads the detail of an analysis that filed an issue for the owner.
	AnalysisIssueDetail = "analysis needs-owner "
	// GatingDetail leads the detail of an analysis whose PR is being gated.
	GatingDetail = "gating "
	// ReadyDetail leads the gate verdict of an analysis PR that waits for the owner's go.
	ReadyDetail = model.ReadyVerdictPrefix
	// GateErrorDetail leads the detail of an analysis PR whose gate could not be read.
	GateErrorDetail = "ERROR "
	// GateCodeNote ends the verdict of a green PR that changes more than docs.
	GateCodeNote = "changes more than docs, the owner's go merges"
	// GateDocsNote ends the verdict of a green docs-only PR.
	GateDocsNote = "docs only, the owner's go merges"
	// GateUnreadNote is the verdict of a PR no gate reader can read; the owner checks it before the go.
	GateUnreadNote = "gate unread, no forge reader; check the PR before the go"
)

// closeByHand is logged when an ended analysis's pane cannot be closed.
const closeByHand = "sched: cannot close the pane of an ended analysis; close it by hand"

// The states of a task's gate turn; a timer that fires while its own turn is in flight reads again after it.
type turnState int

const (
	turnIdle turnState = iota
	turnRunning
	turnAgain
)

// PRGates opens the gate of one PR of a project.
type PRGates interface {
	For(p config.Project, pr int) (gate.Reader, gate.Target, error)
}

// gateTimer reports whether kind is a timer the gate arms.
func gateTimer(kind string) bool {
	return kind == gate.TimerRetry || kind == gate.TimerWait || kind == gate.TimerSettle || kind == gate.TimerPoll
}

// finishAnalysis ends a running analysis task by its reported result (SCENARIOS FP-3); other tasks are left alone.
func (s *Scheduler) finishAnalysis(ctx context.Context, p config.Project, ev model.Event) error {
	task, err := s.d.Store.Task(ctx, ev.TaskID)
	if err != nil {
		return fmt.Errorf("sched: read task %s: %w", ev.TaskID, err)
	}
	if task.Shape != model.ShapeAnalysis || task.State != model.TaskRunning {
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
	case model.ResultIssue:
		return s.closeAnalysis(ctx, p, run, AnalysisIssueDetail+issueRef(res))
	case model.ResultNothing:
		return s.closeAnalysis(ctx, p, run, IdleDetail+res.Text)
	case model.ResultBlocked:
		return s.blockAnalysis(ctx, p, run, res.Text)
	case model.ResultDonePR:
		return s.startGate(ctx, p, run, res.PR, nil)
	}
	return s.blockAnalysis(ctx, p, run, fmt.Sprintf("analysis reported %s; it ends with %s, %s, %s or %s",
		res.Kind, model.ResultIssue, model.ResultNothing, model.ResultBlocked, model.ResultDonePR))
}

// closeAnalysis ends the run succeeded and the task done with detail, then closes the pane.
func (s *Scheduler) closeAnalysis(ctx context.Context, p config.Project, run model.Run, detail string) error {
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.SetRunState(ctx, run.ID, model.RunSucceeded, detail); err != nil {
			return err
		}
		return tx.SetTaskState(ctx, run.TaskID, model.TaskDone, detail)
	})
	if err != nil {
		return fmt.Errorf("sched: end analysis %s: %w", run.TaskID, err)
	}
	log := s.logFor(p.Name, run.TaskID, run.ID)
	log.InfoContext(ctx, "sched: analysis ended", "agent", run.Worker.Agent, "detail", detail)
	s.unwatch(run.ID)
	h := handle(p.Name, run)
	safego.Go(ctx, log, func(ctx context.Context) {
		err := s.d.Runtime.Stop(ctx, h)
		s.post(ctx, func(ctx context.Context) {
			if err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, closeByHand, "agent", h.Agent, "pane", h.Pane, "err", err)
			}
		})
	})
	return nil
}

// blockAnalysis moves the task to needs_owner and notifies the owner; the pane stays open.
func (s *Scheduler) blockAnalysis(ctx context.Context, p config.Project, run model.Run, why string) error {
	return s.askOwner(ctx, p, run, BlockedDetail+run.Worker.Agent+": "+why)
}

// askOwner moves the task to needs_owner with detail, then sends detail as a note; a failed note is recorded
// and never undoes the move.
func (s *Scheduler) askOwner(ctx context.Context, p config.Project, run model.Run, detail string) error {
	if err := s.toOwner(ctx, p, run, model.TaskNeedsOwner, detail); err != nil {
		return err
	}
	log := s.logFor(p.Name, run.TaskID, run.ID)
	note := notify.Note{Project: p.Name, TaskID: run.TaskID, Text: detail}
	safego.Go(ctx, log, func(ctx context.Context) { s.note(ctx, log, run, note) })
	return nil
}

// note sends n and records notified or notify_failed.
func (s *Scheduler) note(ctx context.Context, log *slog.Logger, run model.Run, n notify.Note) {
	ev := model.Event{Type: model.EventNotified, Project: n.Project, TaskID: n.TaskID, RunID: run.ID, Detail: n.Text}
	if err := s.d.Notify.Notify(ctx, n); err != nil {
		ev.Type = model.EventNotifyFailed
		ev.Detail = fmt.Sprintf("note on %s failed: %v", n.TaskID, err)
		log.ErrorContext(ctx, "sched: note failed", "err", err)
	}
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, ev)
		return err
	})
	if err != nil && ctx.Err() == nil {
		log.ErrorContext(ctx, "sched: note event not recorded", "err", err)
	}
}

// startGate moves the task to gating and reads the gate of its PR, first from head when it is set (SCENARIOS GH-11).
func (s *Scheduler) startGate(ctx context.Context, p config.Project, run model.Run, pr int, head *forge.PR) error {
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		return tx.SetTaskState(ctx, run.TaskID, model.TaskGating, GatingDetail+"#"+strconv.Itoa(pr))
	})
	if err != nil {
		return fmt.Errorf("sched: gate task %s: %w", run.TaskID, err)
	}
	delete(s.gates, run.TaskID)
	return s.gateRead(ctx, p, run, pr, head)
}

// gateTurn reads the gate once in the background; gated applies the answer on the loop.
func (s *Scheduler) gateTurn(ctx context.Context, p config.Project, run model.Run, pr int) error {
	return s.gateRead(ctx, p, run, pr, nil)
}

// gateRead is gateTurn whose first PR read is head when it is set.
func (s *Scheduler) gateRead(ctx context.Context, p config.Project, run model.Run, pr int, head *forge.PR) error {
	ref := "#" + strconv.Itoa(pr)
	task, err := s.d.Store.Task(ctx, run.TaskID)
	if err != nil {
		return fmt.Errorf("sched: read task %s: %w", run.TaskID, err)
	}
	if s.d.Gates == nil {
		return s.verdict(ctx, p, run, ReadyDetail+ref+": "+GateUnreadNote)
	}
	r, target, err := s.d.Gates.For(p, pr)
	if err != nil {
		return s.askOwner(ctx, p, run, GateErrorDetail+ref+": "+err.Error())
	}
	if head != nil {
		r = &headReader{Reader: r, head: head}
	}
	w := gate.NewWatcher(r, s.d.Clock, p.Gate, p.Name, run.TaskID, target)
	st := s.gates[run.TaskID]
	s.busy[run.TaskID] = turnRunning
	log := s.logFor(p.Name, run.TaskID, run.ID)
	safego.Go(ctx, log, func(ctx context.Context) {
		step, err := w.Turn(ctx, storeTimers{s.d.Store}, st)
		var files []string
		var filesErr error
		if err == nil && step.Final && step.Err == nil && step.Result.Verdict == gate.Green &&
			task.Shape == model.ShapeAnalysis {
			files, filesErr = r.Files(ctx, target.Repo, target.Number)
		}
		s.post(ctx, func(ctx context.Context) {
			again := s.busy[run.TaskID] == turnAgain
			delete(s.busy, run.TaskID)
			if err == nil {
				err = s.gated(ctx, p, run, task, ref, step, files, filesErr)
			}
			if err == nil && again && !step.Final {
				err = s.gateTurn(ctx, p, run, pr)
			}
			if err != nil {
				s.fail(ctx, fmt.Errorf("sched: gate %s of task %s: %w", ref, run.TaskID, err))
			}
		})
	})
	return nil
}

// gated applies one gate step: a PR task goes on in gatedPR; a green analysis PR waits for the owner's go, any
// other final answer goes to the owner.
func (s *Scheduler) gated(ctx context.Context, p config.Project, run model.Run, task model.Task, ref string,
	step gate.Step, files []string, filesErr error) error {
	if !step.Final {
		s.gates[run.TaskID] = step.State
		return nil
	}
	delete(s.gates, run.TaskID)
	switch {
	case step.Err != nil:
		return s.askOwner(ctx, p, run, GateErrorDetail+ref+": "+step.Err.Error())
	case task.Shape == model.ShapePR:
		return s.gatedPR(ctx, p, run, task, ref, step.Result)
	case !step.Result.Verdict.IsGreen():
		return s.blockAnalysis(ctx, p, run, ref+" "+step.Result.String())
	}
	docs := step.Result.Verdict == gate.GreenDocs ||
		(step.Result.Verdict == gate.Green && filesErr == nil && gate.DocsOnly(files))
	note := GateCodeNote
	if docs {
		note = GateDocsNote
	}
	if filesErr != nil {
		s.logFor(p.Name, run.TaskID, run.ID).WarnContext(ctx, "sched: PR files unreadable; counted as code",
			"pr", ref, "err", filesErr)
	}
	return s.ready(ctx, p, run, ref, step.Result.String()+"; "+note)
}

// verdict records detail as the task's gate verdict; the task stays gating.
func (s *Scheduler) verdict(ctx context.Context, p config.Project, run model.Run, detail string) error {
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventGateVerdict, Project: p.Name, TaskID: run.TaskID,
			RunID: run.ID, Detail: detail})
		return err
	})
	if err != nil {
		return fmt.Errorf("sched: record the gate verdict of %s: %w", run.TaskID, err)
	}
	s.logFor(p.Name, run.TaskID, run.ID).InfoContext(ctx, "sched: gate verdict",
		"verdict", detail)
	return nil
}

// fireGate disarms a due gate timer and reads the gate again while the task is still gating.
func (s *Scheduler) fireGate(ctx context.Context, p config.Project, t store.Timer, task model.Task,
	runs []model.Run) error {
	if err := s.rearm(ctx, t, time.Time{}, nil); err != nil {
		return err
	}
	if task.State != model.TaskGating || len(runs) == 0 {
		return nil
	}
	if s.busy[task.ID] != turnIdle {
		s.busy[task.ID] = turnAgain
		return nil
	}
	run := runs[len(runs)-1]
	if run.Result == nil || run.Result.Kind != model.ResultDonePR {
		return nil
	}
	return s.gateTurn(ctx, p, run, run.Result.PR)
}

// storeTimers arms each gate timer in its own transaction.
type storeTimers struct{ st *store.Store }

func (t storeTimers) ArmTimer(ctx context.Context, timer store.Timer) (string, error) {
	var id string
	err := t.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		id, err = tx.ArmTimer(ctx, timer)
		return err
	})
	return id, err
}
