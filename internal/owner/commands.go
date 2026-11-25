package owner

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// goDetail is the log line of the owner's go.
const goDetail = "owner go: merge"

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
