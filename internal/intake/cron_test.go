package intake

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

const (
	cronName  = "nightly"
	cronEvery = 24 * time.Hour
)

var cronStart = time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)

// waitClock is a manual clock that reports every After call, so a test advances it only once Run waits.
type waitClock struct {
	*clock.Manual
	waits chan time.Duration
}

func (c *waitClock) After(d time.Duration) <-chan time.Time {
	ch := c.Manual.After(d)
	c.waits <- d
	return ch
}

func cronConfig() config.Config {
	cfg := testConfig()
	cfg.Triggers.Cron = map[string]config.Cron{
		cronName: {Project: projGH, Every: config.Duration(cronEvery), Task: "update the dependencies", Model: modelSonnet},
	}
	return cfg
}

func newCron(t *testing.T, cfg config.Config) (*Cron, *clock.Manual, *store.Store) {
	t.Helper()
	clk := clock.NewManual(cronStart)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &Cron{Config: cfg, Store: st, Intake: New(cfg, st), Clock: clk}, clk, st
}

func taskIDs(t *testing.T, st *store.Store) []model.TaskID {
	t.Helper()
	var ids []model.TaskID
	for _, tk := range openTasks(t, st) {
		ids = append(ids, tk.ID)
	}
	return ids
}

func armedDues(t *testing.T, st *store.Store) []time.Time {
	t.Helper()
	tms, err := st.ArmedTimers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var dues []time.Time
	for _, tm := range tms {
		dues = append(dues, tm.DueAt)
	}
	return dues
}

func cronID(due time.Time) model.TaskID {
	return CronTaskID(projGH, cronName, due)
}

func fire(t *testing.T, c *Cron) {
	t.Helper()
	if err := c.Fire(context.Background()); err != nil {
		t.Fatalf("Fire: %v", err)
	}
}

func TestCron(t *testing.T) {
	ctx := context.Background()

	t.Run("IN-4_once_per_due_across_two_dues", func(t *testing.T) {
		c, clk, st := newCron(t, cronConfig())
		if err := c.Arm(ctx); err != nil {
			t.Fatal(err)
		}
		fire(t, c)
		if ids := taskIDs(t, st); len(ids) != 0 {
			t.Fatalf("tasks before the first due: %v", ids)
		}
		first, second := cronStart.Add(cronEvery), cronStart.Add(2*cronEvery)
		clk.Advance(cronEvery)
		fire(t, c)
		fire(t, c)
		if d := cmp.Diff([]model.TaskID{cronID(first)}, taskIDs(t, st)); d != "" {
			t.Errorf("after the first due (-want +got):\n%s", d)
		}
		if d := cmp.Diff([]time.Time{second}, armedDues(t, st)); d != "" {
			t.Errorf("armed (-want +got):\n%s", d)
		}
		clk.Advance(cronEvery)
		fire(t, c)
		if d := cmp.Diff([]model.TaskID{cronID(first), cronID(second)}, taskIDs(t, st)); d != "" {
			t.Errorf("after the second due (-want +got):\n%s", d)
		}
		tk, err := st.Task(ctx, cronID(second))
		if err != nil || tk.Shape != model.ShapePR || tk.OwnerModel != modelSonnet || !tk.OwnerTask {
			t.Errorf("task = %+v, %v", tk, err)
		}
	})

	t.Run("IN-4_restart_rearms_nothing_twice", func(t *testing.T) {
		c, _, st := newCron(t, cronConfig())
		for range 2 {
			if err := c.Arm(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if got := armedDues(t, st); len(got) != 1 {
			t.Errorf("armed %v after two arms, want one timer", got)
		}
	})

	t.Run("IN-4_refire_after_a_crash_adds_nothing", func(t *testing.T) {
		c, clk, st := newCron(t, cronConfig())
		if err := c.Arm(ctx); err != nil {
			t.Fatal(err)
		}
		clk.Advance(cronEvery)
		due := cronStart.Add(cronEvery)
		if _, err := c.Intake.Enqueue(ctx, TaskRequest{ID: cronID(due), Project: projGH, Task: "x"}); err != nil {
			t.Fatal(err)
		}
		fire(t, c)
		if d := cmp.Diff([]model.TaskID{cronID(due)}, taskIDs(t, st)); d != "" {
			t.Errorf("tasks (-want +got):\n%s", d)
		}
		if d := cmp.Diff([]time.Time{due.Add(cronEvery)}, armedDues(t, st)); d != "" {
			t.Errorf("armed (-want +got):\n%s", d)
		}
	})

	t.Run("IN-4_missed_dues_fire_once", func(t *testing.T) {
		c, clk, st := newCron(t, cronConfig())
		if err := c.Arm(ctx); err != nil {
			t.Fatal(err)
		}
		clk.Advance(3*cronEvery + time.Hour)
		fire(t, c)
		if ids := taskIDs(t, st); len(ids) != 1 {
			t.Errorf("tasks after three missed dues: %v; want one", ids)
		}
		if d := cmp.Diff([]time.Time{cronStart.Add(4 * cronEvery)}, armedDues(t, st)); d != "" {
			t.Errorf("armed (-want +got):\n%s", d)
		}
	})

	t.Run("IN-4_removed_entry_is_disarmed", func(t *testing.T) {
		c, _, st := newCron(t, cronConfig())
		if err := c.Arm(ctx); err != nil {
			t.Fatal(err)
		}
		c.Config = testConfig()
		if err := c.Arm(ctx); err != nil {
			t.Fatal(err)
		}
		if got := armedDues(t, st); len(got) != 0 {
			t.Errorf("armed %v after the entry was removed, want none", got)
		}
	})

	t.Run("IN-4_run_fires_when_due", func(t *testing.T) {
		c, clk, st := newCron(t, cronConfig())
		wc := &waitClock{Manual: clk, waits: make(chan time.Duration, 1)}
		done := make(chan model.TaskID, 1)
		c.Clock = wc
		c.Intake = signalEnqueuer{Enqueuer: c.Intake, done: done}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		safego.Go(runCtx, slog.New(slog.DiscardHandler), c.Run)
		if d := <-wc.waits; d != cronEvery {
			t.Errorf("Run waits %s, want %s", d, cronEvery)
		}
		clk.Advance(cronEvery)
		if id := <-done; id != cronID(cronStart.Add(cronEvery)) {
			t.Errorf("enqueued %s", id)
		}
		<-wc.waits
		if ids := taskIDs(t, st); len(ids) != 1 {
			t.Errorf("tasks = %v; want one", ids)
		}
	})
}
