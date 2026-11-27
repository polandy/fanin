package sched

import (
	"context"
	"errors"
	"fmt"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// MergeDroppedLog is logged for a merge whose task left gating before it took the lock (SCENARIOS OA-8).
const MergeDroppedLog = "sched: merge dropped, the task is no longer gating"

// cancelCloseByHand is logged when the pane of a cancelled task cannot be closed.
const cancelCloseByHand = "sched: cannot close the pane of a cancelled task; close it by hand"

// cancelled drops what the loop keeps for a cancelled task, closes its worker's pane and frees a merge lock it
// left; a merge in flight drops itself under the lock (SCENARIOS OA-8).
func (s *Scheduler) cancelled(ctx context.Context, p config.Project, id model.TaskID) error {
	delete(s.held, id)
	delete(s.goAgain, id)
	delete(s.reviewed, id)
	delete(s.gates, id)
	runs, err := s.d.Store.Runs(ctx, id)
	if err != nil {
		return fmt.Errorf("sched: read the runs of cancelled task %s: %w", id, err)
	}
	if len(runs) > 0 && runs[len(runs)-1].Worker.Pane != "" {
		s.closePane(ctx, p, runs[len(runs)-1])
	}
	if s.merging[id] {
		return nil
	}
	err = s.d.Store.ReleaseLock(ctx, p.Name, string(id))
	if err != nil && !errors.Is(err, store.ErrLockNotHeld) {
		return fmt.Errorf("sched: release the merge lock of cancelled task %s: %w", id, err)
	}
	return nil
}

// closePane stops the worker of a cancelled run off the loop.
func (s *Scheduler) closePane(ctx context.Context, p config.Project, run model.Run) {
	log := s.logFor(p.Name, run.TaskID, run.ID)
	s.unwatch(run.ID)
	h := handle(p.Name, run)
	safego.Go(ctx, log, func(ctx context.Context) {
		err := s.d.Runtime.Stop(ctx, h)
		s.post(ctx, func(ctx context.Context) {
			if err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, cancelCloseByHand, "agent", h.Agent, "pane", h.Pane, "err", err)
			}
		})
	})
}

// gone reports whether the task has ended, so a result that comes back for it records nothing; an unreadable
// task is a failed step and counts as gone.
func (s *Scheduler) gone(ctx context.Context, id model.TaskID) bool {
	task, err := s.d.Store.Task(ctx, id)
	if err != nil {
		s.fail(ctx, fmt.Errorf("sched: read task %s: %w", id, err))
		return true
	}
	return task.State.Terminal()
}

// mergeDropped stops a merge whose task left gating, as by a cancel, before the merge took the lock.
func (s *Scheduler) mergeDropped(ctx context.Context, j mergeJob) (mergeEnd, bool) {
	task, err := s.d.Store.Task(ctx, j.run.TaskID)
	if err != nil {
		return mergeEnd{err: fmt.Errorf("read task %s under the lock: %w", j.run.TaskID, err)}, true
	}
	if task.State == model.TaskGating {
		return mergeEnd{}, false
	}
	s.logFor(j.p.Name, j.run.TaskID, j.run.ID).InfoContext(ctx, MergeDroppedLog, "pr", j.ref(), "state", task.State)
	return mergeEnd{}, true
}
