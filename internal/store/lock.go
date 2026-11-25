package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LockPoll is how often WaitLock retries; it notices a release by another process or an expired lease.
const LockPoll = time.Second

// The lock sentinels.
var (
	// ErrLockHeld is returned when another holder has a live lease on the project lock.
	ErrLockHeld = errors.New("store: project lock held")
	// ErrLockTimeout is returned, next to ErrLockHeld, when WaitLock gives up.
	ErrLockTimeout = errors.New("store: project lock wait timed out")
	// ErrLockNotHeld is returned when a holder renews or releases a lock it does not hold.
	ErrLockNotHeld = errors.New("store: project lock not held")
)

// LockRequest asks for one project's lock (see SCENARIOS MG-1, FP-1).
type LockRequest struct {
	Project string
	// Holder names who holds the lock, e.g. a task or run ID.
	Holder string
	// Lease is how long the lock stays held without a renewal (see SCENARIOS MG-3).
	Lease time.Duration
}

func (r LockRequest) validate() error {
	if r.Project == "" || r.Holder == "" || r.Lease <= 0 {
		return fmt.Errorf("%w: lock request project %q holder %q lease %s; set all three, lease above 0",
			ErrInvalid, r.Project, r.Holder, r.Lease)
	}
	return nil
}

// LockHeldError reports who holds a project lock. It matches ErrLockHeld, and ErrLockTimeout when Waited > 0.
type LockHeldError struct {
	Project    string
	Holder     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
	// Waited is how long WaitLock waited before it gave up; 0 for AcquireLock.
	Waited time.Duration
}

func (e *LockHeldError) Error() string {
	if e.Waited > 0 {
		return fmt.Sprintf("store: lock of project %q held for over %s by %q (since %s); end or release that holder",
			e.Project, e.Waited, e.Holder, e.AcquiredAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("store: lock of project %q held by %q until %s; wait or release that holder",
		e.Project, e.Holder, e.ExpiresAt.Format(time.RFC3339))
}

// Unwrap lets errors.Is match ErrLockHeld and, after a wait, ErrLockTimeout.
func (e *LockHeldError) Unwrap() []error {
	if e.Waited > 0 {
		return []error{ErrLockHeld, ErrLockTimeout}
	}
	return []error{ErrLockHeld}
}

// AcquireLock takes the project lock for req.Lease; a live lease of any holder is a *LockHeldError.
func (s *Store) AcquireLock(ctx context.Context, req LockRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.clock.Now()
		var (
			holder            string
			acquired, expires int64
		)
		err := tx.QueryRowContext(ctx, `SELECT holder, acquired_at, expires_at FROM project_locks WHERE project = ?`,
			req.Project).Scan(&holder, &acquired, &expires)
		switch {
		case err == nil && now.Before(fromDB(expires)):
			return &LockHeldError{
				Project: req.Project, Holder: holder, AcquiredAt: fromDB(acquired), ExpiresAt: fromDB(expires),
			}
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("store: read lock of project %q: %w", req.Project, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_locks (project, holder, acquired_at, expires_at)
			VALUES (?, ?, ?, ?) ON CONFLICT (project) DO UPDATE SET holder = excluded.holder,
			acquired_at = excluded.acquired_at, expires_at = excluded.expires_at`,
			req.Project, req.Holder, toDB(now), toDB(now.Add(req.Lease))); err != nil {
			return fmt.Errorf("store: take lock of project %q: %w", req.Project, err)
		}
		return nil
	})
}

// WaitLock retries AcquireLock until it succeeds, ctx ends or timeout passes on the store's clock. A timeout is a
// *LockHeldError that names the holder and the time waited (see SCENARIOS MG-2).
func (s *Store) WaitLock(ctx context.Context, req LockRequest, timeout time.Duration) error {
	start := s.clock.Now()
	deadline := start.Add(timeout)
	for {
		released := s.releasedChan()
		err := s.AcquireLock(ctx, req)
		var held *LockHeldError
		if !errors.As(err, &held) {
			return err
		}
		now := s.clock.Now()
		if !now.Before(deadline) {
			held.Waited = now.Sub(start)
			return held
		}
		wake := s.clock.After(min(LockPoll, deadline.Sub(now)))
		s.waiting()
		select {
		case <-ctx.Done():
			return fmt.Errorf("store: wait for lock of project %q: %w", req.Project, ctx.Err())
		case <-released:
		case <-wake:
		}
	}
}

// RenewLock extends the holder's live lease by lease from now.
func (s *Store) RenewLock(ctx context.Context, req LockRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	now := s.clock.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE project_locks SET expires_at = ?
		WHERE project = ? AND holder = ? AND expires_at > ?`,
		toDB(now.Add(req.Lease)), req.Project, req.Holder, toDB(now))
	if err != nil {
		return fmt.Errorf("store: renew lock of project %q: %w", req.Project, err)
	}
	return lockChanged(res, req.Project, req.Holder)
}

// ReleaseLock frees the project lock held by holder and wakes every WaitLock of this store.
func (s *Store) ReleaseLock(ctx context.Context, project, holder string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM project_locks WHERE project = ? AND holder = ?`, project, holder)
	if err != nil {
		return fmt.Errorf("store: release lock of project %q: %w", project, err)
	}
	if err := lockChanged(res, project, holder); err != nil {
		return err
	}
	s.mu.Lock()
	close(s.released)
	s.released = make(chan struct{})
	s.mu.Unlock()
	return nil
}

func (s *Store) releasedChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
}

func lockChanged(res sql.Result, project, holder string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: lock of project %q: %w", project, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: project %q by %q; the lease expired or another holder has it", ErrLockNotHeld,
			project, holder)
	}
	return nil
}
