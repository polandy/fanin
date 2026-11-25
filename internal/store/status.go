package store

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/model"
)

// Locks returns every project lock row by project, expired leases included: status shows those as stale.
func (s *Store) Locks(ctx context.Context) ([]model.Lock, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project, holder, acquired_at, expires_at FROM project_locks ORDER BY project`)
	if err != nil {
		return nil, fmt.Errorf("store: read project locks: %w", err)
	}
	return scanAll(rows, func(sc scanner) (model.Lock, error) {
		var (
			l                 model.Lock
			acquired, expires int64
		)
		if err := sc.Scan(&l.Project, &l.Holder, &acquired, &expires); err != nil {
			return model.Lock{}, err
		}
		l.AcquiredAt, l.ExpiresAt = fromDB(acquired), fromDB(expires)
		return l, nil
	})
}

// LatestEvents returns the newest event of each project, by project.
func (s *Store) LatestEvents(ctx context.Context) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, type, project, task_id, run_id, at, detail FROM events
		WHERE seq IN (SELECT MAX(seq) FROM events GROUP BY project) ORDER BY project`)
	if err != nil {
		return nil, fmt.Errorf("store: read the latest events: %w", err)
	}
	return scanAll(rows, scanEvent)
}
