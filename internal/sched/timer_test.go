package sched

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

func idle() runtime.RunEvent { return runtime.RunEvent{Type: runtime.RunEventIdle} }

// alive checks that the run was not ended and that its timeout looks again after now.
func (h *harness) alive(run model.Run) {
	h.t.Helper()
	if got := h.run(run.ID).State; got.Terminal() {
		h.t.Fatalf("run %s = %s, want it alive", run.ID, got)
	}
	if due, ok := h.timers(run.TaskID)[TimerTimeout]; !ok || !due.After(h.clk.Now()) {
		h.t.Fatalf("timeout timer of %s = %v (armed %t), want one after %v", run.TaskID, due, ok, h.clk.Now())
	}
}

func TestTimeout(t *testing.T) {
	t.Run("WD-1_no_heartbeat_or_result_for_worker_timeout_ends_the_run", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.advance(stallAfter)
		if got := h.run(run.ID).State; got != model.RunStalled {
			t.Fatalf("run after %v = %s, want stalled", stallAfter, got)
		}
		h.advance(workerTimeout - stallAfter)
		if got := h.run(run.ID).State; got != model.RunTimedOut {
			t.Fatalf("run after %v = %s, want timed_out", workerTimeout, got)
		}
		if got := h.task("a1").State; got != model.TaskFailed {
			t.Errorf("task = %s, want failed", got)
		}
		if got := h.events("a1", model.EventRunState); !strings.HasPrefix(got[len(got)-1], TimeoutDetail) {
			t.Errorf("run detail = %q, want the %q detail", got[len(got)-1], TimeoutDetail)
		}
		if got := h.timers("a1"); len(got) != 0 {
			t.Errorf("timers = %v, want none", got)
		}
		if !strings.Contains(h.logs.String(), "timed out") {
			t.Errorf("logs lack the timeout line:\n%s", h.logs)
		}
	})

	t.Run("WD-1_heartbeat_moves_the_timeout", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.advance(stallAfter)
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventProgress, Project: projAlpha, TaskID: "a1",
				RunID: run.ID, Detail: "tests green"})
			return err
		})
		h.quiet()
		if got := h.run(run.ID).State; got != model.RunWorking {
			t.Fatalf("run after progress = %s, want working", got)
		}
		h.advance(workerTimeout - stallAfter)
		h.alive(run)
		if due := h.timers("a1")[TimerTimeout]; !due.Equal(epoch.Add(stallAfter + workerTimeout)) {
			t.Errorf("timeout due %v, want one timeout after the progress", due)
		}
	})

	t.Run("WD-1_result_disarms_the_timers", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventStalled, Reason: runtime.StallQuiet})
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunResult(ctx, run.ID, model.Result{Kind: model.ResultNothing, Text: "no todo"})
		})
		h.until(h.runIn(run.ID, model.RunReported))
		h.quiet()
		if got := h.timers("a1"); len(got) != 0 {
			t.Errorf("timers = %v, want none after the result", got)
		}
		h.advance(2 * workerTimeout)
		if got := h.run(run.ID).State; got != model.RunReported {
			t.Errorf("run = %s, want reported", got)
		}
	})
}

func TestTimeoutSpared(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness, run model.Run)
	}{
		{"WD-2_working_worker", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		}},
		{"WD-2_worker_blocked_on_a_dialog", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventDialog, Dialog: "trust?"})
		}},
		{"WD-2_held_merge_lock", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, idle())
			err := h.st.AcquireLock(h.t.Context(), store.LockRequest{Project: projAlpha, Holder: string(run.TaskID),
				Lease: 3 * workerTimeout})
			if err != nil {
				h.t.Fatal(err)
			}
		}},
		{"WD-3_FP-6_unreachable_runtime", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventUnreachable, Err: runtime.ErrUnreachable})
		}},
		{"WD-3_FP-6_event_stream_ended_without_gone", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, idle())
			h.closeFeed(run.Worker.Agent)
		}},
		{"WD-3_FP-6_no_event_yet", func(*harness, model.Run) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.start()
			run := h.startOwner("a1")
			tc.setup(h, run)
			h.advance(workerTimeout)
			h.alive(run)
			if !strings.Contains(h.logs.String(), "worker timeout spared") {
				t.Errorf("logs lack the spared line:\n%s", h.logs)
			}
		})
	}
}

func TestTimeoutSparesTheBackgroundFooter(t *testing.T) {
	t.Run("WD-2_own_bg_footer_up_to_180_min", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventStalled, Reason: runtime.StallBGShell})
		h.advance(workerTimeout)
		h.alive(run)
		h.advance(BGFooterMax - workerTimeout - time.Minute)
		h.alive(run)
		h.advance(time.Minute)
		if got := h.run(run.ID).State; got != model.RunTimedOut {
			t.Errorf("run after %v of footer = %s, want timed_out", BGFooterMax, got)
		}
	})

	t.Run("WD-2_footer_gone_ends_the_spare", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventStalled, Reason: runtime.StallBGShell})
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventStalled, Reason: runtime.StallQuiet})
		h.advance(workerTimeout)
		if got := h.run(run.ID).State; got != model.RunTimedOut {
			t.Errorf("run = %s, want timed_out once the footer is gone", got)
		}
	})
}

func TestSpawnFailed(t *testing.T) {
	t.Run("SP-8_failed_spawn_is_a_run_row_and_the_next_number_skips_it", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.startErr = errors.New("herdr: no pane")
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.runIn(RunID("a1", 1), model.RunFailed))
		if got := h.task("a1").State; got != model.TaskFailed {
			t.Errorf("task = %s, want failed", got)
		}
		want := SpawnFailedDetail + AgentName("a1", 1)
		if got := h.events("a1", model.EventRunState); !strings.HasPrefix(got[len(got)-1], want) {
			t.Errorf("run detail = %q, want it to start with %q", got[len(got)-1], want)
		}
		if !strings.Contains(h.logs.String(), "spawn failed") {
			t.Errorf("logs lack the spawn failed line:\n%s", h.logs)
		}
		h.rt.mu.Lock()
		h.rt.startErr = nil
		h.rt.mu.Unlock()
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			return tx.SetTaskState(ctx, "a1", model.TaskQueued, "retry")
		})
		h.until(h.runIn(RunID("a1", 2), model.RunWorking))
		if got := h.run(RunID("a1", 2)); got.Number != 2 || got.Worker.Agent != AgentName("a1", 2) {
			t.Errorf("second run = number %d agent %q, want 2 and %q", got.Number, got.Worker.Agent, AgentName("a1", 2))
		}
	})
}

func TestStopIsNoFailure(t *testing.T) {
	t.Run("stop_during_the_pick_leaves_the_task_queued", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.pk.block = true
		h.start()
		h.addTask(model.Task{ID: "a1", Title: titleLoop})
		h.until(func() bool { return h.pk.calls() == 1 })
		h.stop()
		if got := h.task("a1").State; got != model.TaskQueued {
			t.Errorf("task = %s, want queued", got)
		}
	})

	t.Run("stop_during_the_start_fails_nothing", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.block = true
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(func() bool { return len(h.rt.starts()) == 1 })
		h.stop()
		if got := h.run(RunID("a1", 1)).State; got != model.RunStarting {
			t.Errorf("run = %s, want starting", got)
		}
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want running", got)
		}
	})
}
