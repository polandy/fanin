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

func TestCancel(t *testing.T) {
	const reason = "merged outside fanind"
	to := func(states ...model.TaskState) func(ctx context.Context, tx *store.Tx) error {
		return func(ctx context.Context, tx *store.Tx) error {
			for _, s := range states {
				if err := tx.SetTaskState(ctx, taskID, s, ""); err != nil {
					return err
				}
			}
			return nil
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(ctx context.Context, tx *store.Tx) error
	}{
		{"OA-8_a_running_task_ends_with_its_run", to()},
		{"OA-8_a_gating_task_ends_with_its_run", to(model.TaskGating)},
		{"OA-8_a_needs_owner_task_ends_with_its_run", to(model.TaskNeedsOwner)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.st.Transition(context.Background(), tc.setup); err != nil {
				t.Fatal(err)
			}
			if err := e.svc.Cancel(context.Background(), taskID, reason); err != nil {
				t.Fatal(err)
			}
			if got := e.state(t); got != model.TaskCanceled {
				t.Errorf("task state = %s, want %s", got, model.TaskCanceled)
			}
			run, err := e.st.Run(context.Background(), runID)
			if err != nil || run.State != model.RunCanceled {
				t.Errorf("run = %v %v, want %s", run.State, err, model.RunCanceled)
			}
			if d := cmp.Diff([]string{reason}, e.events(t, model.EventTaskCancelled)); d != "" {
				t.Errorf("task_cancelled events (-want +got):\n%s", d)
			}
		})
	}
	t.Run("OA-8_a_queued_task_leaves_the_queue_with_the_default_reason", func(t *testing.T) {
		e := newEnv(t)
		if err := e.st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
			return tx.CreateTask(ctx, model.Task{ID: "t2", Project: proj, Title: "next", Shape: model.ShapePR})
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Cancel(context.Background(), "t2", " "); err != nil {
			t.Fatal(err)
		}
		ready, err := e.st.Ready(context.Background(), proj)
		if err != nil || len(ready) != 0 {
			t.Errorf("ready = %v %v, want none", ready, err)
		}
		evs, err := e.st.Events(context.Background(), "t2")
		if err != nil {
			t.Fatal(err)
		}
		last := evs[len(evs)-1]
		if last.Type != model.EventTaskCancelled || last.Detail != DefaultCancelReason {
			t.Errorf("last event = %s %q, want %s %q", last.Type, last.Detail, model.EventTaskCancelled,
				DefaultCancelReason)
		}
	})
	t.Run("OA-8_the_open_question_of_a_cancelled_task_closes", func(t *testing.T) {
		e := newEnv(t)
		e.ask(t, model.QuestionDialog, false)
		if err := e.svc.Cancel(context.Background(), taskID, reason); err != nil {
			t.Fatal(err)
		}
		if qs, err := e.st.OpenQuestions(context.Background()); err != nil || len(qs) != 0 {
			t.Errorf("open questions = %v %v, want none", qs, err)
		}
	})
	for _, tc := range []struct {
		name   string
		states []model.TaskState
	}{
		{"OA-8_a_done_task_cannot_be_cancelled", []model.TaskState{model.TaskGating, model.TaskDone}},
		{"OA-8_a_merged_task_in_post_merge_cannot_be_cancelled", []model.TaskState{model.TaskGating, model.TaskPostMerge}},
		{"OA-8_a_cancelled_task_cannot_be_cancelled_again", []model.TaskState{model.TaskCanceled}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.st.Transition(context.Background(), to(tc.states...)); err != nil {
				t.Fatal(err)
			}
			if err := e.svc.Cancel(context.Background(), taskID, reason); !errors.Is(err, ErrNotCancellable) {
				t.Errorf("Cancel = %v, want ErrNotCancellable", err)
			}
			if got := e.events(t, model.EventTaskCancelled); len(got) != 0 {
				t.Errorf("task_cancelled events %v, want none", got)
			}
		})
	}
	t.Run("OA-8_cancel_of_an_unknown_task", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.Cancel(context.Background(), "nope", reason); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Cancel = %v, want ErrNotFound", err)
		}
	})
}
