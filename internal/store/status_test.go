package store

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

// TestLocksListsEveryRow covers the lock view of status (see SCENARIOS ST-1).
func TestLocksListsEveryRow(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	empty, err := s.Locks(ctx)
	if err != nil || len(empty) != 0 {
		t.Fatalf("Locks on an empty store = %v, %v; want none", empty, err)
	}
	if err := s.AcquireLock(ctx, LockRequest{Project: projectB, Holder: holderB, Lease: lease}); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireLock(ctx, req(holderA)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * lease)
	got, err := s.Locks(ctx)
	if err != nil {
		t.Fatalf("Locks: %v", err)
	}
	// An expired lease stays listed: status shows it as stale.
	want := []model.Lock{
		{Project: project, Holder: holderA, AcquiredAt: t0, ExpiresAt: t0.Add(lease)},
		{Project: projectB, Holder: holderB, AcquiredAt: t0, ExpiresAt: t0.Add(lease)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Locks (-want +got):\n%s", diff)
	}
}

func TestLatestEventsNewestPerProject(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	other := task("t2")
	other.Project = projectB
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		if err := tx.CreateTask(ctx, task("t1")); err != nil {
			return err
		}
		if err := tx.CreateTask(ctx, other); err != nil {
			return err
		}
		return tx.SetTaskState(ctx, "t1", model.TaskRunning, "picked")
	})
	got, err := s.LatestEvents(ctx)
	if err != nil {
		t.Fatalf("LatestEvents: %v", err)
	}
	want := []model.Event{
		{Seq: 3, Type: model.EventTaskState, Project: project, TaskID: "t1", At: t0, Detail: "picked"},
		{Seq: 2, Type: model.EventTaskCreated, Project: projectB, TaskID: "t2", At: t0, Detail: "title t2"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("LatestEvents (-want +got):\n%s", diff)
	}
}

func TestStatusReadsFailOnAClosedStore(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Locks(ctx); err == nil {
		t.Error("Locks on a closed store: nil error, want the read error")
	}
	if _, err := s.LatestEvents(ctx); err == nil {
		t.Error("LatestEvents on a closed store: nil error, want the read error")
	}
}

// TestDoneTasksListsOnlyDoneOldestUpdateFirst covers the board's Erledigt data (see SCENARIOS ST-2).
func TestDoneTasksListsOnlyDoneOldestUpdateFirst(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	finish := func(id string) {
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			if err := tx.CreateTask(ctx, task(id)); err != nil {
				return err
			}
			for _, st := range []model.TaskState{model.TaskRunning, model.TaskGating, model.TaskDone} {
				if err := tx.SetTaskState(ctx, model.TaskID(id), st, ""); err != nil {
					return err
				}
			}
			return nil
		})
		clk.Advance(lease)
	}
	finish("t2")
	finish("t1")
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.CreateTask(ctx, task("t3")) })
	got, err := s.DoneTasks(ctx)
	if err != nil {
		t.Fatalf("DoneTasks: %v", err)
	}
	var ids []model.TaskID
	for _, tk := range got {
		ids = append(ids, tk.ID)
	}
	if diff := cmp.Diff([]model.TaskID{"t2", "t1"}, ids); diff != "" {
		t.Errorf("DoneTasks ids (-want +got):\n%s", diff)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DoneTasks(ctx); err == nil {
		t.Error("DoneTasks on a closed store: nil error, want the read error")
	}
}

func TestReady(t *testing.T) {
	t.Run("CH-2_holds_only_the_dependents", testReadyHoldsOnlyDependents)
}

func testReadyHoldsOnlyDependents(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		for _, tk := range []model.Task{
			task("a"),
			{ID: "b", Project: project, Title: "b", Shape: model.ShapePR, After: []model.TaskID{"a"}},
			{ID: "c", Project: project, Title: "c", Shape: model.ShapePR, After: []model.TaskID{"b"}},
			task("d"),
			{ID: "e", Project: project, Title: "e", Shape: model.ShapePR, After: []model.TaskID{"d"}},
		} {
			if err := tx.CreateTask(ctx, tk); err != nil {
				return err
			}
		}
		for _, st := range []model.TaskState{model.TaskRunning, model.TaskGating, model.TaskPostMerge, model.TaskDone} {
			if err := tx.SetTaskState(ctx, "d", st, ""); err != nil {
				return err
			}
		}
		for _, st := range []model.TaskState{model.TaskRunning, model.TaskFailed} {
			if err := tx.SetTaskState(ctx, "a", st, ""); err != nil {
				return err
			}
		}
		return nil
	})
	got, err := s.Ready(ctx, project)
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	var ids []string
	for _, tk := range got {
		ids = append(ids, string(tk.ID))
	}
	if diff := cmp.Diff([]string{"e"}, ids); diff != "" {
		t.Errorf("ready (-want +got): a failed task holds b and c; d is done, so e is free:\n%s", diff)
	}
}

func TestNextTaskIDSkipsTakenIDs(t *testing.T) {
	s, _ := newStore(t)
	createTasks(t, s, project+"-2")
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		id, err := tx.NextTaskID(ctx, project)
		if err != nil {
			return err
		}
		if id != project+"-3" {
			t.Errorf("NextTaskID = %q; want the count-based %s-2 skipped as taken", id, project)
		}
		return tx.CreateTask(ctx, model.Task{ID: id, Project: project, Title: "n", Shape: model.ShapePR})
	})
}
