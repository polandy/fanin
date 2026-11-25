package sched

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
)

// spent names the run budget b has used up under lim, or "" while every one is left (concept C-BUDGET).
func spent(b model.Budget, lim config.Budgets) string {
	switch {
	case b.TotalTokens >= lim.MaxTokensPerRun:
		return fmt.Sprintf("token budget spent: %d of %d tokens", b.TotalTokens, lim.MaxTokensPerRun)
	case b.ContextPct >= lim.RolloverPct:
		return fmt.Sprintf("context %d%% full, roll-over at %d%%", b.ContextPct, lim.RolloverPct)
	case b.FixRounds > lim.FixRoundsPerRun:
		return fmt.Sprintf("fix budget spent: fix round %d of %d", b.FixRounds, lim.FixRoundsPerRun)
	}
	return ""
}

// checkBudget hands a live run whose budget is spent over to a fresh run, or pauses its task once the task has
// used max_runs_per_task runs.
func (s *Scheduler) checkBudget(ctx context.Context, p config.Project, ev model.Event) error {
	run, ok, err := s.liveRun(ctx, ev.TaskID, ev.RunID)
	if err != nil || !ok {
		return err
	}
	if reason := spent(run.Budget, p.Budgets); reason != "" {
		return s.handOver(ctx, p, run, reason, model.TaskPaused)
	}
	return nil
}

// liveRun reads the task's run id if it is the latest one, not ended, and its task is running or gating.
func (s *Scheduler) liveRun(ctx context.Context, task model.TaskID, id model.RunID) (model.Run, bool, error) {
	t, err := s.d.Store.Task(ctx, task)
	if err != nil {
		return model.Run{}, false, fmt.Errorf("sched: read task %s: %w", task, err)
	}
	runs, err := s.d.Store.Runs(ctx, task)
	if err != nil {
		return model.Run{}, false, fmt.Errorf("sched: read the runs of %s: %w", task, err)
	}
	if len(runs) == 0 || (t.State != model.TaskRunning && t.State != model.TaskGating) {
		return model.Run{}, false, nil
	}
	run := runs[len(runs)-1]
	if run.ID != id || run.State.Terminal() || run.State == model.RunStarting {
		return model.Run{}, false, nil
	}
	return run, true, nil
}
