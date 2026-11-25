package sched

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/hook"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const (
	hookCmd    = "/srv/alpha/deploy.sh"
	stateDir   = "/var/lib/fanin"
	hookLast   = "deploy: host unreachable"
	lockWait   = time.Hour
	lockLease  = 10 * time.Minute
	prSecond   = 43
	prThird    = 44
	checkBuild = "build"
)

var (
	errMerge  = errors.New("merge refused: base moved")
	errBaseCI = errors.New("forge down")
	errPrompt = errors.New("pane closed")
)

// mergeLog orders the merges and hook runs of one test.
type mergeLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *mergeLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *mergeLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// fakeMerger logs each merge and fails it with err.
type fakeMerger struct {
	log *mergeLog
	mu  sync.Mutex
	err error
}

func (f *fakeMerger) Merge(_ context.Context, _ config.Project, pr int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log.add("merge #" + strconv.Itoa(pr))
	return f.err
}

// fakeHookRunner logs each hook run and fails it with err; while hold is set, a run signals on in and waits for
// hold to close or its ctx to end.
type fakeHookRunner struct {
	log  *mergeLog
	mu   sync.Mutex
	cmds []runtime.Cmd
	err  error
	hold chan struct{}
	in   chan struct{}
}

func (f *fakeHookRunner) Run(ctx context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd)
	err, hold := f.err, f.hold
	f.mu.Unlock()
	f.log.add("hook #" + strings.TrimPrefix(cmd.Env[len(cmd.Env)-1], hook.EnvPR+"="))
	if hold != nil {
		f.in <- struct{}{}
		select {
		case <-hold:
		case <-ctx.Done():
			return runtime.Result{}, ctx.Err()
		}
	}
	if err != nil {
		return runtime.Result{}, &runtime.ExitError{Argv: cmd.Argv, Code: 1, StderrTail: "step 1\n" + hookLast + "\n"}
	}
	return runtime.Result{}, nil
}

func (f *fakeHookRunner) calls() []runtime.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtime.Cmd(nil), f.cmds...)
}

func (f *fakeHookRunner) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// fakeBaseCI answers from a queue of check sets whose last answer repeats, or fails with err.
type fakeBaseCI struct {
	mu      sync.Mutex
	answers [][]forge.Check
	err     error
	reads   int
}

func (f *fakeBaseCI) BaseChecks(context.Context, config.Project) ([]forge.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	i := min(f.reads, len(f.answers)-1)
	f.reads++
	if i < 0 {
		return nil, nil
	}
	return f.answers[i], nil
}

func redBase() []forge.Check {
	return []forge.Check{{Name: "ci", State: forge.CheckSuccess}, {Name: checkBuild, State: forge.CheckFailure}}
}

func greenBase() []forge.Check { return []forge.Check{{Name: checkBuild, State: forge.CheckSuccess}} }

// mergeConfig is testConfig with alpha merging under a hook and four slots.
func mergeConfig() config.Config {
	cfg := testConfig()
	p := &cfg.Projects[0]
	p.MaxParallel = 4
	p.PostMergeHook = hookCmd
	p.Merge = config.Merge{LockWait: config.Duration(lockWait), LockLease: config.Duration(lockLease)}
	return cfg
}

// mergeRig is a harness with a merger, a real hook on a fake runner and a green forge.
type mergeRig struct {
	*harness
	log   *mergeLog
	m     *fakeMerger
	hr    *fakeHookRunner
	f     *fakeForge
	waits chan struct{}
	waitN atomic.Int32
}

func newMergeRig(t *testing.T, cfg config.Config) *mergeRig {
	t.Helper()
	log := &mergeLog{}
	r := &mergeRig{harness: newHarness(t, cfg), log: log, m: &fakeMerger{log: log},
		hr: &fakeHookRunner{log: log, in: make(chan struct{}, 4)}, f: &fakeForge{prs: []forge.PR{greenPR()}},
		waits: make(chan struct{}, 1)}
	r.gates = &fakeGates{forge: r.f}
	r.merger, r.hookRun, r.stateDir = r.m, r.hr, stateDir
	r.st.OnLockWait(func() {
		r.waitN.Add(1)
		select {
		case r.waits <- struct{}{}:
		default:
		}
	})
	return r
}

// await lets the loop step until ch signals.
func (r *mergeRig) await(ch <-chan struct{}) {
	r.t.Helper()
	for {
		select {
		case <-ch:
			return
		case <-r.waiting:
		case err := <-r.done:
			r.t.Fatalf("Run returned early: %v", err)
		}
	}
}

// blockHook makes every hook run wait until the returned release is called.
func (r *mergeRig) blockHook() (release func()) {
	hold := make(chan struct{})
	r.hr.mu.Lock()
	r.hr.hold = hold
	r.hr.mu.Unlock()
	return func() { close(hold) }
}

// ready starts an analysis that reports pr and waits for its green verdict.
func (r *mergeRig) ready(id model.TaskID, pr int) model.Run {
	r.t.Helper()
	run := r.startAnalysis(id)
	r.report(run, model.Result{Kind: model.ResultDonePR, PR: pr})
	r.until(r.hasEvent(id, model.EventGateVerdict))
	return run
}

// readyOwnerPR moves the running owner PR task id to gating with pr reported, as a gated PR task.
func (r *mergeRig) readyOwnerPR(id model.TaskID, pr int) model.Run {
	r.t.Helper()
	r.until(r.runIn(RunID(id, 1), model.RunWorking))
	run := r.run(RunID(id, 1))
	r.report(run, model.Result{Kind: model.ResultDonePR, PR: pr})
	r.until(r.runIn(run.ID, model.RunReported))
	r.tx(func(ctx context.Context, tx *store.Tx) error {
		return tx.SetTaskState(ctx, id, model.TaskGating, GatingDetail+"#"+strconv.Itoa(pr))
	})
	return run
}

func (r *mergeRig) owner() *owner.Service {
	return owner.New(r.st, owner.NoDeliverer{}, slog.New(slog.DiscardHandler))
}

// goMerge gives the owner's go for id.
func (r *mergeRig) goMerge(id model.TaskID) {
	r.t.Helper()
	if err := r.owner().Go(r.t.Context(), id); err != nil {
		r.t.Fatal(err)
	}
}

func (r *mergeRig) holds() []model.Hold {
	r.t.Helper()
	hs, err := r.st.Holds(r.t.Context(), projAlpha)
	if err != nil {
		r.t.Fatal(err)
	}
	return hs
}

func TestMerge(t *testing.T) {
	t.Run("MG-1_one_merge_runs_the_hook_once_with_the_env_without_re-gate_or_wait", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.start()
		run := r.ready("a1", prNumber)
		reads := r.f.prReads()
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{"merge #42", "hook #42"}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
		want := []runtime.Cmd{{Argv: []string{hook.Shell, hook.ShellFlag, hookCmd}, Dir: stateDir,
			Env: []string{hook.EnvProject + "=" + projAlpha, hook.EnvPR + "=42"}}}
		if diff := cmp.Diff(want, r.hr.calls()); diff != "" {
			t.Errorf("hook runs (-want +got):\n%s", diff)
		}
		if got := r.f.prReads(); got != reads {
			t.Errorf("PR reads = %d, want %d: no re-gate without a wait", got, reads)
		}
		if n := r.waitN.Load(); n != 0 {
			t.Errorf("lock waits = %d, want none", n)
		}
		if got := r.lastDetail("a1"); got != MergedDetail+"#42" {
			t.Errorf("task detail = %q, want %q", got, MergedDetail+"#42")
		}
		if got := r.run(run.ID).State; got != model.RunSucceeded {
			t.Errorf("run state = %s, want %s", got, model.RunSucceeded)
		}
		r.until(r.stopped(run.Worker.Agent))
		if hs := r.holds(); len(hs) != 0 {
			t.Errorf("holds = %+v, want none", hs)
		}
		if locks, err := r.st.Locks(t.Context()); err != nil || len(locks) != 0 {
			t.Errorf("locks = %+v %v, want the lock released", locks, err)
		}
	})

	t.Run("MG-1_no_merger_merges_nothing_on_the_go", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.merger = nil
		r.start()
		r.ready("a1", prNumber)
		r.goMerge("a1")
		r.quiet()
		if got := r.log.all(); len(got) != 0 {
			t.Errorf("merges = %v, want none", got)
		}
	})

	t.Run("MG-2_a_taken_lock_waits_then_re-gates_after_the_other_hook", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		release := r.blockHook()
		r.start()
		r.ready("a1", prNumber)
		r.ready("a2", prSecond)
		r.goMerge("a1")
		r.await(r.hr.in)
		r.goMerge("a2")
		r.await(r.waits)
		reads := r.f.prReads()
		if diff := cmp.Diff([]string{"merge #42", "hook #42"}, r.log.all()); diff != "" {
			t.Errorf("merges while #42's hook runs (-want +got):\n%s", diff)
		}
		release()
		r.until(r.taskIn("a2", model.TaskDone))
		if diff := cmp.Diff([]string{"merge #42", "hook #42", "merge #43", "hook #43"}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
		if got := r.f.prReads(); got != reads+1 {
			t.Errorf("PR reads = %d, want %d: one re-gate after the wait", got, reads+1)
		}
	})

	t.Run("MG-2_a_red_re-gate_nudges_instead_of_merging_and_the_next_green_merges", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.f.prs = []forge.PR{greenPR(), greenPR(), redPR(), greenPR()}
		release := r.blockHook()
		r.start()
		r.ready("a1", prNumber)
		run := r.ready("a2", prSecond)
		r.goMerge("a1")
		r.await(r.hr.in)
		r.goMerge("a2")
		r.await(r.waits)
		release()
		r.until(r.taskIn("a2", model.TaskRunning))
		if got := r.lastDetail("a2"); !strings.HasPrefix(got, RegateDetail+"#43 RED") {
			t.Errorf("task detail = %q, want %s#43 RED", got, RegateDetail)
		}
		if got := r.run(run.ID).State; got != model.RunWorking {
			t.Errorf("run state = %s, want %s", got, model.RunWorking)
		}
		r.until(func() bool { return len(r.rt.sent(run.Worker.Agent)) > 0 })
		if sent := r.rt.sent(run.Worker.Agent); !strings.Contains(sent[len(sent)-1], "#43 RED") {
			t.Errorf("nudge = %q, want the re-gate's reasons", sent[len(sent)-1])
		}
		if diff := cmp.Diff([]string{string(prompt.NudgeRegate)}, r.events("a2", model.EventWorkerNudged)); diff != "" {
			t.Errorf("nudges (-want +got):\n%s", diff)
		}
		if got := r.log.all(); slices.Contains(got, "merge #43") {
			t.Fatalf("merges = %v, want no merge of a red #43", got)
		}
		r.report(run, model.Result{Kind: model.ResultDonePR, PR: prSecond})
		r.until(r.taskIn("a2", model.TaskDone))
		if got := r.log.all(); !slices.Contains(got, "merge #43") {
			t.Errorf("merges = %v, want #43 merged on the standing go", got)
		}
	})

	t.Run("MG-2_a_lock_held_past_the_wait_goes_to_the_owner_naming_holder_and_duration", func(t *testing.T) {
		cfg := mergeConfig()
		cfg.Projects[0].Merge.LockLease = config.Duration(2 * lockWait)
		r := newMergeRig(t, cfg)
		r.blockHook()
		r.start()
		r.ready("a1", prNumber)
		r.ready("a2", prSecond)
		r.goMerge("a1")
		r.await(r.hr.in)
		r.goMerge("a2")
		r.await(r.waits)
		r.advance(lockWait)
		r.until(r.taskIn("a2", model.TaskNeedsOwner))
		got := r.lastDetail("a2")
		for _, part := range []string{MergeErrorDetail + "#43", "a1", lockWait.String()} {
			if !strings.Contains(got, part) {
				t.Errorf("task detail = %q, want it to name %q", got, part)
			}
		}
		if got := r.log.all(); slices.Contains(got, "merge #43") {
			t.Errorf("merges = %v, want no merge of #43", got)
		}
	})

	t.Run("MG-3_an_expired_lease_frees_the_lock", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.start()
		r.ready("a1", prNumber)
		if err := r.st.AcquireLock(t.Context(), store.LockRequest{Project: projAlpha, Holder: "gone",
			Lease: lockLease}); err != nil {
			t.Fatal(err)
		}
		r.goMerge("a1")
		r.await(r.waits)
		r.advance(lockLease)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{"merge #42", "hook #42"}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})

	t.Run("MG-4_a_failed_hook_counts_the_PR_merged_and_holds_every_other_merge_until_lifted", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.hr.set(errMerge)
		r.start()
		r.ready("a1", prNumber)
		r.ready("a2", prSecond)
		r.ready("a3", prThird)
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskDone))
		hs := r.holds()
		if len(hs) != 1 || hs[0].Kind != model.HoldMerge || hs[0].Reason != hook.FailedPrefix+hookLast {
			t.Fatalf("holds = %+v, want one merge hold %q", hs, hook.FailedPrefix+hookLast)
		}
		if m := r.events("a1", model.EventMerged); len(m) != 1 {
			t.Errorf("merged events = %v, want #42 merged", m)
		}
		got := r.lastDetail("a1")
		for _, part := range []string{MergeErrorDetail + "#42", hookLast, "fanin hold lift " + hs[0].ID} {
			if !strings.Contains(got, part) {
				t.Errorf("task detail = %q, want it to name %q", got, part)
			}
		}
		r.until(func() bool { return len(r.nt.sent()) > 0 })
		if n := r.nt.sent()[0]; n.TaskID != "a1" || n.Text != got {
			t.Errorf("note = %+v, want the hook failure of a1", n)
		}
		r.goMerge("a2")
		r.goMerge("a3")
		r.until(r.hasEvent("a2", model.EventMergeHeld))
		r.until(r.hasEvent("a3", model.EventMergeHeld))
		held := r.events("a2", model.EventMergeHeld)[0]
		if !strings.HasPrefix(held, PausedDetail+"#43"+MergeHeldNote) || !strings.Contains(held, hs[0].Reason) {
			t.Errorf("held = %q, want paused #43 merge held with the reason", held)
		}
		if diff := cmp.Diff([]string{"merge #42", "hook #42"}, r.log.all()); diff != "" {
			t.Errorf("merges while held (-want +got):\n%s", diff)
		}
		r.hr.set(nil)
		if err := r.owner().LiftHold(t.Context(), hs[0].ID); err != nil {
			t.Fatal(err)
		}
		r.until(r.taskIn("a2", model.TaskDone))
		r.until(r.taskIn("a3", model.TaskDone))
		if hs := r.holds(); len(hs) != 0 {
			t.Errorf("holds after the lift = %+v, want none", hs)
		}
	})

	t.Run("MG-5_a_holder_that_dies_inside_the_hook_keeps_the_hold_and_the_others_pause", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.blockHook()
		r.start()
		r.ready("a1", prNumber)
		r.ready("a2", prSecond)
		r.goMerge("a1")
		r.await(r.hr.in)
		r.restart()
		r.st.OnLockWait(func() { r.waits <- struct{}{} })
		hs := r.holds()
		if len(hs) != 1 || hs[0].Reason != hook.RunningReason(prNumber) {
			t.Fatalf("holds = %+v, want the running hold kept", hs)
		}
		r.goMerge("a2")
		r.await(r.waits)
		r.advance(lockLease)
		r.until(r.hasEvent("a2", model.EventMergeHeld))
		if got := r.log.all(); slices.Contains(got, "merge #43") {
			t.Errorf("merges = %v, want #43 paused", got)
		}
	})

	t.Run("LN-6_two_tasks_of_a_project_never_merge_at_once", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.start()
		r.ready("a1", prNumber)
		r.ready("a2", prSecond)
		r.goMerge("a1")
		r.goMerge("a2")
		r.until(r.taskIn("a1", model.TaskDone))
		r.until(r.taskIn("a2", model.TaskDone))
		got := r.log.all()
		if len(got) != 4 {
			t.Fatalf("merges = %v, want two merges with their hooks", got)
		}
		for i := 0; i < len(got); i += 2 {
			pr := strings.TrimPrefix(got[i], "merge ")
			if got[i+1] != "hook "+pr {
				t.Errorf("merges = %v, want each hook right after its merge", got)
			}
		}
	})
}

func TestMergeCIHold(t *testing.T) {
	// redHold has a1's go find the base red: a ci hold with a fix task, which starts.
	redHold := func(t *testing.T, r *mergeRig) model.Hold {
		t.Helper()
		r.goMerge("a1")
		r.until(r.hasEvent("a1", model.EventMergeHeld))
		hs := r.holds()
		if len(hs) != 1 || hs[0].Kind != model.HoldCI || hs[0].FixTask == "" {
			t.Fatalf("holds = %+v, want one ci hold with a fix task", hs)
		}
		fix := r.task(hs[0].FixTask)
		if !fix.OwnerTask || fix.Title != CIFixTitle+baseMain+": "+checkBuild {
			t.Errorf("fix task = %+v, want an owner task %q", fix, CIFixTitle+baseMain+": "+checkBuild)
		}
		r.until(r.runIn(RunID(fix.ID, 1), model.RunWorking))
		return hs[0]
	}

	t.Run("MG-8_a_red_base_holds_merges_and_fresh_workers_but_lets_the_fix_task_start", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.baseCI = &fakeBaseCI{answers: [][]forge.Check{redBase()}}
		r.start()
		r.ready("a1", prNumber)
		redHold(t, r)
		r.addTask(ownerTask("q1"))
		r.quiet()
		if runs := r.runs("q1"); len(runs) != 0 {
			t.Errorf("runs of q1 = %+v, want no fresh worker during the ci hold", runs)
		}
		if got := r.log.all(); len(got) != 0 {
			t.Errorf("merges = %v, want none", got)
		}
	})

	t.Run("MG-8_a_ci_hold_set_during_the_pick_starts_no_worker", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		holdErr := make(chan error, 1)
		r.pk.during = func() {
			holdErr <- r.st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
				fix := ownerTask("f1")
				fix.Project, fix.Shape = projAlpha, model.ShapePR
				if err := tx.CreateTask(ctx, fix); err != nil {
					return err
				}
				_, err := tx.SetHold(ctx, model.Hold{Project: projAlpha, Kind: model.HoldCI, Reason: "red " + baseMain,
					FixTask: "f1"})
				return err
			})
		}
		r.start()
		r.addTask(model.Task{ID: "t1", Title: "loop task", Prompt: "do t1"})
		if err := <-holdErr; err != nil {
			t.Fatal(err)
		}
		r.until(r.runIn(RunID("f1", 1), model.RunWorking))
		r.quiet()
		if runs := r.runs("t1"); len(runs) != 0 {
			t.Errorf("runs of t1 = %+v, want no worker after the hold", runs)
		}
		if got := r.task("t1").State; got != model.TaskQueued {
			t.Errorf("task state = %s, want %s", got, model.TaskQueued)
		}
		if !strings.Contains(r.logs.String(), CIHeldDetail) {
			t.Errorf("logs lack %q", CIHeldDetail)
		}
	})

	t.Run("MG-8_the_fix_merge_lifts_the_hold_and_nudges_the_others_naming_a_failed_nudge", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.baseCI = &fakeBaseCI{answers: [][]forge.Check{redBase(), greenBase()}}
		r.start()
		w1 := r.startOwner("w1")
		w2 := r.startOwner("w2")
		r.rt.mu.Lock()
		r.rt.promptErrs = map[string]error{w2.Worker.Agent: errPrompt}
		r.rt.mu.Unlock()
		r.ready("a1", prNumber)
		h := redHold(t, r)
		r.readyOwnerPR(h.FixTask, prThird)
		r.goMerge(h.FixTask)
		r.until(r.taskIn(h.FixTask, model.TaskDone))
		got := r.lastDetail(h.FixTask)
		want := MergedDetail + "#44" + liftNote + h.ID + " lifted, nudged " + w1.Worker.Agent + "; " +
			NudgeFailedDetail + w2.Worker.Agent + " failed: " + errPrompt.Error()
		if got != want {
			t.Errorf("fix detail = %q, want %q", got, want)
		}
		if diff := cmp.Diff([]string{string(prompt.NudgeCIFixed)}, r.events("w1", model.EventWorkerNudged)); diff != "" {
			t.Errorf("nudges of w1 (-want +got):\n%s", diff)
		}
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{"merge #44", "hook #44", "merge #42", "hook #42"}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
		if hs := r.holds(); len(hs) != 0 {
			t.Errorf("holds = %+v, want none", hs)
		}
	})

	t.Run("MG-8_a_fix_task_without_a_hold_merges_normally", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.baseCI = &fakeBaseCI{answers: [][]forge.Check{redBase(), greenBase()}}
		r.start()
		r.ready("a1", prNumber)
		h := redHold(t, r)
		if err := r.owner().LiftHold(t.Context(), h.ID); err != nil {
			t.Fatal(err)
		}
		r.until(r.taskIn("a1", model.TaskDone))
		r.readyOwnerPR(h.FixTask, prThird)
		r.goMerge(h.FixTask)
		r.until(r.taskIn(h.FixTask, model.TaskDone))
		if got := r.lastDetail(h.FixTask); got != MergedDetail+"#44" {
			t.Errorf("fix detail = %q, want %q", got, MergedDetail+"#44")
		}
	})
}

func TestMergeFails(t *testing.T) {
	t.Run("MG-1_a_failed_merge_goes_to_the_owner_and_runs_no_hook", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.m.err = errMerge
		r.start()
		r.ready("a1", prNumber)
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.Contains(got, errMerge.Error()) {
			t.Errorf("task detail = %q, want the cause", got)
		}
		if c := r.hr.calls(); len(c) != 0 {
			t.Errorf("hook runs = %v, want none", c)
		}
	})

	t.Run("MG-2_an_unreadable_re-gate_goes_to_the_owner", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.f.errs = []error{nil, forge.ErrNetwork}
		r.start()
		r.ready("a1", prNumber)
		if err := r.st.AcquireLock(t.Context(), store.LockRequest{Project: projAlpha, Holder: "gone",
			Lease: lockLease}); err != nil {
			t.Fatal(err)
		}
		r.goMerge("a1")
		r.await(r.waits)
		r.advance(lockLease)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.Contains(got, "re-gate unreadable") {
			t.Errorf("task detail = %q, want the cause", got)
		}
		if got := r.log.all(); len(got) != 0 {
			t.Errorf("merges = %v, want none", got)
		}
	})

	t.Run("MG-8_an_unreadable_base_CI_goes_to_the_owner", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.baseCI = &fakeBaseCI{err: errBaseCI}
		r.start()
		r.ready("a1", prNumber)
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.Contains(got, errBaseCI.Error()) {
			t.Errorf("task detail = %q, want the cause", got)
		}
		if got := r.log.all(); len(got) != 0 {
			t.Errorf("merges = %v, want none", got)
		}
	})

	t.Run("MG-1_a_merger_without_hook_or_state_dir_is_refused", func(t *testing.T) {
		_, err := New(Deps{Config: mergeConfig(), Merger: &fakeMerger{log: &mergeLog{}}})
		if !errors.Is(err, ErrMergeDeps) {
			t.Errorf("New = %v, want ErrMergeDeps", err)
		}
	})
}
