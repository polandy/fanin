package store

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/model"
)

// pauseConsumedDetail is the log line of a pause that stopped the project before its next worker.
const pauseConsumedDetail = "stopped on request"

// RequestPause sets the pause flag of project and appends project_paused. ok is false, and nothing is written,
// when the flag is already set (see SCENARIOS FP-2, CH-3).
func (t *Tx) RequestPause(ctx context.Context, project string) (ok bool, err error) {
	if project == "" {
		return false, fmt.Errorf("%w: pause without a project; name the project", ErrInvalid)
	}
	res, err := t.tx.ExecContext(ctx, `INSERT INTO project_pauses (project, requested_at) VALUES (?, ?)
		ON CONFLICT (project) DO NOTHING`, project, toDB(t.now))
	if err != nil {
		return false, fmt.Errorf("store: pause project %q: %w", project, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, rowsErr(err, "pause project", project)
	}
	_, err = t.AppendEvent(ctx, model.Event{Type: model.EventProjectPaused, Project: project, Detail: "pause requested"})
	return err == nil, err
}

// Resume clears the pause flag of project, if set, and appends project_resumed: the project runs workers again.
func (t *Tx) Resume(ctx context.Context, project string) error {
	if project == "" {
		return fmt.Errorf("%w: resume without a project; name the project", ErrInvalid)
	}
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM project_pauses WHERE project = ?`, project); err != nil {
		return fmt.Errorf("store: resume project %q: %w", project, err)
	}
	_, err := t.AppendEvent(ctx, model.Event{Type: model.EventProjectResumed, Project: project, Detail: "resumed"})
	return err
}

// ConsumePause is called before the next worker of project starts. When the pause flag is set it clears it,
// appends pause_consumed and returns true: the worker does not start (see SCENARIOS FP-2, CH-3).
func (t *Tx) ConsumePause(ctx context.Context, project string) (bool, error) {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM project_pauses WHERE project = ?`, project)
	if err != nil {
		return false, fmt.Errorf("store: consume the pause of project %q: %w", project, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, rowsErr(err, "consume the pause of project", project)
	}
	_, err = t.AppendEvent(ctx, model.Event{Type: model.EventPauseConsumed, Project: project,
		Detail: pauseConsumedDetail})
	return err == nil, err
}

// Paused reports whether project has a pause flag that no worker start consumed yet.
func (s *Store) Paused(ctx context.Context, project string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_pauses WHERE project = ?`, project).
		Scan(&n); err != nil {
		return false, fmt.Errorf("store: read the pause of project %q: %w", project, err)
	}
	return n > 0, nil
}

// rowsErr wraps a failed RowsAffected; nil when there was none (no row changed).
func rowsErr(err error, what, project string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("store: %s %q: %w", what, project, err)
}
