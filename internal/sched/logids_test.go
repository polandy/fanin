package sched

import (
	"errors"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime"
)

// idKeys are the IDs every task-bound log line carries (docs/dev/concept.md, Observability).
var idKeys = []string{"project=", "task_id=", "run_id="}

// notTaskBound are scheduler lines about the loop itself.
var notTaskBound = []string{"sched: step failed, retrying"}

func requireIDs(t *testing.T, logs string) {
	t.Helper()
	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" || matchesAny(notTaskBound, line) {
			continue
		}
		seen++
		for _, key := range idKeys {
			if !strings.Contains(line, " "+key) {
				t.Errorf("log line lacks %s: %s", key, line)
			}
		}
	}
	if seen == 0 {
		t.Error("the scenario logged no task-bound line, so the check proved nothing")
	}
}

func matchesAny(subs []string, line string) bool {
	for _, s := range subs {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

// TestLogIDs: every task-bound line the scheduler logs carries project, task_id and run_id.
// see SCENARIOS FP-5
func TestLogIDs(t *testing.T) {
	t.Run("OB-4_idle_pick_logs_carry_the_ids", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.pk.res = pick.Result{Idle: true, Reason: "no open todo"}
		h.start()
		h.addTask(model.Task{ID: "a1", Title: titleLoop})
		h.until(h.taskIn("a1", model.TaskDone))
		h.stop()
		requireIDs(t, h.logs.String())
	})

	t.Run("OB-4_spawn_and_stall_timeout_logs_carry_the_ids", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, idle())
		h.advance(stallAfter)
		h.advance(workerTimeout - stallAfter)
		h.stop()
		requireIDs(t, h.logs.String())
	})

	t.Run("OB-4_failed_spawn_logs_carry_the_ids", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.startErr = errors.New("herdr: no pane")
		h.start()
		h.addTask(ownerTask("a1"))
		h.until(h.runIn(RunID("a1", 1), model.RunFailed))
		h.stop()
		requireIDs(t, h.logs.String())
	})

	t.Run("OB-4_nudge_logs_carry_the_ids", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventIdle, LastWords: "done, I think"})
		h.nudged(run.Worker.Agent, 1)
		h.stop()
		requireIDs(t, h.logs.String())
		if !strings.Contains(h.logs.String(), "sched: nudging") {
			t.Errorf("the scenario logged no nudge line:\n%s", h.logs)
		}
	})
}
