package sched

import (
	"errors"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
)

// push hands ev to the agent's observer and waits until the loop took it, without a quiet marker: a failed step
// holds the cursor until the retry.
func (h *harness) push(agent string, ev runtime.RunEvent) {
	h.t.Helper()
	n := h.s.applied.Load()
	h.rt.mu.Lock()
	feed := h.rt.feed(agent)
	h.rt.mu.Unlock()
	go func() { feed <- ev }()
	h.until(func() bool { return h.s.applied.Load() > n })
}

func TestLadderFaults(t *testing.T) {
	// nudgeOnce nudges the first stall of run and gets it working again.
	nudgeOnce := func(h *harness, run model.Run) {
		h.send(run.Worker.Agent, quietStall(""))
		h.nudged(run.Worker.Agent, 1)
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
	}

	t.Run("AW-10_unwritable_next_run_keeps_the_spent_run", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		nudgeOnce(h, run)
		h.fault("runs", "INSERT")
		h.push(run.Worker.Agent, quietStall(""))
		h.until(h.logged("hand run " + string(run.ID) + " over"))
		if got := h.run(run.ID).State; got != model.RunStalled {
			t.Errorf("run = %s, want stalled", got)
		}
		if got := len(h.runs("a1")); got != 1 {
			t.Errorf("runs = %d, want no second run", got)
		}
	})

	t.Run("AW-10_unwritable_task_stays_running_at_max_runs", func(t *testing.T) {
		cfg := testConfig()
		cfg.Projects[0].Budgets.MaxRunsPerTask = 1
		h := newHarness(t, cfg)
		h.start()
		run := h.startOwner("a1")
		nudgeOnce(h, run)
		h.fault("tasks", "UPDATE")
		h.push(run.Worker.Agent, quietStall(""))
		h.until(h.logged("move task a1 to " + string(model.TaskNeedsOwner)))
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want running", got)
		}
	})

	t.Run("AW-11_unwritable_footer_wait_counts_nothing", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.fault("timers", "INSERT")
		h.fault("timers", "UPDATE")
		h.push(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventIdle, BGFooter: true})
		h.until(h.logged("await the footer of run " + string(run.ID)))
		if got := h.run(run.ID).Budget.FooterWaits; got != 0 {
			t.Errorf("footer waits = %d, want 0", got)
		}
	})

	t.Run("AW-13_unrecordable_nudge_is_not_sent", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.fault("runs", "UPDATE")
		h.push(run.Worker.Agent, idle())
		h.until(h.logged("record the nudge of run " + string(run.ID)))
		if got := h.rt.sent(run.Worker.Agent); len(got) != 0 {
			t.Errorf("prompts = %q, want none", got)
		}
	})

	t.Run("AW-13_unrecordable_failed_nudge_keeps_the_run", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.promptErr = errors.New("herdr: pane gone")
		h.start()
		run := h.startOwner("a1")
		h.fault("tasks", "UPDATE")
		h.push(run.Worker.Agent, quietStall(""))
		h.until(h.logged("record the failed nudge of " + string(run.ID)))
		if got := h.run(run.ID).State; got != model.RunStalled {
			t.Errorf("run = %s, want stalled", got)
		}
	})
}

func TestLadderEdges(t *testing.T) {
	t.Run("AW-11_await_timer_after_the_worker_resumed_climbs_nothing", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventIdle, BGFooter: true})
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.advance(FooterWait(h.cfg.Projects[0]))
		if got := h.run(run.ID).Budget.FooterWaits; got != 1 {
			t.Errorf("footer waits = %d, want 1", got)
		}
		if _, ok := h.timers("a1")[TimerAwait]; ok {
			t.Error("await timer still armed after it fired")
		}
		if got := h.rt.sent(run.Worker.Agent); len(got) != 0 {
			t.Errorf("prompts = %q, want none while the worker works", got)
		}
	})

	t.Run("C-BUDGET_handover_after_the_secure_nudge_names_the_unpushed_commits", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.up.commits = []string{"abc1234 fix the parser"}
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.nudged(run.Worker.Agent, 1)
		h.send(run.Worker.Agent, idle())
		h.nudged(run.Worker.Agent, 2)
		b := h.run(run.ID).Budget
		b.TotalTokens = 3_000_000
		h.spend(run.ID, b)
		h.until(h.runIn(RunID("a1", 2), model.RunWorking))
		if got := h.startSpec(AgentName("a1", 2)).Prompt; !strings.Contains(got, "- "+openUnpushed) {
			t.Errorf("next prompt lacks the unpushed note:\n%s", got)
		}
	})

	t.Run("C-BUDGET_spent_worker_that_cannot_be_stopped_stays_open", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.stopErr = errors.New("herdr: pane gone")
		h.start()
		run := h.startOwner("a1")
		h.spend(run.ID, model.Budget{TotalTokens: 3_000_000})
		h.until(h.runIn(RunID("a1", 2), model.RunWorking))
		h.until(h.logged("cannot stop the spent worker"))
		if got := h.run(run.ID).State; got != model.RunRolledOver {
			t.Errorf("spent run = %s, want rolled_over", got)
		}
	})

	t.Run("C-BUDGET_late_token_report_of_a_rolled_over_run_starts_nothing", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.spend(run.ID, model.Budget{TotalTokens: 3_000_000})
		h.until(h.runIn(RunID("a1", 2), model.RunWorking))
		h.spend(run.ID, model.Budget{TotalTokens: 4_000_000})
		h.quiet()
		if got := len(h.runs("a1")); got != 2 {
			t.Errorf("runs = %d, want 2", got)
		}
	})
}
