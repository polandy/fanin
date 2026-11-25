package owner

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

func TestGo(t *testing.T) {
	t.Run("OA-6_go_on_a_ready_task_appends_the_release", func(t *testing.T) {
		e := newEnv(t)
		if err := e.st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
			return tx.SetTaskState(ctx, taskID, model.TaskGating, "")
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Go(context.Background(), taskID); err != nil {
			t.Fatal(err)
		}
		if d := cmp.Diff([]string{goDetail}, e.events(t, model.EventOwnerGo)); d != "" {
			t.Errorf("owner_go events (-want +got):\n%s", d)
		}
	})
	t.Run("go_on_a_task_that_is_not_ready", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.Go(context.Background(), taskID); !errors.Is(err, ErrNotReady) {
			t.Errorf("Go on a running task = %v, want ErrNotReady", err)
		}
		if got := e.events(t, model.EventOwnerGo); len(got) != 0 {
			t.Errorf("owner_go events %v, want none", got)
		}
	})
	t.Run("go_on_an_unknown_task", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.Go(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Go = %v, want ErrNotFound", err)
		}
	})
}

func TestLiftHold(t *testing.T) {
	t.Run("MG-4_hold_lift_lifts_the_hold", func(t *testing.T) {
		e := newEnv(t)
		var id string
		if err := e.st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
			var err error
			id, err = tx.SetHold(ctx, model.Hold{Project: proj, Kind: model.HoldMerge, Reason: "failed: hook"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.LiftHold(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		holds, err := e.st.Holds(context.Background(), proj)
		if err != nil || len(holds) != 0 {
			t.Errorf("holds after the lift = %v %v, want none", holds, err)
		}
	})
	t.Run("unknown_hold", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.LiftHold(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("LiftHold = %v, want ErrNotFound", err)
		}
	})
}

func TestPause(t *testing.T) {
	// see SCENARIOS FP-2, CH-3
	t.Run("FP-2_CH-3_pause_sets_the_flag_resume_clears_it", func(t *testing.T) {
		e := newEnv(t)
		ctx := context.Background()
		ok, err := e.svc.Pause(ctx, proj)
		if err != nil || !ok {
			t.Fatalf("Pause = %v %v, want true", ok, err)
		}
		if ok, err := e.svc.Pause(ctx, proj); err != nil || ok {
			t.Errorf("second Pause = %v %v, want false", ok, err)
		}
		if p, err := e.st.Paused(ctx, proj); err != nil || !p {
			t.Errorf("Paused = %v %v, want true", p, err)
		}
		if err := e.svc.Resume(ctx, proj); err != nil {
			t.Fatal(err)
		}
		if p, err := e.st.Paused(ctx, proj); err != nil || p {
			t.Errorf("Paused after resume = %v %v, want false", p, err)
		}
	})
	t.Run("no_project", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Pause(context.Background(), ""); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("Pause = %v, want ErrInvalid", err)
		}
		if err := e.svc.Resume(context.Background(), ""); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("Resume = %v, want ErrInvalid", err)
		}
	})
}
