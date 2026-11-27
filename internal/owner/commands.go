package owner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// goDetail is the log line of the owner's go.
const goDetail = "owner go: merge"

// DefaultCancelReason is the reason of a cancel that gives none.
const DefaultCancelReason = "cancelled by the owner"

// cancelAnswer closes the open question of a cancelled task.
const cancelAnswer = "task cancelled: "

// Go releases a task that waits in gating for the owner's go and appends owner_go (see SCENARIOS OA-6).
func (s *Service) Go(ctx context.Context, id model.TaskID) error {
	err := s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		task, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if task.State != model.TaskGating {
			return fmt.Errorf("%w: task %q is %s; go releases only a task in %s", ErrNotReady, id, task.State,
				model.TaskGating)
		}
		_, err = tx.AppendEvent(ctx, model.Event{Type: model.EventOwnerGo, Project: task.Project, TaskID: id,
			Detail: goDetail})
		return err
	})
	if err != nil {
		return fmt.Errorf("owner: go on task %q: %w", id, err)
	}
	return nil
}

// Cancel ends a task for good with reason: its open run and question close, the task becomes canceled and
// task_cancelled is appended last. A done, merged or canceled task is ErrNotCancellable (see SCENARIOS OA-8).
func (s *Service) Cancel(ctx context.Context, id model.TaskID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = DefaultCancelReason
	}
	err := s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		task, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if task.State.Terminal() || task.State == model.TaskPostMerge {
			return fmt.Errorf("%w: task %q is %s; only an open task that is not merged can be cancelled",
				ErrNotCancellable, id, task.State)
		}
		if err := cancelRun(ctx, tx, id, reason); err != nil {
			return err
		}
		if err := closeQuestions(ctx, tx, id, reason); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, id, model.TaskCanceled, reason); err != nil {
			return err
		}
		_, err = tx.AppendEvent(ctx, model.Event{Type: model.EventTaskCancelled, Project: task.Project, TaskID: id,
			Detail: reason})
		return err
	})
	if err != nil {
		return fmt.Errorf("owner: cancel task %q: %w", id, err)
	}
	return nil
}

// cancelRun ends the task's latest run canceled when it is still open.
func cancelRun(ctx context.Context, tx *store.Tx, id model.TaskID, reason string) error {
	run, err := tx.LatestRun(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || run.State.Terminal() {
		return err
	}
	return tx.SetRunState(ctx, run.ID, model.RunCanceled, reason)
}

// closeQuestions answers each open question of the task with the cancel, so they leave the owner's list.
func closeQuestions(ctx context.Context, tx *store.Tx, id model.TaskID, reason string) error {
	for {
		q, err := tx.OpenTaskQuestion(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.AnswerQuestion(ctx, q.ID, cancelAnswer+reason); err != nil {
			return err
		}
	}
}

// LiftHold lifts one active hold (see SCENARIOS MG-4).
func (s *Service) LiftHold(ctx context.Context, id string) error {
	if err := s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		return tx.LiftHold(ctx, id)
	}); err != nil {
		return fmt.Errorf("owner: lift hold %q: %w", id, err)
	}
	return nil
}

// Pause sets the project's pause flag; the next worker start consumes it. ok is false when it was already set
// (see SCENARIOS FP-2, CH-3).
func (s *Service) Pause(ctx context.Context, project string) (ok bool, err error) {
	err = s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		ok, err = tx.RequestPause(ctx, project)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("owner: pause project %q: %w", project, err)
	}
	return ok, nil
}

// Resume clears the project's pause flag and lets it run workers again.
func (s *Service) Resume(ctx context.Context, project string) error {
	if err := s.store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		return tx.Resume(ctx, project)
	}); err != nil {
		return fmt.Errorf("owner: resume project %q: %w", project, err)
	}
	return nil
}
