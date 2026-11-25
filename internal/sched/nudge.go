package sched

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// The steps of the nudge ladder, counted per run (SCENARIOS AW-10, AW-11).
const (
	// MaxFooterWaits is how often a silent stop held by a live background footer is awaited again.
	MaxFooterWaits = 3
	// StallNudges is how many stalls of a run get a nudge; the next stall hands the run over.
	StallNudges = 1
	// ContinueNudges and SecureNudges are how many NUDGE_CONTINUE and NUDGE_SECURE one run gets.
	ContinueNudges = 1
	SecureNudges   = 1
)

// The details the ladder leaves on a task or run.
const (
	BlockedDetail     = "blocked "
	PausedDetail      = "paused "
	NudgeFailedDetail = "nudge to "
	StalledDetail     = "stalled: "
	unpushedNone      = "none"
	unpushedUnknown   = "unknown"
)

// markStalled moves a working run to stalled and counts the stall; it reports whether the run moved.
func markStalled(ctx context.Context, tx *store.Tx, id model.RunID, detail string) (model.Run, bool, error) {
	run, err := tx.Run(ctx, id)
	if err != nil || run.State != model.RunWorking {
		return run, false, err
	}
	if err := tx.SetRunState(ctx, id, model.RunStalled, detail); err != nil {
		return run, false, err
	}
	run.State = model.RunStalled
	run.Budget.Stalls++
	return run, true, tx.SetRunCounts(ctx, id, run.Budget)
}

// stall marks the run stalled with the worker's tail and climbs the stall ladder when it moved (AW-10).
func (s *Scheduler) stall(ctx context.Context, p config.Project, id model.RunID, detail, tail string) error {
	var run model.Run
	moved := false
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		if run, moved, err = markStalled(ctx, tx, id, detail); err != nil || tail == "" {
			return err
		}
		return tx.SetRunTail(ctx, id, tail)
	})
	if err != nil {
		return fmt.Errorf("sched: mark run %s stalled: %w", id, err)
	}
	if !moved {
		return nil
	}
	return s.onStall(ctx, p, run)
}

// onStall nudges the first stall of a run; the next one hands the run over, or asks the owner once the task has
// used max_runs_per_task runs (AW-10).
func (s *Scheduler) onStall(ctx context.Context, p config.Project, run model.Run) error {
	run, ok, err := s.liveRun(ctx, run.TaskID, run.ID)
	if err != nil || !ok {
		return err
	}
	if run.Budget.Stalls <= StallNudges {
		return s.nudge(ctx, p, run, prompt.NudgeStall, nil)
	}
	reason := fmt.Sprintf("no progress for %s after the stall nudge; tail: %s", p.StallAfter.Std(),
		s.lastWords(run))
	return s.handOver(ctx, p, run, reason, model.TaskNeedsOwner)
}

// silentStop climbs the ladder of a worker that stopped without a result: a live footer is awaited again, then
// NUDGE_CONTINUE, then NUDGE_SECURE while commits may be unpushed, then the task waits for the owner (AW-11).
func (s *Scheduler) silentStop(ctx context.Context, p config.Project, w *watch, ev runtime.RunEvent) error {
	log := s.logFor(p.Name, w.run.TaskID, w.run.ID)
	if ev.Tail != "" {
		err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunTail(ctx, w.run.ID, ev.Tail)
		})
		if err != nil {
			return fmt.Errorf("sched: store the tail of run %s: %w", w.run.ID, err)
		}
	}
	run, ok, err := s.liveRun(ctx, w.run.TaskID, w.run.ID)
	if err != nil || !ok || run.State == model.RunReported {
		return err
	}
	log.InfoContext(ctx, "sched: worker stopped without a result", "agent", run.Worker.Agent,
		"last_words", ev.LastWords)
	b := run.Budget
	switch {
	case ev.BGFooter && b.FooterWaits < MaxFooterWaits:
		return s.awaitFooter(ctx, p, run)
	case b.ContinueNudges < ContinueNudges:
		return s.nudge(ctx, p, run, prompt.NudgeContinue, func(b *model.Budget) { b.ContinueNudges++ })
	}
	h := handle(p.Name, run)
	safego.Go(ctx, log, func(ctx context.Context) {
		commits, err := []string(nil), ErrNoScan
		if s.d.Unpushed != nil {
			commits, err = s.d.Unpushed.Unpushed(ctx, p, run.Branch)
		}
		s.post(ctx, func(ctx context.Context) {
			if err := s.afterScan(ctx, p, h, ev, commits, err); err != nil {
				s.fail(ctx, err)
			}
		})
	})
	return nil
}

// FooterWait is the pause between two looks at a footer-held stop: every wait fits into one stall_after.
func FooterWait(p config.Project) time.Duration {
	return p.StallAfter.Std() / (MaxFooterWaits + 1)
}

// awaitFooter counts one more wait for a live background footer and looks again one FooterWait later.
func (s *Scheduler) awaitFooter(ctx context.Context, p config.Project, run model.Run) error {
	now := s.d.Clock.Now()
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		run.Budget.FooterWaits++
		if err := tx.SetRunCounts(ctx, run.ID, run.Budget); err != nil {
			return err
		}
		return armTimers(ctx, tx, p, run.TaskID, now, TimerAwait)
	})
	if err != nil {
		return fmt.Errorf("sched: await the footer of run %s: %w", run.ID, err)
	}
	return nil
}

// afterScan sends NUDGE_SECURE once while commits are unpushed or cannot be listed, else asks the owner with the
// unpushed commits and the tail (AW-11, AW-12).
func (s *Scheduler) afterScan(ctx context.Context, p config.Project, h runtime.RunHandle, ev runtime.RunEvent,
	commits []string, scanErr error) error {
	w := s.watched[h.RunID]
	if w == nil || w.last.Type != runtime.RunEventIdle {
		return nil
	}
	run, ok, err := s.liveRun(ctx, h.TaskID, h.RunID)
	if err != nil || !ok || run.State == model.RunReported {
		return err
	}
	unpushed := strings.Join(commits, ", ")
	switch {
	case scanErr != nil:
		s.logFor(p.Name, run.TaskID, run.ID).WarnContext(ctx, "sched: unpushed commits unknown", "err", scanErr)
		unpushed = unpushedUnknown
	case len(commits) == 0:
		unpushed = unpushedNone
	}
	if run.Budget.SecureNudges < SecureNudges && unpushed != unpushedNone {
		return s.nudge(ctx, p, run, prompt.NudgeSecure, func(b *model.Budget) { b.SecureNudges++ })
	}
	detail := fmt.Sprintf("%s%s: no status; unpushed: %s; tail: %s", BlockedDetail, run.Worker.Agent, unpushed,
		model.CapField(ev.LastWords))
	return s.toOwner(ctx, p, run, model.TaskNeedsOwner, detail)
}

// nudge counts the nudge with count, records worker_nudged and sends the text in the background; a nudge that
// cannot be delivered fails the run at once (AW-13).
func (s *Scheduler) nudge(ctx context.Context, p config.Project, run model.Run, name prompt.Name,
	count func(*model.Budget)) error {
	return s.nudgeWith(ctx, p, run, name, "", count)
}

// nudgeWith is nudge with hint filled into the template.
func (s *Scheduler) nudgeWith(ctx context.Context, p config.Project, run model.Run, name prompt.Name, hint string,
	count func(*model.Budget)) error {
	text, err := prompt.Render(name, prompt.Vars{Repo: p.Path, Base: p.Base, Hint: hint})
	if err != nil {
		return fmt.Errorf("sched: nudge run %s: %w", run.ID, err)
	}
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if count != nil {
			cur, err := tx.Run(ctx, run.ID)
			if err != nil {
				return err
			}
			count(&cur.Budget)
			if err := tx.SetRunCounts(ctx, run.ID, cur.Budget); err != nil {
				return err
			}
		}
		_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventWorkerNudged, Project: p.Name, TaskID: run.TaskID,
			RunID: run.ID, Detail: string(name)})
		return err
	})
	if err != nil {
		return fmt.Errorf("sched: record the nudge of run %s: %w", run.ID, err)
	}
	log := s.logFor(p.Name, run.TaskID, run.ID)
	log.InfoContext(ctx, "sched: nudging", "agent", run.Worker.Agent, "nudge", string(name))
	h := handle(p.Name, run)
	safego.Go(ctx, log, func(ctx context.Context) {
		err := s.d.Runtime.Prompt(ctx, h, text)
		s.post(ctx, func(ctx context.Context) {
			if err != nil && ctx.Err() == nil {
				s.nudgeFailed(ctx, p, run, err)
			}
		})
	})
	return nil
}

// nudgeFailed ends the run and its task failed: nothing is awaited after a lost nudge (AW-13).
func (s *Scheduler) nudgeFailed(ctx context.Context, p config.Project, run model.Run, cause error) {
	detail := fmt.Sprintf("%s%s failed: %v", NudgeFailedDetail, run.Worker.Agent, cause)
	s.logFor(p.Name, run.TaskID, run.ID).ErrorContext(ctx, "sched: nudge failed", "agent", run.Worker.Agent,
		"err", cause)
	timers, err := s.taskTimers(ctx, run.TaskID)
	if err == nil {
		err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
			cur, err := tx.Run(ctx, run.ID)
			if err != nil {
				return err
			}
			if cur.State.CanTransition(model.RunFailed) == nil {
				if err := tx.SetRunState(ctx, run.ID, model.RunFailed, detail); err != nil {
					return err
				}
			}
			task, err := tx.Task(ctx, run.TaskID)
			if err != nil {
				return err
			}
			if task.State.CanTransition(model.TaskFailed) == nil {
				if err := tx.SetTaskState(ctx, run.TaskID, model.TaskFailed, detail); err != nil {
					return err
				}
			}
			return disarm(ctx, tx, timers)
		})
	}
	if err != nil {
		s.fail(ctx, fmt.Errorf("sched: record the failed nudge of %s: %w", run.ID, err))
		return
	}
	s.unwatch(run.ID)
}

// toOwner moves the run's task to state with detail and disarms its timers; the worker's pane stays open.
func (s *Scheduler) toOwner(ctx context.Context, p config.Project, run model.Run, state model.TaskState,
	detail string) error {
	timers, err := s.taskTimers(ctx, run.TaskID)
	if err != nil {
		return err
	}
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		task, err := tx.Task(ctx, run.TaskID)
		if err != nil {
			return err
		}
		if err := task.State.CanTransition(state); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, run.TaskID, state, detail); err != nil {
			return err
		}
		return disarm(ctx, tx, timers)
	})
	if err != nil {
		return fmt.Errorf("sched: move task %s to %s: %w", run.TaskID, state, err)
	}
	s.logFor(p.Name, run.TaskID, run.ID).WarnContext(ctx, "sched: "+detail, "agent", run.Worker.Agent)
	return nil
}

// lastWords is the worker's last words from its newest event, cut to one field.
func (s *Scheduler) lastWords(run model.Run) string {
	if w := s.watched[run.ID]; w != nil && w.last.LastWords != "" {
		return model.CapField(w.last.LastWords)
	}
	return model.CapField(lastLines(run.Tail, 1))
}

// lastLines is the last n non-empty lines of text.
func lastLines(text string, n int) string {
	var out []string
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{lines[i]}, out...)
		}
	}
	return strings.Join(out, "\n")
}
