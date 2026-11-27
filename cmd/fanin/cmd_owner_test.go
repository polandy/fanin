package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/api"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/store"
)

var errPaneGone = errors.New("pane gone")

type cliDeliverer struct {
	got []owner.Delivery
	err error
}

func (d *cliDeliverer) Deliver(_ context.Context, del owner.Delivery) error {
	if d.err != nil {
		return d.err
	}
	d.got = append(d.got, del)
	return nil
}

// startOwnerDaemon serves the API with the owner channel; task a1 of alpha runs in r1 and asks a dialog question.
func startOwnerDaemon(t *testing.T) (*store.Store, *cliDeliverer, string) {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewManual(statusT0)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fanin.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	del := &cliDeliverer{}
	svc := owner.New(st, del, nil)
	reg := api.NewRegistry()
	api.RegisterBuiltins(reg)
	h, err := api.New(api.Config{Source: st, OwnerToken: statusToken, Clock: clk, Registry: reg,
		Projects: []string{projAlpha}, Owner: svc})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	err = st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: "a1", Project: projAlpha, Title: "build", Shape: model.ShapePR}); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "a1", model.TaskRunning, ""); err != nil {
			return err
		}
		return tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "a1", Number: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AskDialog(ctx, owner.Dialog{TaskID: "a1", RunID: "r1", Text: "Proceed?",
		Options: []string{"yes", "no"}}); err != nil {
		t.Fatal(err)
	}
	return st, del, srv.URL
}

func ownerRun(t *testing.T, url string, args ...string) (int, string, string) {
	t.Helper()
	return runArgs(t, defaultRegistry, map[string]string{envURL: url, envToken: statusToken}, args...)
}

func TestAnswerCommand(t *testing.T) {
	t.Run("OA-1_option_number_is_delivered", func(t *testing.T) {
		_, del, url := startOwnerDaemon(t)
		code, out, errOut := ownerRun(t, url, "answer", "a1", "2")
		if code != cli.ExitOK || out != "a1: option 2: no\n" || len(del.got) != 1 {
			t.Errorf("exit %d, stdout %q, stderr %q, sent %v", code, out, errOut, del.got)
		}
	})
	t.Run("OA-1_free_text_is_refused_with_the_options", func(t *testing.T) {
		_, del, url := startOwnerDaemon(t)
		code, _, errOut := ownerRun(t, url, "answer", "a1", "go", "ahead")
		if code != cli.ExitError || !strings.Contains(errOut, "1) yes, 2) no") || len(del.got) != 0 {
			t.Errorf("exit %d, stderr %q, sent %v; want a refusal listing the options", code, errOut, del.got)
		}
	})
	t.Run("OA-3_failed_delivery_exits_one", func(t *testing.T) {
		_, del, url := startOwnerDaemon(t)
		del.err = errPaneGone
		if code, _, errOut := ownerRun(t, url, "answer", "a1", "1"); code != cli.ExitError {
			t.Errorf("exit %d, stderr %q; want 1", code, errOut)
		}
	})
	t.Run("missing_answer_is_a_usage_error", func(t *testing.T) {
		if code, _, _ := ownerRun(t, "", "answer", "a1"); code != cli.ExitUsage {
			t.Errorf("exit %d, want %d", code, cli.ExitUsage)
		}
	})
}

func TestGoHoldPauseCommands(t *testing.T) {
	t.Run("OA-6_go_on_a_ready_task", func(t *testing.T) {
		st, _, url := startOwnerDaemon(t)
		if code, _, errOut := ownerRun(t, url, "answer", "a1", "1"); code != cli.ExitOK {
			t.Fatalf("answer: exit %d %s", code, errOut)
		}
		err := st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
			return tx.SetTaskState(ctx, "a1", model.TaskGating, "")
		})
		if err != nil {
			t.Fatal(err)
		}
		if code, out, errOut := ownerRun(t, url, "go", "a1"); code != cli.ExitOK || !strings.HasPrefix(out, "a1: ") {
			t.Errorf("go: exit %d, stdout %q, stderr %q", code, out, errOut)
		}
	})
	t.Run("go_on_a_task_that_is_not_ready_fails", func(t *testing.T) {
		_, _, url := startOwnerDaemon(t)
		if code, _, _ := ownerRun(t, url, "go", "a1"); code != cli.ExitError {
			t.Errorf("exit %d, want %d", code, cli.ExitError)
		}
	})
	t.Run("MG-4_hold_lift", func(t *testing.T) {
		st, _, url := startOwnerDaemon(t)
		err := st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.SetHold(ctx, model.Hold{ID: "h1", Project: projAlpha, Kind: model.HoldMerge, Reason: "hook broke"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if code, _, errOut := ownerRun(t, url, "hold", "lift", "h1"); code != cli.ExitOK {
			t.Fatalf("hold lift: exit %d %s", code, errOut)
		}
		if holds, err := st.AllHolds(context.Background()); err != nil || len(holds) != 0 {
			t.Errorf("holds = %v %v, want none", holds, err)
		}
	})
	// see SCENARIOS FP-2, CH-3
	t.Run("FP-2_CH-3_pause_and_resume", func(t *testing.T) {
		st, _, url := startOwnerDaemon(t)
		if code, _, errOut := ownerRun(t, url, "pause", projAlpha); code != cli.ExitOK {
			t.Fatalf("pause: exit %d %s", code, errOut)
		}
		if p, err := st.Paused(context.Background(), projAlpha); err != nil || !p {
			t.Fatalf("paused = %v %v, want true", p, err)
		}
		if code, _, errOut := ownerRun(t, url, "resume", projAlpha); code != cli.ExitOK {
			t.Fatalf("resume: exit %d %s", code, errOut)
		}
		if p, err := st.Paused(context.Background(), projAlpha); err != nil || p {
			t.Errorf("paused = %v %v, want false", p, err)
		}
	})
	t.Run("usage_errors", func(t *testing.T) {
		for _, args := range [][]string{{"go"}, {"hold", "drop", "h1"}, {"pause"}, {"resume", "a", "b"},
			{"wait", "--after", "x"}, {"wait", "extra"}, {"go", "a1"}} {
			if code, _, _ := runArgs(t, defaultRegistry, nil, args...); code != cli.ExitUsage {
				t.Errorf("%v: exit %d, want %d", args, code, cli.ExitUsage)
			}
		}
	})
}

func TestTaskCancelCommand(t *testing.T) {
	t.Run("OA-8_cancel_ends_the_task_with_the_reason", func(t *testing.T) {
		st, _, url := startOwnerDaemon(t)
		code, out, errOut := ownerRun(t, url, argTask, taskCancel, "a1", "--reason", "merged outside fanind")
		if code != cli.ExitOK || !strings.HasPrefix(out, "a1: ") {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
		}
		task, err := st.Task(context.Background(), "a1")
		if err != nil || task.State != model.TaskCanceled {
			t.Errorf("task = %v %v, want %s", task.State, err, model.TaskCanceled)
		}
	})
	t.Run("OA-8_cancel_of_an_unknown_task_fails", func(t *testing.T) {
		_, _, url := startOwnerDaemon(t)
		if code, _, errOut := ownerRun(t, url, argTask, taskCancel, "nope"); code != cli.ExitError ||
			!strings.Contains(errOut, "nope") {
			t.Errorf("exit %d, stderr %q; want 1 naming the task", code, errOut)
		}
	})
	t.Run("OA-8_usage_errors", func(t *testing.T) {
		for _, args := range [][]string{{argTask, taskCancel}, {argTask, taskCancel, "--reason", "x"},
			{argTask, taskCancel, "a1", "a2"}, {argTask, taskCancel, "a1", "--why", "x"}} {
			if code, _, _ := runArgs(t, defaultRegistry, nil, args...); code != cli.ExitUsage {
				t.Errorf("%v: exit %d, want %d", args, code, cli.ExitUsage)
			}
		}
	})
}

func TestWaitCommand(t *testing.T) {
	t.Run("WA-2_wait_owner_prints_one_event_and_ends", func(t *testing.T) {
		_, _, url := startOwnerDaemon(t)
		code, out, errOut := ownerRun(t, url, "wait", "--owner", "--after", "0")
		want := " question_asked alpha a1 r1: "
		if code != cli.ExitOK || !strings.Contains(out, want) || strings.Count(out, "\n") != 1 {
			t.Errorf("wait: exit %d, stdout %q, stderr %q; want one line with %q", code, out, errOut, want)
		}
	})
	t.Run("unreachable_daemon_fails", func(t *testing.T) {
		srv := httptest.NewServer(nil)
		srv.Close()
		if code, _, _ := ownerRun(t, srv.URL, "wait"); code != cli.ExitError {
			t.Errorf("exit %d, want %d", code, cli.ExitError)
		}
	})
}
