package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// Shared test values.
const (
	project      = "proj-a"
	holderA      = "task-a"
	unknownValue = "nap"
	timerPoll    = "poll"
	caseDup      = "duplicate id"
	caseNoTask   = "missing task"
)

func openStore(t *testing.T, path string, clk clock.Clock) *Store {
	t.Helper()
	s, err := Open(context.Background(), path, clk)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	closeOnCleanup(t, s)
	return s
}

// closeOnCleanup closes s at the end of the test; closing an already closed store is a no-op.
func closeOnCleanup(t *testing.T, s *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

func newStore(t *testing.T) (*Store, *clock.Manual) {
	t.Helper()
	clk := clock.NewManual(t0)
	return openStore(t, filepath.Join(t.TempDir(), "fanin.db"), clk), clk
}

func task(id string) model.Task {
	return model.Task{ID: model.TaskID(id), Project: project, Title: "title " + id, Shape: model.ShapePR}
}

func mustTx(t *testing.T, s *Store, fn func(ctx context.Context, tx *Tx) error) {
	t.Helper()
	if err := s.Transition(context.Background(), fn); err != nil {
		t.Fatalf("Transition: %v", err)
	}
}

func createTasks(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		for _, id := range ids {
			if err := tx.CreateTask(ctx, task(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

func eventTypes(t *testing.T, s *Store, id model.TaskID) []model.EventType {
	t.Helper()
	evs, err := s.Events(context.Background(), id)
	if err != nil {
		t.Fatalf("Events(%s): %v", id, err)
	}
	var out []model.EventType
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}
