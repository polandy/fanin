package sched

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/store"
)

// fault makes every op (INSERT, UPDATE) on table abort, through a second connection to the store's file.
func (h *harness) fault(table, op string) {
	h.t.Helper()
	h.sql(fmt.Sprintf(`CREATE TRIGGER fault_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'injected fault'); END`,
		table, op, op, table))
}

func (h *harness) clearFault(table, op string) {
	h.t.Helper()
	h.sql(fmt.Sprintf(`DROP TRIGGER fault_%s_%s`, table, op))
}

func (h *harness) sql(stmt string) {
	h.t.Helper()
	db, err := sql.Open("sqlite", "file:"+h.path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := db.ExecContext(h.t.Context(), stmt); err != nil {
		h.t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) logged(line string) func() bool {
	return func() bool { return strings.Contains(h.logs.String(), line) }
}

const stepFailed = "step failed"

func TestStoreFaults(t *testing.T) {
	t.Run("WD-3_unwritable_timeout_never_ends_the_run_and_is_retried", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.fault("runs", "UPDATE")
		h.clk.Advance(workerTimeout)
		h.until(h.logged(stepFailed))
		if got := h.run(run.ID).State; got != model.RunWorking {
			t.Fatalf("run = %s, want working while the store refuses the write", got)
		}
		h.clearFault("runs", "UPDATE")
		h.clk.Advance(RetryAfter)
		h.until(h.runIn(run.ID, model.RunTimedOut))
	})

	t.Run("SP-11_unrecorded_start_leaves_the_status_unknown", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.fault("runs", "UPDATE")
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.logged("cannot record the started worker"))
		if got := h.run(RunID("a1", 1)).State; got != model.RunStarting {
			t.Errorf("run = %s, want starting", got)
		}
		if n := len(h.rt.observes()); n != 0 {
			t.Errorf("observes = %d, want none", n)
		}
	})

	t.Run("SP-8_unrecorded_failed_spawn_is_logged", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.startErr = runtime.ErrStartFailed
		h.fault("runs", "UPDATE")
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.logged("record the failed spawn"))
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want running", got)
		}
	})

	t.Run("unwritable_run_row_starts_no_worker", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.fault("runs", "INSERT")
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.logged(stepFailed))
		if got := h.task("a1").State; got != model.TaskQueued {
			t.Errorf("task = %s, want queued", got)
		}
		if n := len(h.rt.starts()); n != 0 {
			t.Errorf("starts = %d, want none", n)
		}
	})

	t.Run("PK-3_unwritable_idle_end_keeps_the_task_queued", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.pk.res = pick.Result{Idle: true, Reason: textNoTodo}
		h.start()
		h.addTask(model.Task{ID: "a1", Title: titleLoop})
		h.fault("tasks", "UPDATE")
		h.until(h.logged("end task a1"))
		if got := h.task("a1").State; got != model.TaskQueued {
			t.Errorf("task = %s, want queued", got)
		}
	})

	t.Run("PK-8_unwritable_events_keep_the_task_queued_and_start_no_worker", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.pk.res = pick.Result{Task: "x", Model: pick.ModelSonnet, Usage: headless.Usage{Input: 1}}
		h.start()
		h.addTask(model.Task{ID: "a1", Title: titleLoop})
		h.fault("events", "INSERT")
		h.until(h.logged(stepFailed))
		if got := h.task("a1").State; got != model.TaskQueued {
			t.Errorf("task = %s, want queued", got)
		}
		if n := len(h.rt.starts()); n != 0 {
			t.Errorf("starts = %d, want none", n)
		}
	})

	t.Run("unwritable_stall_move_is_a_failed_step", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.fault("runs", "UPDATE")
		n := h.s.applied.Load()
		h.rt.mu.Lock()
		feed := h.rt.feed(run.Worker.Agent)
		h.rt.mu.Unlock()
		go func() { feed <- runtime.RunEvent{Type: runtime.RunEventStalled, Reason: runtime.StallQuiet} }()
		h.until(func() bool { return h.s.applied.Load() > n && h.logged(stepFailed)() })
		if got := h.run(run.ID).State; got != model.RunWorking {
			t.Errorf("run = %s, want working", got)
		}
	})

	t.Run("unwritable_result_keeps_the_timers", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.fault("timers", "UPDATE")
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunResult(ctx, run.ID, model.Result{Kind: model.ResultNothing, Text: "none"})
		})
		h.until(h.logged("record the result"))
		if got := h.timers("a1"); len(got) != 2 {
			t.Errorf("timers = %v, want both still armed", got)
		}
	})

	t.Run("closed_store_is_a_failed_step", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.startOwner("a1")
		if err := h.st.Close(); err != nil {
			t.Fatal(err)
		}
		h.clk.Advance(stallAfter)
		h.until(h.logged(stepFailed))
		h.stop()
		st, err := store.Open(t.Context(), h.path, h.clk)
		if err != nil {
			t.Fatal(err)
		}
		h.st = st
	})

	for _, op := range []string{"INSERT", "UPDATE"} {
		t.Run("WD-1_unwritable_stall_timer_"+op+"_never_ends_the_run", func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.start()
			run := h.startOwner("a1")
			h.send(run.Worker.Agent, idle())
			h.fault("timers", op)
			h.clk.Advance(stallAfter)
			h.until(h.logged(stepFailed))
			if got := h.run(run.ID).State; got != model.RunWorking {
				t.Errorf("run = %s, want working", got)
			}
		})
	}

	t.Run("SP-10_unwritable_timers_at_resume_stop_the_loop", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.startOwner("a1")
		h.stop()
		h.addTask(model.Task{ID: "b1", Project: projBash, Title: "bash"})
		all, err := h.st.ArmedTimers(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx *store.Tx) error { return disarm(ctx, tx, all) })
		h.fault("timers", "INSERT")
		s, err := New(Deps{Config: h.cfg, Store: h.st, Runtime: newFakeRuntime(), Picker: h.pk, Files: h.files,
			Clock: h.clk, Log: slog.New(slog.NewTextHandler(h.logs, nil))})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "resume") {
			t.Errorf("Run = %v, want the resume error", err)
		}
	})
}
