package store

import (
	"context"
	"fmt"
)

// Count is one group of a count query: Key is the state or event type, N the rows in it.
type Count struct {
	Project string
	Key     string
	N       int
}

// TaskCounts returns the tasks per project and state.
func (s *Store) TaskCounts(ctx context.Context) ([]Count, error) {
	return s.counts(ctx, "tasks per state", `SELECT project, state, COUNT(*) FROM tasks
		GROUP BY project, state ORDER BY project, state`)
}

// RunCounts returns the runs per project of their task and state.
func (s *Store) RunCounts(ctx context.Context) ([]Count, error) {
	return s.counts(ctx, "runs per state", `SELECT t.project, r.state, COUNT(*) FROM runs r
		JOIN tasks t ON t.id = r.task_id GROUP BY t.project, r.state ORDER BY t.project, r.state`)
}

// EventCounts returns the events per project and type.
func (s *Store) EventCounts(ctx context.Context) ([]Count, error) {
	return s.counts(ctx, "events per type", `SELECT project, type, COUNT(*) FROM events
		GROUP BY project, type ORDER BY project, type`)
}

func (s *Store) counts(ctx context.Context, what, query string) ([]Count, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: count %s: %w", what, err)
	}
	return scanAll(rows, func(sc scanner) (Count, error) {
		var c Count
		return c, sc.Scan(&c.Project, &c.Key, &c.N)
	})
}
