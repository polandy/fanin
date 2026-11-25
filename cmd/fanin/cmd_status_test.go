package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/api"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

const (
	statusToken = "owner-secret"
	projAlpha   = "alpha"
	projGamma   = "gamma"
)

var statusT0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// startDaemon serves the API over a real store with the projects beta, alpha and gamma in config order.
func startDaemon(t *testing.T) (*store.Store, *clock.Manual, string) {
	t.Helper()
	clk := clock.NewManual(statusT0)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	reg := api.NewRegistry()
	api.RegisterBuiltins(reg)
	h, err := api.New(api.Config{Source: st, OwnerToken: statusToken, Clock: clk, Registry: reg,
		Projects: []string{"beta", projAlpha, projGamma}})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return st, clk, srv.URL
}

func fillStatus(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	err := st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		for _, task := range []model.Task{
			{ID: "a1", Project: projAlpha, Title: "build it", Shape: model.ShapePR},
			{ID: "a2", Project: projAlpha, Title: "after it", Shape: model.ShapePR, After: []model.TaskID{"a1"}},
			{ID: "a3", Project: projAlpha, Title: "next", Shape: model.ShapePR},
			{ID: "g1", Project: projGamma, Title: "ask", Shape: model.ShapePR},
		} {
			if err := tx.CreateTask(ctx, task); err != nil {
				return err
			}
		}
		if err := tx.SetTaskState(ctx, "a1", model.TaskRunning, "dispatched"); err != nil {
			return err
		}
		if err := tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "a1", Number: 1, Branch: "feat/x",
			State: model.RunStarting}); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "g1", model.TaskRunning, ""); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "g1", model.TaskNeedsOwner, ""); err != nil {
			return err
		}
		if _, err := tx.SetHold(ctx, model.Hold{ID: "h1", Project: projGamma, Kind: model.HoldCI,
			Reason: "main is red", FixTask: "g9"}); err != nil {
			return err
		}
		_, err := tx.SetHold(ctx, model.Hold{ID: "h2", Project: projGamma, Kind: model.HoldMerge, Reason: "hook failed"})
		return err
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	for _, l := range []store.LockRequest{
		{Project: projAlpha, Holder: "r1", Lease: time.Hour},
		// r0's loop died: no open task or live run is called r0.
		{Project: projGamma, Holder: "r0", Lease: time.Hour},
	} {
		if err := st.AcquireLock(ctx, l); err != nil {
			t.Fatalf("AcquireLock: %v", err)
		}
	}
}

func statusRun(t *testing.T, url string, args ...string) (int, []string, string) {
	t.Helper()
	code, out, errOut := runArgs(t, defaultRegistry, map[string]string{envURL: url}, append([]string{"status"}, args...)...)
	return code, strings.Split(strings.TrimSuffix(out, "\n"), "\n"), errOut
}

func TestStatus(t *testing.T) {
	t.Run("ST-1_empty_store_lists_every_project_in_config_order", func(t *testing.T) {
		_, _, url := startDaemon(t)
		code, lines, errOut := statusRun(t, url)
		if code != cli.ExitOK {
			t.Fatalf("exit %d, stderr %s", code, errOut)
		}
		want := []string{
			"beta  loop=idle  worker=0  merge=free  last: -  open: -",
			"alpha  loop=idle  worker=0  merge=free  last: -  open: -",
			"gamma  loop=idle  worker=0  merge=free  last: -  open: -",
		}
		if diff := cmp.Diff(want, lines); diff != "" {
			t.Errorf("status (-want +got):\n%s", diff)
		}
	})

	t.Run("ST-1_loop_worker_merge_last_open_and_a_stale_lock", func(t *testing.T) {
		st, _, url := startDaemon(t)
		fillStatus(t, st)
		code, lines, errOut := statusRun(t, url)
		if code != cli.ExitOK {
			t.Fatalf("exit %d, stderr %s", code, errOut)
		}
		want := []string{
			"beta  loop=idle  worker=0  merge=free  last: -  open: -",
			"alpha  loop=active  worker=1  merge=held by r1  last: 2026-03-04T05:06:07Z run_started a1: run 1" +
				"  open: ready 1, blocked 1",
			"gamma  loop=idle  worker=0  merge=stale (r0)  last: 2026-03-04T05:06:07Z hold_set: merge hold h2: hook failed" +
				"  open: needs-owner 1, ci hold (fix g9), merge hold",
		}
		if diff := cmp.Diff(want, lines); diff != "" {
			t.Errorf("status (-want +got):\n%s", diff)
		}
	})

	t.Run("ST-1_lock_after_its_lease_ran_out_is_stale", func(t *testing.T) {
		st, clk, url := startDaemon(t)
		fillStatus(t, st)
		clk.Advance(time.Hour)
		_, lines, _ := statusRun(t, url, projAlpha)
		if len(lines) != 1 || !strings.Contains(lines[0], "merge=stale (r1)") {
			t.Errorf("status alpha = %q, want one line with merge=stale (r1)", lines)
		}
	})

	t.Run("ST-1_named_projects_only_in_the_given_order", func(t *testing.T) {
		_, _, url := startDaemon(t)
		_, lines, _ := statusRun(t, url, projGamma, "beta")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "gamma ") || !strings.HasPrefix(lines[1], "beta ") {
			t.Errorf("status gamma beta = %q, want gamma then beta", lines)
		}
	})

	t.Run("unknown_project_is_an_error", func(t *testing.T) {
		_, _, url := startDaemon(t)
		code, _, errOut := statusRun(t, url, "nope")
		if code != cli.ExitError || !strings.Contains(errOut, `"nope"`) {
			t.Errorf("exit %d, stderr %q; want a failure naming the project", code, errOut)
		}
	})

	t.Run("no_daemon_url_is_a_usage_error", func(t *testing.T) {
		code, _, errOut := runArgs(t, defaultRegistry, nil, "status")
		if code != cli.ExitUsage || !strings.Contains(errOut, envURL) {
			t.Errorf("exit %d, stderr %q; want a usage error naming %s", code, errOut, envURL)
		}
	})

	t.Run("unreachable_daemon_is_an_error_not_an_empty_status", func(t *testing.T) {
		srv := httptest.NewServer(nil)
		url := srv.URL
		srv.Close()
		code, out, errOut := runArgs(t, defaultRegistry, map[string]string{envURL: url}, "status")
		if code != cli.ExitError || out != "" {
			t.Errorf("exit %d, stdout %q, stderr %q; want a failure and no status", code, out, errOut)
		}
	})
}
