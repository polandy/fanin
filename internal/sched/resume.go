package sched

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// resume observes every live run of a scheduled project again and arms its missing timers; a run without a
// stored pane keeps its slot and starts nothing (SCENARIOS SP-10, SP-11).
func (s *Scheduler) resume(ctx context.Context) error {
	tasks, err := s.d.Store.OpenTasks(ctx)
	if err != nil {
		return fmt.Errorf("sched: resume: read the open tasks: %w", err)
	}
	armed, err := s.d.Store.ArmedTimers(ctx)
	if err != nil {
		return fmt.Errorf("sched: resume: read the armed timers: %w", err)
	}
	for _, t := range tasks {
		p, ok := s.project(t.Project)
		if !ok {
			continue
		}
		runs, err := s.d.Store.Runs(ctx, t.ID)
		if err != nil {
			return fmt.Errorf("sched: resume: read the runs of %s: %w", t.ID, err)
		}
		if len(runs) == 0 || runs[len(runs)-1].State.Terminal() {
			continue
		}
		run := runs[len(runs)-1]
		log := s.logFor(p.Name, t.ID, run.ID)
		if run.Worker.Agent == "" || run.Worker.Pane == "" {
			s.watched[run.ID] = &watch{project: p.Name, run: run, unknown: true}
			log.ErrorContext(ctx, "sched: status of "+run.Worker.Agent+" unknown, no second worker started",
				"err", fmt.Errorf("%w: run %s in state %s has no pane stored", ErrStatusUnknown, run.ID, run.State))
			continue
		}
		if err := s.armMissing(ctx, p.Name, t.ID, run, armed); err != nil {
			return fmt.Errorf("sched: resume: arm the timers of %s: %w", t.ID, err)
		}
		s.watch(ctx, p.Name, run)
		log.InfoContext(ctx, "sched: resumed run", "agent", run.Worker.Agent)
	}
	return nil
}

// armMissing arms the stall and timeout timers a working or stalled run lacks.
func (s *Scheduler) armMissing(ctx context.Context, project string, task model.TaskID, run model.Run,
	armed []store.Timer) error {
	if run.State != model.RunWorking && run.State != model.RunStalled {
		return nil
	}
	have := map[string]bool{}
	for _, t := range armed {
		if t.TaskID == task {
			have[t.Kind] = true
		}
	}
	var missing []string
	for _, kind := range []string{TimerStall, TimerTimeout} {
		if !have[kind] {
			missing = append(missing, kind)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	p, _ := s.project(project)
	now := s.d.Clock.Now()
	return s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		return armTimers(ctx, tx, p, task, now, missing...)
	})
}
