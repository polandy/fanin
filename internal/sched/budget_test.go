package sched

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

func quietStall(tail string) runtime.RunEvent {
	return runtime.RunEvent{Type: runtime.RunEventStalled, Reason: runtime.StallQuiet, Tail: tail}
}

// nudgeText is the nudge as project alpha's worker receives it.
func nudgeText(t *testing.T, name prompt.Name) string {
	t.Helper()
	text, err := prompt.Render(name, prompt.Vars{Repo: pathAlpha, Base: baseMain})
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// nudged waits until the agent got n prompts and returns them.
func (h *harness) nudged(agent string, n int) []string {
	h.t.Helper()
	h.until(func() bool { return len(h.rt.sent(agent)) >= n })
	return h.rt.sent(agent)
}

// lastTaskDetail is the detail of the task's newest task_state event.
func (h *harness) lastTaskDetail(id model.TaskID) string {
	h.t.Helper()
	got := h.events(id, model.EventTaskState)
	return got[len(got)-1]
}

func (h *harness) spend(id model.RunID, b model.Budget) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.SetRunBudget(ctx, id, b) })
}

// startSpec is the spec of the agent's start.
func (h *harness) startSpec(agent string) runtime.RunSpec {
	h.t.Helper()
	for _, s := range h.rt.starts() {
		if s.Agent == agent {
			return s
		}
	}
	h.t.Fatalf("no start of %s in %v", agent, h.rt.starts())
	return runtime.RunSpec{}
}

func TestStallLadder(t *testing.T) {
	t.Run("AW-10_first_stall_gets_one_nudge_and_the_second_rolls_over", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, quietStall("make test\nhangs here"))
		if got := h.nudged(run.Worker.Agent, 1); !cmp.Equal(got, []string{nudgeText(t, prompt.NudgeStall)}) {
			t.Fatalf("prompts after the first stall = %q, want one stall nudge", got)
		}
		if got := h.run(run.ID); got.State != model.RunStalled || got.Budget.Stalls != 1 {
			t.Fatalf("run = %s with %d stalls, want stalled with 1", got.State, got.Budget.Stalls)
		}
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.send(run.Worker.Agent, quietStall("still hangs"))
		next := RunID("a1", 2)
		h.until(h.runIn(next, model.RunWorking))
		if got := h.run(run.ID); got.State != model.RunRolledOver {
			t.Errorf("first run = %s, want rolled_over", got.State)
		}
		if got := h.rt.sent(run.Worker.Agent); len(got) != 1 {
			t.Errorf("prompts to the spent run = %d, want only the one stall nudge", len(got))
		}
		if got := h.run(next); got.Slot != run.Slot || got.Number != 2 {
			t.Errorf("next run = slot %d number %d, want slot %d number 2", got.Slot, got.Number, run.Slot)
		}
		h.until(func() bool { return slices.Contains(h.rt.stops(), run.Worker.Agent) })
		got := h.startSpec(AgentName("a1", 2)).Prompt
		for _, want := range []string{"## Handover from run 1", "no progress for 10m0s after the stall nudge",
			"still hangs", "do a1"} {
			if !strings.Contains(got, want) {
				t.Errorf("next prompt lacks %q:\n%s", want, got)
			}
		}
	})

	t.Run("AW-10_stall_timer_counts_like_a_runtime_stall", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.advance(stallAfter)
		h.nudged(run.Worker.Agent, 1)
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventProgress, Project: projAlpha, TaskID: "a1",
				RunID: run.ID, Detail: "tests green"})
			return err
		})
		h.quiet()
		h.advance(stallAfter)
		h.until(h.runIn(RunID("a1", 2), model.RunWorking))
		if !strings.Contains(h.startSpec(AgentName("a1", 2)).Prompt, "### State\n\ntests green") {
			t.Errorf("next prompt lacks the last progress:\n%s", h.startSpec(AgentName("a1", 2)).Prompt)
		}
	})

	t.Run("AW-10_second_stall_after_max_runs_asks_the_owner", func(t *testing.T) {
		cfg := testConfig()
		cfg.Projects[0].Budgets.MaxRunsPerTask = 1
		h := newHarness(t, cfg)
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, quietStall(""))
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventStalled, Reason: runtime.StallQuiet,
			LastWords: "waiting for the lock"})
		h.until(h.taskIn("a1", model.TaskNeedsOwner))
		want := "blocked a1-1: no progress for 10m0s after the stall nudge; tail: waiting for the lock; 1 of 1 runs used"
		if got := h.lastTaskDetail("a1"); got != want {
			t.Errorf("task detail = %q, want %q", got, want)
		}
		if got := len(h.runs("a1")); got != 1 {
			t.Errorf("runs = %d, want no second run", got)
		}
		if got := h.timers("a1"); len(got) != 0 {
			t.Errorf("timers = %v, want none", got)
		}
	})

	t.Run("AW-13_undeliverable_nudge_fails_the_run_at_once", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.promptErr = errors.New("herdr: pane gone")
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, quietStall(""))
		h.until(h.taskIn("a1", model.TaskFailed))
		if got := h.run(run.ID).State; got != model.RunFailed {
			t.Errorf("run = %s, want failed", got)
		}
		want := NudgeFailedDetail + "a1-1 failed: herdr: pane gone"
		if got := h.lastTaskDetail("a1"); got != want {
			t.Errorf("task detail = %q, want %q", got, want)
		}
		if got := h.timers("a1"); len(got) != 0 {
			t.Errorf("timers = %v, want nothing awaited", got)
		}
	})
}

func TestSilentStopLadder(t *testing.T) {
	t.Run("AW-11_footer_waits_then_continue_then_secure_then_blocked", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.up.commits = []string{"abc123 add the parser"}
		h.start()
		run := h.startOwner("a1")
		agent := run.Worker.Agent
		// The stop comes a minute after the start, so every footer wait ends before the first stall.
		h.advance(time.Minute)
		h.send(agent, runtime.RunEvent{Type: runtime.RunEventIdle, BGFooter: true, LastWords: "watching CI"})
		for i := 1; i < MaxFooterWaits; i++ {
			h.advance(FooterWait(h.cfg.Projects[0]))
		}
		if got := h.run(run.ID).Budget.FooterWaits; got != MaxFooterWaits {
			t.Fatalf("footer waits = %d, want %d", got, MaxFooterWaits)
		}
		if got := h.rt.sent(agent); len(got) != 0 {
			t.Fatalf("prompts while the footer is awaited = %q, want none", got)
		}
		h.advance(FooterWait(h.cfg.Projects[0]))
		h.nudged(agent, 1)
		h.send(agent, runtime.RunEvent{Type: runtime.RunEventIdle, LastWords: "done, I think"})
		h.nudged(agent, 2)
		h.send(agent, runtime.RunEvent{Type: runtime.RunEventIdle, LastWords: "all set"})
		h.until(h.taskIn("a1", model.TaskNeedsOwner))
		want := []string{nudgeText(t, prompt.NudgeContinue), nudgeText(t, prompt.NudgeSecure)}
		if got := h.rt.sent(agent); !cmp.Equal(got, want) {
			t.Errorf("prompts = %q, want continue then secure", got)
		}
		wantEv := []string{string(prompt.NudgeContinue), string(prompt.NudgeSecure)}
		if got := h.events("a1", model.EventWorkerNudged); !cmp.Equal(got, wantEv) {
			t.Errorf("worker_nudged = %q, want %q", got, wantEv)
		}
		wantDetail := "blocked a1-1: no status; unpushed: abc123 add the parser; tail: all set"
		if got := h.lastTaskDetail("a1"); got != wantDetail {
			t.Errorf("task detail = %q, want %q", got, wantDetail)
		}
		if got := h.timers("a1"); len(got) != 0 {
			t.Errorf("timers = %v, want none", got)
		}
		if !strings.Contains(h.logs.String(), "last_words=\"all set\"") {
			t.Errorf("logs lack the last words:\n%s", h.logs)
		}
	})

	t.Run("AW-11_no_unpushed_commits_skips_the_secure_nudge", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.nudged(run.Worker.Agent, 1)
		h.send(run.Worker.Agent, idle())
		h.until(h.taskIn("a1", model.TaskNeedsOwner))
		if got := h.rt.sent(run.Worker.Agent); !cmp.Equal(got, []string{nudgeText(t, prompt.NudgeContinue)}) {
			t.Errorf("prompts = %q, want only the continue nudge", got)
		}
		if got := h.lastTaskDetail("a1"); !strings.Contains(got, "unpushed: none") {
			t.Errorf("task detail = %q, want unpushed: none", got)
		}
	})

	t.Run("AW-11_reported_run_is_not_nudged", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunResult(ctx, run.ID, model.Result{Kind: model.ResultNothing, Text: textNoTodo})
		})
		h.until(h.runIn(run.ID, model.RunReported))
		h.send(run.Worker.Agent, idle())
		if got := h.events("a1", model.EventWorkerNudged); len(got) != 0 {
			t.Errorf("worker_nudged = %q, want none after the result", got)
		}
	})
}

func TestUnpushedScan(t *testing.T) {
	t.Run("AW-12_scan_covers_only_the_run_branch_and_the_tail_is_per_run", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.up.commits = []string{"abc123 wip"}
		h.start()
		run := h.startOwner("a1")
		other := h.startOwner("a2")
		h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.SetRunBranch(ctx, run.ID, "feat/a1") })
		h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.SetRunBranch(ctx, other.ID, "feat/a2") })
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventIdle, Tail: "a1 line 1\na1 line 2"})
		h.send(other.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventIdle, Tail: "a2 tail"})
		h.nudged(run.Worker.Agent, 1)
		h.send(run.Worker.Agent, idle())
		h.nudged(run.Worker.Agent, 2)
		if got := h.up.asked(); !cmp.Equal(got, []string{"feat/a1"}) {
			t.Errorf("scanned branches = %q, want only feat/a1", got)
		}
		if got := h.run(run.ID).Tail; got != "a1 line 1\na1 line 2" {
			t.Errorf("tail of a1 = %q", got)
		}
		if got := h.run(other.ID).Tail; got != "a2 tail" {
			t.Errorf("tail of a2 = %q", got)
		}
	})

	t.Run("AW-12_unknown_unpushed_commits_still_get_the_secure_nudge", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.up.err = ErrNoBranch
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.nudged(run.Worker.Agent, 1)
		h.send(run.Worker.Agent, idle())
		if got := h.nudged(run.Worker.Agent, 2); got[1] != nudgeText(t, prompt.NudgeSecure) {
			t.Errorf("second prompt = %q, want the secure nudge", got[1])
		}
		h.send(run.Worker.Agent, idle())
		h.until(h.taskIn("a1", model.TaskNeedsOwner))
		if got := h.lastTaskDetail("a1"); !strings.Contains(got, "unpushed: unknown") {
			t.Errorf("task detail = %q, want unpushed: unknown", got)
		}
	})
}

func TestRunBudgets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget model.Budget
		reason string
	}{
		{"tokens", model.Budget{TotalTokens: 3_000_000}, "token budget spent: 3000000 of 3000000 tokens"},
		{"context", model.Budget{ContextPct: 80}, "context 80% full, roll-over at 75%"},
		{"fix_rounds", model.Budget{FixRounds: 3}, "fix budget spent: fix round 3 of 2"},
	} {
		t.Run("C-BUDGET_spent_"+tc.name+"_hands_over_to_a_fresh_run", func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.start()
			run := h.startOwner("a1")
			h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.SetRunBranch(ctx, run.ID, "feat/a1") })
			h.spend(run.ID, tc.budget)
			next := RunID("a1", 2)
			h.until(h.runIn(next, model.RunWorking))
			if got := h.events("a1", model.EventRunState); !slices.Contains(got, RolledOverDetail+tc.reason) {
				t.Errorf("run details = %q, want %q", got, RolledOverDetail+tc.reason)
			}
			nr := h.run(next)
			if nr.Branch != "feat/a1" || nr.Hint != "do a1" || nr.Budget != (model.Budget{}) {
				t.Errorf("next run = branch %q hint %q budget %+v, want the branch and hint and a fresh budget",
					nr.Branch, nr.Hint, nr.Budget)
			}
			if nr.Token == run.Token {
				t.Error("next run reuses the spent run's token")
			}
			got := h.startSpec(AgentName("a1", 2)).Prompt
			for _, want := range []string{"Run 1 of this task ended: " + tc.reason, "### Branch\n\n`feat/a1`"} {
				if !strings.Contains(got, want) {
					t.Errorf("next prompt lacks %q:\n%s", want, got)
				}
			}
		})
	}

	t.Run("C-BUDGET_spent_budget_during_the_gate_reruns_the_task", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			if err := tx.SetRunResult(ctx, run.ID, model.Result{Kind: model.ResultDonePR, PR: 7}); err != nil {
				return err
			}
			return tx.SetTaskState(ctx, "a1", model.TaskGating, "pr 7")
		})
		h.until(h.runIn(run.ID, model.RunReported))
		h.spend(run.ID, model.Budget{FixRounds: 3})
		h.until(h.runIn(RunID("a1", 2), model.RunWorking))
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want running", got)
		}
		if got := h.startSpec(AgentName("a1", 2)).Prompt; !strings.Contains(got, "PR #7 is open") {
			t.Errorf("next prompt lacks the open PR:\n%s", got)
		}
	})

	t.Run("C-BUDGET_after_max_runs_per_task_the_task_pauses", func(t *testing.T) {
		cfg := testConfig()
		cfg.Projects[0].Budgets.MaxRunsPerTask = 2
		h := newHarness(t, cfg)
		h.start()
		run := h.startOwner("a1")
		h.spend(run.ID, model.Budget{TotalTokens: 3_000_000})
		next := RunID("a1", 2)
		h.until(h.runIn(next, model.RunWorking))
		h.spend(next, model.Budget{TotalTokens: 3_000_001})
		h.until(h.taskIn("a1", model.TaskPaused))
		want := "paused a1-2: token budget spent: 3000001 of 3000000 tokens; 2 of 2 runs used"
		if got := h.lastTaskDetail("a1"); got != want {
			t.Errorf("task detail = %q, want %q", got, want)
		}
		if got := len(h.runs("a1")); got != 2 {
			t.Errorf("runs = %d, want 2", got)
		}
		if got := h.run(next).State; got != model.RunWorking {
			t.Errorf("last run = %s, want it left open", got)
		}
	})

	t.Run("C-BUDGET_budgets_follow_the_config", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			budgets  config.Budgets
			rollover bool
		}{
			{"defaults_keep_the_run", testBudgets, false},
			{"low_token_limit_rolls_over", config.Budgets{FixRoundsPerRun: 2, RolloverPct: 75,
				MaxTokensPerRun: 1000, MaxRunsPerTask: 3}, true},
			{"low_rollover_pct_rolls_over", config.Budgets{FixRoundsPerRun: 2, RolloverPct: 40,
				MaxTokensPerRun: 3_000_000, MaxRunsPerTask: 3}, true},
			{"zero_fix_rounds_rolls_over", config.Budgets{FixRoundsPerRun: 0, RolloverPct: 75,
				MaxTokensPerRun: 3_000_000, MaxRunsPerTask: 3}, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfg := testConfig()
				cfg.Projects[0].Budgets = tc.budgets
				h := newHarness(t, cfg)
				h.start()
				run := h.startOwner("a1")
				h.spend(run.ID, model.Budget{TotalTokens: 5000, ContextPct: 50, FixRounds: 1})
				if tc.rollover {
					h.until(h.runIn(RunID("a1", 2), model.RunWorking))
					return
				}
				h.quiet()
				if got := h.run(run.ID).State; got != model.RunWorking {
					t.Errorf("run = %s, want working", got)
				}
				if got := len(h.runs("a1")); got != 1 {
					t.Errorf("runs = %d, want 1", got)
				}
			})
		}
	})
}
