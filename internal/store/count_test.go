package store

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/polandy/fanin/internal/model"
)

func TestCounts(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	createTasks(t, s, "one", "two")

	t.Run("OB-2_tasks_per_project_and_state", func(t *testing.T) {
		got, err := s.TaskCounts(ctx)
		if err != nil {
			t.Fatalf("TaskCounts: %v", err)
		}
		want := []Count{{Project: project, Key: string(model.TaskQueued), N: 2}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("TaskCounts mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("OB-2_runs_and_events", func(t *testing.T) {
		if got, err := s.RunCounts(ctx); err != nil || len(got) != 0 {
			t.Errorf("RunCounts = %v, %v; want none", got, err)
		}
		got, err := s.EventCounts(ctx)
		if err != nil || len(got) == 0 {
			t.Errorf("EventCounts = %v, %v; want the task_created events", got, err)
		}
	})

	t.Run("OB-1_closed_store_is_an_error", func(t *testing.T) {
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		for name, fn := range map[string]func(context.Context) ([]Count, error){
			"tasks": s.TaskCounts, "runs": s.RunCounts, "events": s.EventCounts,
		} {
			if _, err := fn(ctx); err == nil {
				t.Errorf("%s: a closed store gave counts, want an error", name)
			}
		}
	})
}
