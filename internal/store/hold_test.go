package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

func setHold(t *testing.T, s *Store, h model.Hold) string {
	t.Helper()
	var id string
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		var err error
		id, err = tx.SetHold(ctx, h)
		return err
	})
	return id
}

func liftHold(t *testing.T, s *Store, id string) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.LiftHold(ctx, id) })
}

type holdCheck struct {
	merge, dispatch string // ID of the blocking hold, "" when free
}

func checkHolds(t *testing.T, s *Store, task model.TaskID, want holdCheck) {
	t.Helper()
	ctx := context.Background()
	for _, c := range []struct {
		what string
		fn   func(context.Context, string, model.TaskID) (model.Hold, bool, error)
		want string
	}{{"MergeHold", s.MergeHold, want.merge}, {"DispatchHold", s.DispatchHold, want.dispatch}} {
		h, ok, err := c.fn(ctx, project, task)
		if err != nil {
			t.Fatalf("%s(%s): %v", c.what, task, err)
		}
		if ok != (c.want != "") || h.ID != c.want {
			t.Errorf("%s(%s) = %q %v, want %q", c.what, task, h.ID, ok, c.want)
		}
	}
}

func TestHolds(t *testing.T) {
	t.Run("MG-4_merge_hold_stops_every_merge_until_lifted", func(t *testing.T) {
		s, _ := newStore(t)
		id := setHold(t, s, model.Hold{Project: project, Kind: model.HoldMerge, Reason: "failed: deploy exited 1"})
		for _, task := range []model.TaskID{"t1", "t2"} {
			checkHolds(t, s, task, holdCheck{merge: id})
		}
		other, ok, err := s.MergeHold(context.Background(), projectB, "t1")
		if err != nil || ok {
			t.Errorf("MergeHold of another project = %q %v %v, want none", other.ID, ok, err)
		}
		liftHold(t, s, id)
		checkHolds(t, s, "t1", holdCheck{})
		want := []model.EventType{model.EventHoldSet, model.EventHoldLifted}
		if d := cmp.Diff(want, eventTypes(t, s, "")); d != "" {
			t.Errorf("project events (-want +got):\n%s", d)
		}
	})

	t.Run("MG-8_ci_hold_stops_workers_and_merges_but_its_fix_task", func(t *testing.T) {
		s, _ := newStore(t)
		id := setHold(t, s, model.Hold{Project: project, Kind: model.HoldCI, Reason: "main red", FixTask: "fix"})
		checkHolds(t, s, "t1", holdCheck{merge: id, dispatch: id})
		checkHolds(t, s, "fix", holdCheck{})
		if d := cmp.Diff([]model.EventType{model.EventHoldSet}, eventTypes(t, s, "fix")); d != "" {
			t.Errorf("fix task events (-want +got):\n%s", d)
		}
	})

	t.Run("MG-4 MG-8 holds are lifted one by one", func(t *testing.T) {
		s, _ := newStore(t)
		ci := setHold(t, s, model.Hold{Project: project, Kind: model.HoldCI, FixTask: "fix"})
		merge := setHold(t, s, model.Hold{ID: "merge-1", Project: project, Kind: model.HoldMerge})
		checkHolds(t, s, "fix", holdCheck{merge: merge})
		checkHolds(t, s, "t1", holdCheck{merge: ci, dispatch: ci})
		liftHold(t, s, ci)
		checkHolds(t, s, "t1", holdCheck{merge: merge})
		liftHold(t, s, merge)
		checkHolds(t, s, "t1", holdCheck{})
		holds, err := s.Holds(context.Background(), project)
		if err != nil || len(holds) != 0 {
			t.Errorf("Holds = %v %v, want none active", holds, err)
		}
	})
}

func TestSetHoldReason(t *testing.T) {
	t.Run("MG-4_failed_hook_rewrites_the_reason_of_its_hold", func(t *testing.T) {
		s, _ := newStore(t)
		id := setHold(t, s, model.Hold{Project: project, Kind: model.HoldMerge, Reason: "hook running"})
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			return tx.SetHoldReason(ctx, id, "failed: deploy exited 1")
		})
		holds, err := s.Holds(context.Background(), project)
		if err != nil || len(holds) != 1 || holds[0].Reason != "failed: deploy exited 1" {
			t.Errorf("Holds = %+v %v, want the new reason", holds, err)
		}
	})
	t.Run("MG-4_a_lifted_or_unknown_hold_is_not_found", func(t *testing.T) {
		s, _ := newStore(t)
		id := setHold(t, s, model.Hold{Project: project, Kind: model.HoldMerge})
		liftHold(t, s, id)
		for _, h := range []string{id, "nope"} {
			err := s.Transition(context.Background(), func(ctx context.Context, tx *Tx) error {
				return tx.SetHoldReason(ctx, h, "failed")
			})
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("SetHoldReason(%s) = %v, want ErrNotFound", h, err)
			}
		}
	})
}

func TestHoldFailures(t *testing.T) {
	s, _ := newStore(t)
	id := setHold(t, s, model.Hold{ID: "h1", Project: project, Kind: model.HoldMerge})
	liftHold(t, s, id)
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx *Tx) error
		want error
	}{
		{"unknown kind", func(ctx context.Context, tx *Tx) error {
			return second(tx.SetHold(ctx, model.Hold{Project: project, Kind: unknownValue}))
		}, model.ErrUnknownValue},
		{"no project", func(ctx context.Context, tx *Tx) error {
			return second(tx.SetHold(ctx, model.Hold{Kind: model.HoldMerge}))
		}, ErrInvalid},
		{"ci without fix task", func(ctx context.Context, tx *Tx) error {
			return second(tx.SetHold(ctx, model.Hold{Project: project, Kind: model.HoldCI}))
		}, ErrInvalid},
		{caseDup, func(ctx context.Context, tx *Tx) error {
			return second(tx.SetHold(ctx, model.Hold{ID: "h1", Project: project, Kind: model.HoldMerge}))
		}, nil},
		{"lift twice", func(ctx context.Context, tx *Tx) error { return tx.LiftHold(ctx, "h1") }, ErrNotFound},
		{"lift unknown", func(ctx context.Context, tx *Tx) error { return tx.LiftHold(ctx, "nope") }, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Transition(context.Background(), tt.fn)
			if err == nil {
				t.Fatal("Transition succeeded, want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("Transition: %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := s.db.ExecContext(context.Background(), `UPDATE holds SET kind = 'nap', lifted_at = 0`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DispatchHold(context.Background(), project, "t1"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("DispatchHold on a corrupt row: %v, want ErrCorrupt", err)
	}
}
