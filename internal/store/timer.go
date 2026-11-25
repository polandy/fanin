package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/polandy/fanin/internal/model"
)

// Timer is a wake-up that outlives a restart: it is due at DueAt until it is disarmed.
type Timer struct {
	ID      string
	Project string
	// TaskID is empty for a project-level timer.
	TaskID model.TaskID
	// Kind says what the timer is for; the scheduler owns its values.
	Kind       string
	DueAt      time.Time
	CreatedAt  time.Time
	DisarmedAt time.Time
}

const timerColumns = `id, project, task_id, kind, due_at, created_at, disarmed_at`

// ArmTimer stores an armed timer, with a new ID when timer.ID is empty, and returns its ID.
func (t *Tx) ArmTimer(ctx context.Context, timer Timer) (string, error) {
	if timer.Project == "" || timer.Kind == "" || timer.DueAt.IsZero() {
		return "", fmt.Errorf("%w: timer %q of project %q due %s; set Project, Kind and DueAt", ErrInvalid,
			timer.Kind, timer.Project, timer.DueAt)
	}
	if timer.ID == "" {
		timer.ID = rand.Text()
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO timers (`+timerColumns+`) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		timer.ID, timer.Project, string(timer.TaskID), timer.Kind, toDB(timer.DueAt), toDB(t.now)); err != nil {
		return "", fmt.Errorf("store: arm %s timer of project %q: %w", timer.Kind, timer.Project, err)
	}
	return timer.ID, nil
}

// DisarmTimer disarms one armed timer, after it fired or when it is no longer wanted.
func (t *Tx) DisarmTimer(ctx context.Context, id string) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE timers SET disarmed_at = ? WHERE id = ? AND disarmed_at = 0`,
		toDB(t.now), id)
	if err != nil {
		return fmt.Errorf("store: disarm timer %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: disarm timer %q: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: armed timer %q", ErrNotFound, id)
	}
	return nil
}

func scanTimer(sc scanner) (Timer, error) {
	var (
		tm                     Timer
		task                   string
		due, created, disarmed int64
	)
	if err := sc.Scan(&tm.ID, &tm.Project, &task, &tm.Kind, &due, &created, &disarmed); err != nil {
		return Timer{}, err
	}
	tm.TaskID, tm.DueAt, tm.CreatedAt, tm.DisarmedAt = model.TaskID(task), fromDB(due), fromDB(created),
		fromDB(disarmed)
	return tm, nil
}

// ArmedTimers returns every armed timer by due time; a restart re-arms these.
func (s *Store) ArmedTimers(ctx context.Context) ([]Timer, error) {
	return s.timers(ctx, `disarmed_at = 0`)
}

// DueTimers returns the armed timers due at or before the store clock's now, by due time.
func (s *Store) DueTimers(ctx context.Context) ([]Timer, error) {
	return s.timers(ctx, `disarmed_at = 0 AND due_at <= ?`, toDB(s.clock.Now()))
}

func (s *Store) timers(ctx context.Context, where string, args ...any) ([]Timer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+timerColumns+` FROM timers WHERE `+where+` ORDER BY due_at, id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("store: read timers: %w", err)
	}
	return scanAll(rows, scanTimer)
}
