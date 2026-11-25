package sched

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const (
	projAlpha     = "alpha"
	projBash      = "beta"
	kindCC        = "claude"
	kindOC        = "opencode"
	kindHL        = "headless"
	stallAfter    = 10 * time.Minute
	workerTimeout = 60 * time.Minute
	titleLoop     = "loop"
	modelHaiku    = "haiku"
	modelOpus     = "opus"
	modelSonnet   = "sonnet"
	baseMain      = "main"
	pathAlpha     = "/r/alpha"
	textNoTodo    = "no todo"
	hostFar       = "far"
)

var epoch = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// The schema's default budgets.
var testBudgets = config.Budgets{FixRoundsPerRun: 2, RolloverPct: 75, MaxTokensPerRun: 3_000_000, MaxRunsPerTask: 3}

// testConfig is one fanin project alpha with two slots and one bash project beta.
func testConfig() config.Config {
	return config.Config{
		Forges: map[string]config.Forge{"gh": {Kind: config.ForgeGitHub}, "gt": {Kind: config.ForgeGitea}},
		WorkerKinds: map[string]config.WorkerKind{
			kindCC: {Runtime: config.RuntimeHerdr, Models: []string{modelOpus, modelSonnet, modelHaiku}, DefaultModel: modelSonnet},
			kindOC: {Runtime: config.RuntimeHerdr},
			kindHL: {Runtime: config.RuntimeHeadless},
		},
		Projects: []config.Project{
			{Name: projAlpha, Path: pathAlpha, Host: config.HostLocal, Forge: "gh", Base: baseMain, WorkerKind: kindCC,
				MaxParallel: 2, StallAfter: config.Duration(stallAfter), WorkerTimeout: config.Duration(workerTimeout),
				Loop: config.LoopFanin, Budgets: testBudgets},
			{Name: projBash, Path: "/r/beta", Host: config.HostLocal, Forge: "gh", Base: baseMain, WorkerKind: kindCC,
				MaxParallel: 1, StallAfter: config.Duration(stallAfter), WorkerTimeout: config.Duration(workerTimeout),
				Loop: config.LoopBash, Budgets: testBudgets},
		},
	}
}

// harness runs one scheduler on a real store with the manual clock; until steps it to a condition.
type harness struct {
	t       *testing.T
	cfg     config.Config
	path    string
	st      *store.Store
	clk     *clock.Manual
	rt      *fakeRuntime
	pk      *fakePicker
	up      *fakeUnpushed
	nt      *fakeNotifier
	gates   PRGates
	files   RunFiles
	logs    *syncBuffer
	s       *Scheduler
	cancel  context.CancelFunc
	waiting chan struct{}
	done    chan error
	// cursor is the loop's event cursor at its latest wait.
	cursor atomic.Int64
}

func newHarness(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	clk := clock.NewManual(epoch)
	path := filepath.Join(t.TempDir(), "fanin.db")
	st, err := store.Open(t.Context(), path, clk)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, cfg: cfg, path: path, st: st, clk: clk, rt: newFakeRuntime(), pk: &fakePicker{},
		up: &fakeUnpushed{}, nt: &fakeNotifier{}, files: Files{Dir: t.TempDir(), DaemonURL: "http://127.0.0.1:1"}, logs: &syncBuffer{}}
	t.Cleanup(func() {
		h.stop()
		if err := h.st.Close(); err != nil {
			t.Error(err)
		}
	})
	return h
}

// start runs a fresh scheduler and returns once its first step is done.
func (h *harness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(h.t.Context())
	h.cancel, h.waiting, h.done = cancel, make(chan struct{}), make(chan error, 1)
	s, err := New(Deps{Config: h.cfg, Store: h.st, Runtime: h.rt, Picker: h.pk, Files: h.files, Clock: h.clk,
		Unpushed: h.up, Notify: h.nt, Gates: h.gates, Log: slog.New(slog.NewTextHandler(h.logs, nil)), Waiting: func() {
			h.cursor.Store(h.s.cursor)
			select {
			case h.waiting <- struct{}{}:
			case <-ctx.Done():
			}
		}})
	if err != nil {
		h.t.Fatal(err)
	}
	h.s = s
	go func() { h.done <- h.s.Run(ctx) }()
	h.settle()
}

// stop ends the scheduler by ctx and checks that Run returned nil.
func (h *harness) stop() {
	h.t.Helper()
	if h.cancel == nil {
		return
	}
	h.cancel()
	if err := <-h.done; err != nil {
		h.t.Errorf("Run after cancel = %v, want nil", err)
	}
	h.cancel = nil
}

// settle waits until the loop is about to block once more.
func (h *harness) settle() {
	h.t.Helper()
	select {
	case <-h.waiting:
	case err := <-h.done:
		h.t.Fatalf("Run returned early: %v", err)
	}
}

// until lets the loop step until cond holds.
func (h *harness) until(cond func() bool) {
	h.t.Helper()
	for !cond() {
		h.settle()
	}
}

// send hands ev to the agent's observer and waits until the loop applied it.
func (h *harness) send(agent string, ev runtime.RunEvent) {
	h.t.Helper()
	n := h.s.applied.Load()
	h.rt.mu.Lock()
	feed := h.rt.feed(agent)
	h.rt.mu.Unlock()
	go func() { feed <- ev }()
	h.until(func() bool { return h.s.applied.Load() > n })
	h.quiet()
}

// closeFeed ends the agent's event stream and waits until the loop saw the end.
func (h *harness) closeFeed(agent string) {
	h.t.Helper()
	n := h.s.applied.Load()
	h.rt.mu.Lock()
	close(h.rt.feed(agent))
	h.rt.mu.Unlock()
	h.until(func() bool { return h.s.applied.Load() > n })
	h.quiet()
}

// restart stops the scheduler, reopens the store and starts a fresh scheduler with a fresh runtime.
func (h *harness) restart() {
	h.t.Helper()
	h.stop()
	h.restartWith(func(*fakeRuntime) {})
}

// startOwner adds an owner task and waits until its first run works.
func (h *harness) startOwner(id model.TaskID) model.Run {
	h.t.Helper()
	h.addTask(ownerTask(id))
	h.until(h.runIn(RunID(id, 1), model.RunWorking))
	return h.run(RunID(id, 1))
}

// advance moves the clock and waits for a full step at the new time.
func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	h.clk.Advance(d)
	h.quiet()
}

// quiet appends a marker event and waits until a step read it: every step before it is done, the positive
// signal for an absence assertion.
func (h *harness) quiet() {
	h.t.Helper()
	var seq int64
	h.tx(func(ctx context.Context, tx *store.Tx) error {
		var err error
		seq, err = tx.AppendEvent(ctx, model.Event{Type: model.EventNotified, Project: projAlpha, Detail: "marker"})
		return err
	})
	h.until(func() bool { return h.cursor.Load() >= seq })
}

func (h *harness) tx(fn func(ctx context.Context, tx *store.Tx) error) {
	h.t.Helper()
	if err := h.st.Transition(h.t.Context(), fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) addTask(task model.Task) {
	h.t.Helper()
	if task.Project == "" {
		task.Project = projAlpha
	}
	if task.Shape == "" {
		task.Shape = model.ShapePR
	}
	h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.CreateTask(ctx, task) })
}

func (h *harness) task(id model.TaskID) model.Task {
	h.t.Helper()
	task, err := h.st.Task(h.t.Context(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return task
}

func (h *harness) runs(id model.TaskID) []model.Run {
	h.t.Helper()
	runs, err := h.st.Runs(h.t.Context(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return runs
}

func (h *harness) run(id model.RunID) model.Run {
	h.t.Helper()
	run, err := h.st.Run(h.t.Context(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return run
}

func (h *harness) timers(id model.TaskID) map[string]time.Time {
	h.t.Helper()
	all, err := h.st.ArmedTimers(h.t.Context())
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]time.Time{}
	for _, tm := range all {
		if tm.TaskID == id {
			out[tm.Kind] = tm.DueAt
		}
	}
	return out
}

func (h *harness) runIn(id model.RunID, states ...model.RunState) func() bool {
	return func() bool {
		run, err := h.st.Run(h.t.Context(), id)
		if err != nil {
			return false
		}
		for _, s := range states {
			if run.State == s {
				return true
			}
		}
		return false
	}
}

func (h *harness) taskIn(id model.TaskID, state model.TaskState) func() bool {
	return func() bool { return h.task(id).State == state }
}
