package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/cleanup"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/hook"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/sched"
	"github.com/polandy/fanin/internal/store"
)

const deployCmd = "./deploy"

var mergeArgv = []string{"gh", "pr", "merge", "7", "--repo", ghRepo, "--squash", "--subject", "feat: x (#7)",
	"--body", "", "--match-head-commit", "abc123"}

// signalClock is a manual clock that reports every After call, so a test advances it only once the merge waits.
type signalClock struct {
	*clock.Manual
	waits chan time.Duration
}

func (c *signalClock) After(d time.Duration) <-chan time.Time {
	ch := c.Manual.After(d)
	c.waits <- d
	return ch
}

// mergedView is the gh pr view answer for PR 7 of beta after its merge.
func mergedView() string {
	return strings.Replace(ghView("SUCCESS"), `"state":"OPEN"`, `"state":"MERGED"`, 1)
}

func TestDaemonMerger(t *testing.T) {
	t.Run("MG-6_a_go_squash_merges_with_the_title_and_the_PR_number", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(viewArgv, testutil.Reply{Stdout: ghView("SUCCESS")})
		r.Expect(viewArgv, testutil.Reply{Stdout: ghView("SUCCESS")})
		r.Expect(mergeArgv, testutil.Reply{})
		if err := f.Merge(t.Context(), betaOf(t, cfg), ghPRNumber); err != nil {
			t.Errorf("Merge = %v, want nil", err)
		}
	})

	t.Run("MG-6_an_already_merged_PR_is_merged_once", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(viewArgv, testutil.Reply{Stdout: mergedView()})
		if err := f.Merge(t.Context(), betaOf(t, cfg), ghPRNumber); err != nil {
			t.Errorf("Merge = %v, want nil", err)
		}
	})

	t.Run("MG-6_a_network_failure_retries_after_checking_for_MERGED", func(t *testing.T) {
		cfg, err := config.Load(faninConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		r := testutil.NewRecordingRunner(t)
		t.Cleanup(r.Done)
		clk := &signalClock{Manual: clock.NewManual(t0), waits: make(chan time.Duration, 1)}
		f, err := newForges(cfg, r, func(string) string { return "" }, clk, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(viewArgv, testutil.Reply{Stdout: ghView("SUCCESS")})
		r.Expect(viewArgv, testutil.Reply{Stdout: ghView("SUCCESS")})
		r.Expect(mergeArgv, testutil.Reply{ExitCode: 1, Stderr: "error connecting to api.github.com: i/o timeout"})
		r.Expect(viewArgv, testutil.Reply{Stdout: mergedView()})
		done := make(chan error, 1)
		safego.Go(t.Context(), slog.New(slog.DiscardHandler), func(ctx context.Context) {
			done <- f.Merge(ctx, betaOf(t, cfg), ghPRNumber)
		})
		var waits []time.Duration
		for merged := false; !merged; {
			select {
			case d := <-clk.waits:
				waits = append(waits, d)
				clk.Advance(d)
			case err = <-done:
				merged = true
			}
		}
		if err != nil || len(waits) != 1 {
			t.Errorf("waits %v err %v; want one wait and success without a second merge", waits, err)
		}
	})

	t.Run("MG-6_a_merge_the_forge_refuses_is_an_error", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(viewArgv, testutil.Reply{Stdout: ghView("SUCCESS")})
		r.Expect(viewArgv, testutil.Reply{Stdout: ghView("SUCCESS")})
		r.Expect(mergeArgv, testutil.Reply{ExitCode: 1, Stderr: "Pull request is not mergeable"})
		if err := f.Merge(t.Context(), betaOf(t, cfg), ghPRNumber); err == nil {
			t.Error("Merge = nil, want the forge's refusal")
		}
	})

	t.Run("MG-6_an_unreadable_view_merges_nothing", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, origin)
		r.Expect(viewArgv, testutil.Reply{Stdout: strings.Replace(ghView("SUCCESS"), `"headRefName":"feat/x"`,
			`"headRefName":""`, 1)})
		if err := f.Merge(t.Context(), betaOf(t, cfg), ghPRNumber); err == nil {
			t.Error("Merge = nil, want an error for a view without a head branch")
		}
	})

	t.Run("MG-6_an_unreadable_origin_merges_nothing", func(t *testing.T) {
		f, cfg, r, err := forgeSeam(t, faninConfig(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Expect(originArgv, testutil.Reply{ExitCode: 128, Stderr: notARepo})
		if err := f.Merge(t.Context(), betaOf(t, cfg), ghPRNumber); err == nil {
			t.Error("Merge = nil, want an error")
		}
	})
}

// mergeDeps builds the scheduler deps of a fanin config over a real store and a recording runner.
func mergeDeps(t *testing.T) (sched.Deps, config.Config, *store.Store, *testutil.RecordingRunner) {
	t.Helper()
	cfg, err := config.Load(faninConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), cfg.Store, clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	r := testutil.NewRecordingRunner(t)
	t.Cleanup(r.Done)
	d := deps{getenv: func(string) string { return "" }, clock: clock.NewManual(t0), runner: r,
		workers: func(config.Config, *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
			return idleWorkers{}, idleWorkers{}
		}}
	sd, err := schedDeps(cfg, st, nil, slog.New(slog.DiscardHandler), d)
	if err != nil {
		t.Fatal(err)
	}
	return sd, cfg, st, r
}

func TestDaemonMergeDeps(t *testing.T) {
	t.Run("MG-1_startSched_passes_the_merger_hook_and_state_dir", func(t *testing.T) {
		sd, cfg, _, _ := mergeDeps(t)
		if _, ok := sd.Merger.(*forges); !ok {
			t.Errorf("Merger = %T, want *forges", sd.Merger)
		}
		if _, ok := sd.Cleaner.(*cleanup.Cleaner); !ok {
			t.Errorf("Cleaner = %T, want *cleanup.Cleaner (MG-7)", sd.Cleaner)
		}
		if _, ok := sd.Hook.(*hook.Hook); !ok {
			t.Errorf("Hook = %T, want *hook.Hook", sd.Hook)
		}
		want := filepath.Join(filepath.Dir(cfg.Store), StateDir)
		if info, err := os.Stat(sd.StateDir); sd.StateDir != want || err != nil || !info.IsDir() {
			t.Errorf("StateDir = %q (%v), want the directory %q", sd.StateDir, err, want)
		}
		if _, err := sched.New(sd); err != nil {
			t.Errorf("sched.New = %v, want the merge deps accepted", err)
		}
	})

	t.Run("MG-1_the_hook_runs_in_the_state_dir_and_lifts_its_hold", func(t *testing.T) {
		sd, _, st, r := mergeDeps(t)
		r.Expect([]string{hook.Shell, hook.ShellFlag, deployCmd}, testutil.Reply{})
		err := sd.Hook.Run(t.Context(), hook.Request{Project: projectBeta, Task: "b1", PR: ghPRNumber,
			Command: deployCmd, Dir: sd.StateDir})
		if err != nil {
			t.Fatalf("Hook.Run = %v, want nil", err)
		}
		if calls := r.Calls(); len(calls) != 1 || calls[0].Dir != sd.StateDir {
			t.Errorf("hook calls = %+v, want one in %s", calls, sd.StateDir)
		}
		if _, held, err := st.MergeHold(t.Context(), projectBeta, "b2"); err != nil || held {
			t.Errorf("MergeHold = %v, %v; want the hold lifted", held, err)
		}
	})

	t.Run("MG-4_a_failed_hook_holds_every_merge_of_the_project", func(t *testing.T) {
		sd, _, st, r := mergeDeps(t)
		r.Expect([]string{hook.Shell, hook.ShellFlag, deployCmd}, testutil.Reply{ExitCode: 1,
			Stdout: "switching\nactivation failed\n"})
		err := sd.Hook.Run(t.Context(), hook.Request{Project: projectBeta, Task: "b1", PR: ghPRNumber,
			Command: deployCmd, Dir: sd.StateDir})
		var failed *hook.FailedError
		if !errors.As(err, &failed) || failed.LastLine != "activation failed" {
			t.Fatalf("Hook.Run = %v, want a FailedError naming the last line", err)
		}
		h, held, err := st.MergeHold(t.Context(), projectBeta, "b2")
		if err != nil || !held || h.Kind != model.HoldMerge || h.Reason != hook.FailedPrefix+"activation failed" {
			t.Errorf("MergeHold = %+v, %v, %v; want the failed merge hold", h, held, err)
		}
	})

	t.Run("GT-8_the_pre_go_check_runs_under_the_state_dir", func(t *testing.T) {
		sd, cfg, _, r := mergeDeps(t)
		p := betaOf(t, cfg)
		tree := filepath.Join(sd.StateDir, hook.PreGoDir, projectBeta+"-7")
		r.Expect([]string{gitBin, "-C", p.Path, "fetch", "--quiet", "origin", baseBranch, "feat/x"}, testutil.Reply{})
		r.Expect([]string{gitBin, "-C", p.Path, "worktree", "add", "--force", "--detach", tree, "FETCH_HEAD"},
			testutil.Reply{})
		r.Expect([]string{hook.Shell, hook.ShellFlag, "./check"}, testutil.Reply{ExitCode: 1, Stdout: "would: restart"})
		r.Expect([]string{gitBin, "-C", p.Path, "worktree", "remove", "--force", tree}, testutil.Reply{})
		_, err := sd.PreGo.Run(t.Context(), hook.PreGoRequest{Project: projectBeta, PR: ghPRNumber, Path: p.Path,
			Base: baseBranch, Branch: "feat/x", Command: "./check", Dir: sd.StateDir})
		if !errors.Is(err, hook.ErrPreGoFailed) {
			t.Errorf("PreGo.Run = %v, want ErrPreGoFailed", err)
		}
	})

	t.Run("MG-1_a_state_dir_that_cannot_be_made_is_an_error", func(t *testing.T) {
		cfg, err := config.Load(faninConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.Store), StateDir), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		d := deps{getenv: func(string) string { return "" }, clock: clock.NewManual(t0),
			runner: testutil.NewRecordingRunner(t),
			workers: func(config.Config, *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
				return idleWorkers{}, idleWorkers{}
			}}
		if _, err := schedDeps(cfg, nil, nil, slog.New(slog.DiscardHandler), d); err == nil {
			t.Error("schedDeps = nil, want an error for a state dir that is a file")
		}
	})
}
