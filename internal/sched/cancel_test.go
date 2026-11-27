package sched

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const cancelReason = "merged outside fanind"

// cancelTask gives the owner's cancel for id.
func (h *harness) cancelTask(id model.TaskID) {
	h.t.Helper()
	svc := owner.New(h.st, owner.NoDeliverer{}, slog.New(slog.DiscardHandler))
	if err := svc.Cancel(h.t.Context(), id, cancelReason); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) startedAgent(agent string) func() bool {
	return func() bool {
		return slices.ContainsFunc(h.rt.starts(), func(s runtime.RunSpec) bool { return s.Agent == agent })
	}
}

func TestCancel(t *testing.T) {
	t.Run("OA-8_a_cancelled_gating_task_frees_its_slot_and_the_next_queued_task_spawns", func(t *testing.T) {
		cfg := mergeConfig()
		cfg.Projects[0].MaxParallel = 1
		r := newMergeRig(t, cfg)
		r.start()
		run := r.ready("a1", prNumber)
		r.addTask(ownerTask("a2"))
		r.quiet()
		if r.startedAgent(AgentName("a2", 1))() {
			t.Fatal("a2 started while a1 holds the only slot")
		}
		r.cancelTask("a1")
		r.until(r.startedAgent(AgentName("a2", 1)))
		r.until(r.stopped(run.Worker.Agent))
		if got := r.run(run.ID).State; got != model.RunCanceled {
			t.Errorf("a1 run state = %s, want %s", got, model.RunCanceled)
		}
		if got := r.log.all(); len(got) != 0 {
			t.Errorf("merges = %v, want none", got)
		}
	})

	t.Run("OA-8_a_cancelled_running_task_closes_its_pane", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.cancelTask("a1")
		h.until(h.stopped(run.Worker.Agent))
		if tm := h.timers("a1"); len(tm) != 0 {
			t.Errorf("timers = %v, want none", tm)
		}
	})

	t.Run("OA-8_a_run_cancelled_while_its_worker_starts_closes_the_pane_it_got", func(t *testing.T) {
		h := newHarness(t, testConfig())
		hold := make(chan struct{})
		h.rt.hold = hold
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.startedAgent(AgentName("a1", 1)))
		h.cancelTask("a1")
		close(hold)
		h.until(h.stopped(AgentName("a1", 1)))
		if got := h.run(RunID("a1", 1)).State; got != model.RunCanceled {
			t.Errorf("run state = %s, want %s", got, model.RunCanceled)
		}
		if n := len(h.rt.observes()); n != 0 {
			t.Errorf("observes = %d, want none of a cancelled run", n)
		}
	})

	t.Run("OA-8_a_cancelled_task_with_a_go_waiting_for_the_lock_never_merges", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.start()
		r.ready("a1", prNumber)
		r.ghostLock()
		r.goMerge("a1")
		r.await(r.waits)
		r.cancelTask("a1")
		if err := r.st.ReleaseLock(t.Context(), projAlpha, "gone"); err != nil {
			t.Fatal(err)
		}
		r.until(func() bool { return strings.Contains(r.logs.String(), MergeDroppedLog) })
		r.quiet()
		if got := r.log.all(); len(got) != 0 {
			t.Errorf("merges = %v, want none", got)
		}
		if locks, err := r.st.Locks(t.Context()); err != nil || len(locks) != 0 {
			t.Errorf("locks = %+v %v, want the lock released", locks, err)
		}
	})

	t.Run("OA-8_a_lock_left_by_a_cancelled_task_is_released", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.start()
		r.ready("a1", prNumber)
		if err := r.st.AcquireLock(t.Context(), store.LockRequest{Project: projAlpha, Holder: "a1",
			Lease: lockLease}); err != nil {
			t.Fatal(err)
		}
		r.cancelTask("a1")
		r.until(func() bool {
			locks, err := r.st.Locks(t.Context())
			return err == nil && len(locks) == 0
		})
	})
}
