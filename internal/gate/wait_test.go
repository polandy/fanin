package gate_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/store"
)

// maxTurns bounds a driven gate so a policy bug fails the test instead of looping.
const maxTurns = 200

// drive turns the gate until it is final, moving the manual clock to each armed timer; it returns the final step
// and the armed timers in order.
func drive(t *testing.T, f *fakeForge, lim config.Gate, tg gate.Target) (gate.Step, []store.Timer) {
	t.Helper()
	clk := clock.NewManual(start)
	w := gate.NewWatcher(f, clk, lim, project, taskID, tg)
	timers := &fakeTimers{}
	var st gate.State
	for range maxTurns {
		step, err := w.Turn(context.Background(), timers, st)
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}
		if step.Final {
			return step, timers.armed
		}
		if step.Timer.ID == "" || !step.Timer.DueAt.After(clk.Now()) {
			t.Fatalf("armed timer %+v, want an ID and a due time after %s", step.Timer, clk.Now())
		}
		clk.Advance(step.Timer.DueAt.Sub(clk.Now()))
		st = step.State
	}
	t.Fatalf("gate not final after %d turns", maxTurns)
	return gate.Step{}, nil
}

// kinds returns the kind and the gap to the previous due time of each timer.
func kinds(timers []store.Timer) []string {
	out := make([]string, 0, len(timers))
	prev := start
	for _, tm := range timers {
		out = append(out, fmt.Sprintf("%s+%s", tm.Kind, tm.DueAt.Sub(prev)))
		prev = tm.DueAt
	}
	return out
}

func repeat(n int, s string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func pendingPR(checks ...forge.Check) forge.PR { return openPR(checks...) }

func TestGateUnreachable(t *testing.T) {
	t.Run("GH-6_three_failed_reads_60s_apart_then_unreachable", func(t *testing.T) {
		f := failing(errBroken)
		step, armed := drive(t, f, limits(), githubTarget())
		if !errors.Is(step.Err, gate.ErrUnreachable) || !errors.Is(step.Err, errBroken) {
			t.Fatalf("final err = %v, want gate.ErrUnreachable wrapping the read error", step.Err)
		}
		if step.Result.Verdict != "" {
			t.Errorf("unreachable gate gave verdict %q; nobody may be nudged", step.Result.Verdict)
		}
		if diff := cmp.Diff(repeat(2, "gate_retry+1m0s"), kinds(armed)); diff != "" {
			t.Errorf("timers (-want +got):\n%s", diff)
		}
		if f.prCalls != gate.MaxReadErrors {
			t.Errorf("PR reads = %d, want %d", f.prCalls, gate.MaxReadErrors)
		}
		if armed[0].Project != project || armed[0].TaskID != taskID {
			t.Errorf("timer %+v, want project %s task %s", armed[0], project, taskID)
		}
	})
	t.Run("GH-6_a_good_read_resets_the_errors", func(t *testing.T) {
		f := &fakeForge{reads: []read{{err: errBroken}, {err: errBroken}, {pr: openPR()}}}
		step, armed := drive(t, f, limits(), githubTarget())
		if step.Err != nil || step.Result.Verdict != gate.Green || step.State.ReadErrors != 0 {
			t.Fatalf("final step %+v, want GREEN with no read errors left", step)
		}
		if len(armed) != 2 {
			t.Errorf("timers = %v, want 2 retries", kinds(armed))
		}
	})
	t.Run("GT-6_gitea_pr_unreadable_retries_then_unreachable", func(t *testing.T) {
		step, armed := drive(t, failing(forge.ErrUnreadable), limits(), giteaTarget())
		if !errors.Is(step.Err, gate.ErrUnreachable) || len(armed) != gate.MaxReadErrors-1 {
			t.Fatalf("final err = %v after %v, want gate unreachable after 2 retries", step.Err, kinds(armed))
		}
	})
	t.Run("GT-6_invalid_target_is_final_at_once", func(t *testing.T) {
		tg := giteaTarget()
		tg.Number = 0
		step, armed := drive(t, answer(openPR()), limits(), tg)
		if !errors.Is(step.Err, forge.ErrInvalidRequest) || errors.Is(step.Err, gate.ErrUnreachable) || len(armed) != 0 {
			t.Fatalf("final err = %v after %v, want forge.ErrInvalidRequest and no timer", step.Err, kinds(armed))
		}
	})
}

func TestGitHubWaits(t *testing.T) {
	running := pendingPR(check("ci", forge.CheckPending, 0), review(forge.CheckSuccess, 1, ""))
	t.Run("GH-7_pending_waits_12_times_1800s_then_red", func(t *testing.T) {
		step, armed := drive(t, answer(running), limits(), githubTarget())
		want := redFor("still checks running after 12 waits")
		if diff := cmp.Diff(want, step.Result); diff != "" || step.Err != nil {
			t.Fatalf("final (-want +got):\n%s err %v", diff, step.Err)
		}
		if diff := cmp.Diff(repeat(12, waitTimer), kinds(armed)); diff != "" {
			t.Errorf("timers (-want +got):\n%s", diff)
		}
	})
	t.Run("GH-7_settled_checks_are_read_again_after_settle", func(t *testing.T) {
		f := &fakeForge{reads: []read{{pr: running}, {pr: running}, {pr: openPR()}}}
		step, armed := drive(t, f, limits(), githubTarget())
		if step.Result.Verdict != gate.Green || f.prCalls != 4 {
			t.Fatalf("final %+v after %d reads, want GREEN after 4", step.Result, f.prCalls)
		}
		want := []string{waitTimer, waitTimer, "gate_settle+20s"}
		if diff := cmp.Diff(want, kinds(armed)); diff != "" {
			t.Errorf("timers (-want +got):\n%s", diff)
		}
	})
	t.Run("GH-7_green_at_once_needs_no_settle", func(t *testing.T) {
		step, armed := drive(t, answer(openPR()), limits(), githubTarget())
		if step.Result.Verdict != gate.Green || len(armed) != 0 {
			t.Fatalf("final %+v with timers %v, want GREEN and no timer", step.Result, kinds(armed))
		}
	})
	t.Run("GH-7_network_failures_use_no_wait_at_most_6", func(t *testing.T) {
		f := &fakeForge{reads: []read{{pr: running}, {err: forge.ErrNetwork}}}
		step, armed := drive(t, f, limits(), githubTarget())
		if !errors.Is(step.Err, gate.ErrUnreachable) || !errors.Is(step.Err, forge.ErrNetwork) {
			t.Fatalf("final err = %v, want gate unreachable wrapping forge.ErrNetwork", step.Err)
		}
		want := append([]string{waitTimer}, repeat(6, "gate_retry+1m0s")...)
		if diff := cmp.Diff(want, kinds(armed)); diff != "" {
			t.Errorf("timers (-want +got):\n%s", diff)
		}
		if step.State.Waits != 1 || step.State.WatchFailures != 7 {
			t.Errorf("state %+v, want 1 wait and 7 watch failures", step.State)
		}
	})
	t.Run("GH-7_network_failures_between_waits_do_not_end_the_wait", func(t *testing.T) {
		f := &fakeForge{reads: []read{{pr: running}, {err: forge.ErrNetwork}, {err: forge.ErrNetwork},
			{err: forge.ErrNetwork}, {pr: openPR()}}}
		step, _ := drive(t, f, limits(), githubTarget())
		if step.Result.Verdict != gate.Green || step.State.Waits != 1 || step.State.WatchFailures != 3 {
			t.Fatalf("final %+v state %+v, want GREEN after 1 wait and 3 watch failures", step.Result, step.State)
		}
	})
	t.Run("GH-7_limits_come_from_config", func(t *testing.T) {
		cfg, err := config.Load("../../config/examples")
		if err != nil {
			t.Fatal(err)
		}
		alpha, ok := cfg.Project("alpha")
		if !ok {
			t.Fatal("example config has no project alpha")
		}
		if diff := cmp.Diff(limits(), alpha.Gate); diff != "" {
			t.Fatalf("schema defaults of the gate (-want +got):\n%s", diff)
		}
		lim := alpha.Gate
		lim.GitHub.MaxWaits, lim.GitHub.WatchTimeout = 2, config.Duration(5*time.Minute)
		step, armed := drive(t, answer(running), lim, githubTarget())
		if diff := cmp.Diff(repeat(2, "gate_wait+5m0s"), kinds(armed)); diff != "" || step.Result.Verdict != gate.Red {
			t.Errorf("final %+v, timers (-want +got):\n%s", step.Result, diff)
		}
	})
}

func TestGiteaPolls(t *testing.T) {
	t.Run("GT-4_no_status_becomes_noci_after_5_polls_at_60s", func(t *testing.T) {
		step, armed := drive(t, answer(pendingPR([]forge.Check{}...)), limits(), giteaTarget())
		want := gate.Result{Verdict: gate.NoCI, Reasons: []string{"no checks after 5 polls"}}
		if diff := cmp.Diff(want, step.Result); diff != "" || step.Err != nil {
			t.Fatalf("final (-want +got):\n%s err %v", diff, step.Err)
		}
		if diff := cmp.Diff(repeat(5, pollTimer), kinds(armed)); diff != "" {
			t.Errorf("timers (-want +got):\n%s", diff)
		}
	})
	t.Run("GT-4_running_checks_are_red_after_40_polls", func(t *testing.T) {
		pr := pendingPR(check("build", forge.CheckPending, 0))
		step, armed := drive(t, answer(pr), limits(), giteaTarget())
		want := redFor("still checks running after 40 polls")
		if diff := cmp.Diff(want, step.Result); diff != "" {
			t.Fatalf("final (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(repeat(40, pollTimer), kinds(armed)); diff != "" {
			t.Errorf("timers (-want +got):\n%s", diff)
		}
	})
	t.Run("GT-4_polled_until_settled", func(t *testing.T) {
		running := pendingPR(check("build", forge.CheckPending, 0))
		f := &fakeForge{reads: []read{{pr: running}, {pr: running}, {pr: openPR(check("build", forge.CheckSuccess, 3))}},
			files: []string{docFile}}
		step, armed := drive(t, f, limits(), giteaTarget())
		if step.Result.Verdict != gate.GreenDocs || len(armed) != 2 {
			t.Fatalf("final %+v after %v, want GREEN docs after 2 polls", step.Result, kinds(armed))
		}
	})
	t.Run("GT-5_conflict_ends_polling_at_once", func(t *testing.T) {
		pr := pendingPR(check("build", forge.CheckPending, 0))
		pr.Mergeable = forge.MergeableConflicted
		step, armed := drive(t, answer(pr), limits(), giteaTarget())
		if step.Result.Verdict != gate.Red || len(armed) != 0 {
			t.Fatalf("final %+v after %v, want RED with no poll", step.Result, kinds(armed))
		}
	})
}

func TestTurnArmFailure(t *testing.T) {
	w := gate.NewWatcher(answer(pendingPR([]forge.Check{}...)), clock.NewManual(start), limits(), project, taskID,
		githubTarget())
	errStore := errors.New("database is locked")
	step, err := w.Turn(context.Background(), &fakeTimers{err: errStore}, gate.State{})
	if !errors.Is(err, errStore) {
		t.Fatalf("Turn err = %v, want the store error", err)
	}
	if step.Final || step.Result.Verdict != "" {
		t.Errorf("a failed arm gave step %+v, want no verdict", step)
	}
}
