package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"

	"github.com/polandy/fanin/internal/model"
)

// SetHold stores an active hold, with a new ID when hold.ID is empty, and appends hold_set. A ci hold needs its
// FixTask (see SCENARIOS MG-4, MG-8).
func (t *Tx) SetHold(ctx context.Context, hold model.Hold) (string, error) {
	if _, err := model.ParseHoldKind(string(hold.Kind)); err != nil {
		return "", fmt.Errorf("%w: hold: %w", ErrInvalid, err)
	}
	if hold.Project == "" {
		return "", fmt.Errorf("%w: hold %q has no project; set Hold.Project", ErrInvalid, hold.Kind)
	}
	if hold.Kind == model.HoldCI && hold.FixTask == "" {
		return "", fmt.Errorf("%w: %s hold of project %q has no fix task; set Hold.FixTask", ErrInvalid,
			hold.Kind, hold.Project)
	}
	if hold.ID == "" {
		hold.ID = rand.Text()
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO holds (id, project, kind, reason, fix_task, created_at, lifted_at)
		VALUES (?, ?, ?, ?, ?, ?, 0)`, hold.ID, hold.Project, string(hold.Kind), model.CapField(hold.Reason),
		string(hold.FixTask), toDB(t.now)); err != nil {
		return "", fmt.Errorf("store: set %s hold of project %q: %w", hold.Kind, hold.Project, err)
	}
	_, err := t.AppendEvent(ctx, model.Event{
		Type: model.EventHoldSet, Project: hold.Project, TaskID: hold.FixTask,
		Detail: fmt.Sprintf("%s hold %s: %s", hold.Kind, hold.ID, hold.Reason),
	})
	return hold.ID, err
}

// LiftHold lifts one active hold and appends hold_lifted; other holds of the project stay.
func (t *Tx) LiftHold(ctx context.Context, id string) error {
	hold, err := scanHold(t.tx.QueryRowContext(ctx, `SELECT `+holdColumns+` FROM holds WHERE id = ? AND lifted_at = 0`,
		id))
	if err != nil {
		return notFound(err, "active hold", id)
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE holds SET lifted_at = ? WHERE id = ?`, toDB(t.now), id); err != nil {
		return fmt.Errorf("store: lift hold %q: %w", id, err)
	}
	_, err = t.AppendEvent(ctx, model.Event{
		Type: model.EventHoldLifted, Project: hold.Project, TaskID: hold.FixTask,
		Detail: fmt.Sprintf("%s hold %s lifted", hold.Kind, id),
	})
	return err
}

// SetHoldReason rewrites the reason of one active hold, as a failed post-merge hook does (see SCENARIOS MG-4).
func (t *Tx) SetHoldReason(ctx context.Context, id, reason string) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE holds SET reason = ? WHERE id = ? AND lifted_at = 0`,
		model.CapField(reason), id)
	if err != nil {
		return fmt.Errorf("store: set the reason of hold %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set the reason of hold %q: %w", id, err)
	}
	if n == 0 {
		return notFound(sql.ErrNoRows, "active hold", id)
	}
	return nil
}

const holdColumns = `id, project, kind, reason, fix_task, created_at, lifted_at`

func scanHold(sc scanner) (model.Hold, error) {
	var (
		h               model.Hold
		kind, fix       string
		created, lifted int64
	)
	if err := sc.Scan(&h.ID, &h.Project, &kind, &h.Reason, &fix, &created, &lifted); err != nil {
		return model.Hold{}, err
	}
	var err error
	if h.Kind, err = model.ParseHoldKind(kind); err != nil {
		return model.Hold{}, corrupt("hold", h.ID, err)
	}
	h.FixTask, h.CreatedAt, h.LiftedAt = model.TaskID(fix), fromDB(created), fromDB(lifted)
	return h, nil
}

// Holds returns the active holds of a project, oldest first.
func (s *Store) Holds(ctx context.Context, project string) ([]model.Hold, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+holdColumns+` FROM holds WHERE project = ? AND lifted_at = 0
		ORDER BY created_at, id`, project)
	if err != nil {
		return nil, fmt.Errorf("store: read holds of project %q: %w", project, err)
	}
	return scanAll(rows, scanHold)
}

// AllHolds returns the active holds of every project, oldest first.
func (s *Store) AllHolds(ctx context.Context) ([]model.Hold, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+holdColumns+` FROM holds WHERE lifted_at = 0
		ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: read active holds: %w", err)
	}
	return scanAll(rows, scanHold)
}

// MergeHold returns the oldest active hold that stops task from merging: any merge hold, and a ci hold unless task
// is its fix task. ok is false when nothing stops the merge (see SCENARIOS MG-4, MG-8).
func (s *Store) MergeHold(ctx context.Context, project string, task model.TaskID) (hold model.Hold, ok bool,
	err error) {
	return s.firstHold(ctx, project, func(h model.Hold) bool {
		return h.Kind == model.HoldMerge || h.FixTask != task
	})
}

// DispatchHold returns the oldest active ci hold that stops a fresh worker for task; its fix task is let through.
// ok is false when the worker may start (see SCENARIOS MG-8).
func (s *Store) DispatchHold(ctx context.Context, project string, task model.TaskID) (hold model.Hold, ok bool,
	err error) {
	return s.firstHold(ctx, project, func(h model.Hold) bool {
		return h.Kind == model.HoldCI && h.FixTask != task
	})
}

func (s *Store) firstHold(ctx context.Context, project string, blocks func(model.Hold) bool) (model.Hold, bool,
	error) {
	holds, err := s.Holds(ctx, project)
	if err != nil {
		return model.Hold{}, false, err
	}
	for _, h := range holds {
		if blocks(h) {
			return h, true, nil
		}
	}
	return model.Hold{}, false, nil
}
