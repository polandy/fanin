package sched

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

// wallMax is the worker_max_wall of wallConfig, past one worker_timeout.
const wallMax = 90 * time.Minute

// wallReason is the handover reason of a run that reached wallMax.
const wallReason = "wall time spent: running for 1h30m0s, limit 1h30m0s"

// wallConfig sets alpha's worker_max_wall to wallMax; a stall_after past it keeps the stall ladder out.
func wallConfig() config.Config {
	cfg := testConfig()
	cfg.Projects[0].WorkerMaxWall = config.Duration(wallMax)
	cfg.Projects[0].StallAfter = config.Duration(2 * wallMax)
	return cfg
}

func (h *harness) progress(run model.Run) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventProgress, Project: projAlpha, TaskID: run.TaskID,
			RunID: run.ID, Detail: "still going"})
		return err
	})
	h.quiet()
}

func TestWorkerMaxWall(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep func(h *harness, run model.Run)
	}{
		{"working_status", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		}},
		{"steady_progress", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
			h.advance(50 * time.Minute)
			h.progress(run)
		}},
	} {
		t.Run("WD-5_"+tc.name+"_past_worker_max_wall_hands_over", func(t *testing.T) {
			h := newHarness(t, wallConfig())
			h.start()
			run := h.startOwner("a1")
			h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.SetRunBranch(ctx, run.ID, "feat/a1") })
			tc.keep(h, run)
			h.advance(workerTimeout - (h.clk.Now().Sub(epoch)))
			h.alive(run)
			if due := h.timers("a1")[TimerTimeout]; due.After(epoch.Add(wallMax)) {
				t.Errorf("timeout due %v, want it at the wall %v at the latest", due, epoch.Add(wallMax))
			}
			h.advance(epoch.Add(wallMax).Sub(h.clk.Now()))
			next := RunID("a1", 2)
			h.until(h.runIn(next, model.RunWorking))
			if got := h.run(run.ID).State; got != model.RunRolledOver {
				t.Errorf("spent run = %s, want rolled_over", got)
			}
			if got := h.events("a1", model.EventRunState); !slices.Contains(got, RolledOverDetail+wallReason) {
				t.Errorf("run details = %q, want %q", got, RolledOverDetail+wallReason)
			}
			got := h.startSpec(AgentName("a1", 2)).Prompt
			for _, want := range []string{"Run 1 of this task ended: " + wallReason, "### Branch\n\n`feat/a1`"} {
				if !strings.Contains(got, want) {
					t.Errorf("next prompt lacks %q:\n%s", want, got)
				}
			}
		})
	}

	t.Run("WD-5_wall_counts_from_the_run_start", func(t *testing.T) {
		h := newHarness(t, wallConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.advance(workerTimeout)
		h.alive(run)
		h.advance(wallMax - workerTimeout)
		next := RunID("a1", 2)
		h.until(h.runIn(next, model.RunWorking))
		if due := h.timers("a1")[TimerTimeout]; !due.Equal(h.clk.Now().Add(workerTimeout)) {
			t.Errorf("next run's timeout due %v, want one timeout after its start", due)
		}
	})

	t.Run("WD-5_after_max_runs_per_task_the_task_pauses", func(t *testing.T) {
		cfg := wallConfig()
		cfg.Projects[0].Budgets.MaxRunsPerTask = 1
		h := newHarness(t, cfg)
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.advance(workerTimeout)
		h.advance(wallMax - workerTimeout)
		h.until(h.taskIn("a1", model.TaskPaused))
		want := "paused a1-1: " + wallReason + "; 1 of 1 runs used"
		if got := h.lastTaskDetail("a1"); got != want {
			t.Errorf("task detail = %q, want %q", got, want)
		}
		if got := h.run(run.ID).State; got != model.RunWorking {
			t.Errorf("run = %s, want it left open", got)
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(h *harness, run model.Run)
	}{
		{"dialog", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventDialog, Dialog: "trust?"})
		}},
		{"merge_lock", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, idle())
			if err := h.st.AcquireLock(h.t.Context(), store.LockRequest{Project: projAlpha,
				Holder: string(run.TaskID), Lease: 3 * wallMax}); err != nil {
				h.t.Fatal(err)
			}
			h.progress(run)
		}},
		{"unknown_status", func(h *harness, run model.Run) {
			h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventUnreachable, Err: runtime.ErrUnreachable})
		}},
	} {
		t.Run("WD-5_wall_spares_a_run_with_"+tc.name, func(t *testing.T) {
			h := newHarness(t, wallConfig())
			h.start()
			run := h.startOwner("a1")
			tc.setup(h, run)
			h.advance(workerTimeout)
			h.advance(wallMax - workerTimeout)
			h.alive(run)
			if got := len(h.runs("a1")); got != 1 {
				t.Errorf("runs = %d, want no second run", got)
			}
		})
	}
}

func TestWorkerMaxWallFaults(t *testing.T) {
	t.Run("WD-5_unwritable_next_run_keeps_the_spent_run", func(t *testing.T) {
		h := newHarness(t, wallConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.advance(workerTimeout)
		h.fault("runs", "INSERT")
		h.clk.Advance(wallMax - workerTimeout)
		h.until(h.logged("hand run " + string(run.ID) + " over"))
		if got := h.run(run.ID).State; got != model.RunWorking {
			t.Errorf("run = %s, want working", got)
		}
		if got := len(h.runs("a1")); got != 1 {
			t.Errorf("runs = %d, want no second run", got)
		}
	})

	t.Run("WD-5_unreadable_lock_never_hands_over", func(t *testing.T) {
		h := newHarness(t, wallConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.advance(50 * time.Minute)
		h.progress(run)
		h.advance(workerTimeout - 50*time.Minute)
		h.sql(`DROP TABLE project_locks`)
		h.clk.Advance(wallMax - workerTimeout)
		h.until(h.logged(stepFailed))
		if got := h.run(run.ID).State; got != model.RunWorking {
			t.Errorf("run = %s, want working", got)
		}
		if got := len(h.runs("a1")); got != 1 {
			t.Errorf("runs = %d, want no second run", got)
		}
	})
}
