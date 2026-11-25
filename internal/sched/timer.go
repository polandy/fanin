package sched

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

// ResultDetail is the detail of a stalled run that moves back to working before it is reported.
const ResultDetail = "result reported"

// The reasons the worker timeout spares a run, as logged.
const (
	spareUnknown = "status unknown"
	spareWorking = "busy"
	spareDialog  = "blocked on a dialog"
	spareBGShell = "own background shell"
	spareLock    = "holds the merge lock"
)

// TimeoutDetail leads the detail of a run the worker_timeout timer ended (SCENARIOS WD-1).
const TimeoutDetail = "worker timeout: no heartbeat or result for "

// onStoreEvent applies one store event of a scheduled project.
func (s *Scheduler) onStoreEvent(ctx context.Context, ev model.Event) error {
	p, ok := s.project(ev.Project)
	if !ok {
		return nil
	}
	switch ev.Type {
	case model.EventProgress:
		return s.moveRun(ctx, ev.RunID, model.RunStalled, model.RunWorking, "progress")
	case model.EventResultReported:
		if err := s.reported(ctx, ev); err != nil {
			return err
		}
		if err := s.finishAnalysis(ctx, p, ev); err != nil {
			return err
		}
		return s.finishPR(ctx, p, ev)
	case model.EventTokenUsage:
		return s.checkBudget(ctx, p, ev)
	case model.EventRunState, model.EventTaskState:
		return s.reconcile(ctx, ev.TaskID)
	case model.EventProjectResumed:
		delete(s.stopped, ev.Project)
	case model.EventOwnerGo:
		return s.startMerge(ctx, p, ev.TaskID, false)
	case model.EventHoldLifted:
		return s.retryHeld(ctx, ev.Project)
	}
	return nil
}

// reported moves the run to reported and disarms the task's timers: the result is in.
func (s *Scheduler) reported(ctx context.Context, ev model.Event) error {
	timers, err := s.taskTimers(ctx, ev.TaskID)
	if err != nil {
		return err
	}
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		run, err := tx.Run(ctx, ev.RunID)
		if err != nil {
			return err
		}
		if run.State == model.RunStalled {
			if err := tx.SetRunState(ctx, run.ID, model.RunWorking, ResultDetail); err != nil {
				return err
			}
			run.State = model.RunWorking
		}
		if run.State == model.RunWorking {
			if err := tx.SetRunState(ctx, run.ID, model.RunReported, ""); err != nil {
				return err
			}
		}
		if run.Branch == "" && run.Result != nil && run.Result.Branch != "" {
			if err := tx.SetRunBranch(ctx, run.ID, run.Result.Branch); err != nil {
				return err
			}
		}
		return disarm(ctx, tx, timers)
	})
	if err != nil {
		return fmt.Errorf("sched: record the result of run %s: %w", ev.RunID, err)
	}
	return nil
}

// reconcile drops the watch and the timers of a task whose task or latest run has ended.
func (s *Scheduler) reconcile(ctx context.Context, id model.TaskID) error {
	task, err := s.d.Store.Task(ctx, id)
	if err != nil {
		return fmt.Errorf("sched: read task %s: %w", id, err)
	}
	runs, err := s.d.Store.Runs(ctx, id)
	if err != nil {
		return fmt.Errorf("sched: read the runs of %s: %w", id, err)
	}
	if !task.State.Terminal() && (len(runs) == 0 || !runs[len(runs)-1].State.Terminal()) {
		return nil
	}
	for _, r := range runs {
		s.unwatch(r.ID)
	}
	delete(s.gates, id)
	timers, err := s.taskTimers(ctx, id)
	if err != nil || len(timers) == 0 {
		return err
	}
	return s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error { return disarm(ctx, tx, timers) })
}

// fireTimers handles every due timer of a scheduled project.
func (s *Scheduler) fireTimers(ctx context.Context) error {
	timers, err := s.d.Store.DueTimers(ctx)
	if err != nil {
		return fmt.Errorf("sched: read the due timers: %w", err)
	}
	for _, t := range timers {
		p, ok := s.project(t.Project)
		if !ok || !ownTimer(t.Kind) {
			continue
		}
		if err := s.fire(ctx, p, t); err != nil {
			return fmt.Errorf("sched: %s timer of task %s: %w", t.Kind, t.TaskID, err)
		}
	}
	return nil
}

// fire handles one due timer; a timer of an ended or reported run is only disarmed, except the gate's.
func (s *Scheduler) fire(ctx context.Context, p config.Project, t store.Timer) error {
	task, err := s.d.Store.Task(ctx, t.TaskID)
	if err != nil {
		return err
	}
	runs, err := s.d.Store.Runs(ctx, t.TaskID)
	if err != nil {
		return err
	}
	if gateTimer(t.Kind) {
		return s.fireGate(ctx, p, t, task, runs)
	}
	if len(runs) == 0 || task.State.Terminal() || runs[len(runs)-1].State.Terminal() ||
		runs[len(runs)-1].State == model.RunReported {
		return s.rearm(ctx, t, time.Time{}, nil)
	}
	run := runs[len(runs)-1]
	if t.Kind == TimerAwait {
		if err := s.rearm(ctx, t, time.Time{}, nil); err != nil {
			return err
		}
		if w := s.watched[run.ID]; w != nil && w.last.Type == runtime.RunEventIdle {
			return s.silentStop(ctx, p, w, w.last)
		}
		return nil
	}
	act, err := s.activity(ctx, run)
	if err != nil {
		return err
	}
	now := s.d.Clock.Now()
	if t.Kind == TimerStall {
		limit := p.StallAfter.Std()
		if due := act.Add(limit); due.After(now) {
			return s.rearm(ctx, t, due, nil)
		}
		if err := s.rearm(ctx, t, now.Add(limit), nil); err != nil {
			return err
		}
		return s.stall(ctx, p, run.ID, fmt.Sprintf("%sno heartbeat for %s", StalledDetail, limit), "")
	}
	return s.fireTimeout(ctx, p, t, task, run, act)
}

// fireTimeout ends a run without a heartbeat or result for worker_timeout, unless it is spared (WD-1, WD-2).
func (s *Scheduler) fireTimeout(ctx context.Context, p config.Project, t store.Timer, task model.Task, run model.Run,
	act time.Time) error {
	limit := p.WorkerTimeout.Std()
	now := s.d.Clock.Now()
	if due := act.Add(limit); due.After(now) {
		return s.rearm(ctx, t, due, nil)
	}
	log := s.logFor(p.Name, task.ID, run.ID)
	reason, next, err := s.spared(ctx, p, task, run)
	if err != nil {
		return err
	}
	if reason != "" {
		log.InfoContext(ctx, "sched: worker timeout spared", "agent", run.Worker.Agent, "reason", reason)
		return s.rearm(ctx, t, next, nil)
	}
	timers, err := s.taskTimers(ctx, task.ID)
	if err != nil {
		return err
	}
	detail := TimeoutDetail + limit.String()
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.SetRunState(ctx, run.ID, model.RunTimedOut, detail); err != nil {
			return err
		}
		cur, err := tx.Task(ctx, task.ID)
		if err != nil {
			return err
		}
		if cur.State.CanTransition(model.TaskFailed) == nil {
			if err := tx.SetTaskState(ctx, task.ID, model.TaskFailed, detail); err != nil {
				return err
			}
		}
		return disarm(ctx, tx, timers)
	})
	if err != nil {
		return err
	}
	log.WarnContext(ctx, "sched: worker timed out; its pane stays open", "agent", run.Worker.Agent, "after", limit)
	s.unwatch(run.ID)
	return nil
}

// spared says why the timeout leaves the run alone and when it looks again; "" ends the run. An unknown status
// spares it: an unreadable state never ends a run (WD-3, FP-6).
func (s *Scheduler) spared(ctx context.Context, p config.Project, task model.Task, run model.Run) (string,
	time.Time, error) {
	now := s.d.Clock.Now()
	next := now.Add(p.WorkerTimeout.Std())
	w := s.watched[run.ID]
	switch {
	case w == nil || w.unknown || !w.seen:
		return spareUnknown, next, nil
	case w.last.Type == runtime.RunEventWorking:
		return spareWorking, next, nil
	case w.last.Type == runtime.RunEventDialog:
		return spareDialog, next, nil
	case w.last.Type == runtime.RunEventStalled && w.last.Reason == runtime.StallBGShell &&
		now.Sub(w.bgSince) < BGFooterMax:
		return spareBGShell, minTime(next, w.bgSince.Add(BGFooterMax)), nil
	}
	locks, err := s.d.Store.Locks(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	for _, l := range locks {
		if l.Project == p.Name && !l.Expired(now) && (l.Holder == string(task.ID) || l.Holder == string(run.ID)) {
			return spareLock, next, nil
		}
	}
	return "", time.Time{}, nil
}

// activity is the newest sign of work of run: its start, a progress event or a runtime working event.
func (s *Scheduler) activity(ctx context.Context, run model.Run) (time.Time, error) {
	act := run.StartedAt
	evs, err := s.d.Store.Events(ctx, run.TaskID)
	if err != nil {
		return time.Time{}, err
	}
	for _, ev := range evs {
		if ev.RunID == run.ID && ev.Type == model.EventProgress && ev.At.After(act) {
			act = ev.At
		}
	}
	if w := s.watched[run.ID]; w != nil && w.activity.After(act) {
		act = w.activity
	}
	return act, nil
}

// rearm disarms t and, unless due is zero, arms it again at due, in one transaction with extra; a timer an
// earlier one of the same step disarmed is left alone.
func (s *Scheduler) rearm(ctx context.Context, t store.Timer, due time.Time,
	extra func(context.Context, *store.Tx) error) error {
	return s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.DisarmTimer(ctx, t.ID); errors.Is(err, store.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if !due.IsZero() {
			if _, err := tx.ArmTimer(ctx, store.Timer{Project: t.Project, TaskID: t.TaskID, Kind: t.Kind,
				DueAt: due}); err != nil {
				return err
			}
		}
		if extra == nil {
			return nil
		}
		return extra(ctx, tx)
	})
}

// taskTimers returns the armed timers of task.
func (s *Scheduler) taskTimers(ctx context.Context, task model.TaskID) ([]store.Timer, error) {
	all, err := s.d.Store.ArmedTimers(ctx)
	if err != nil {
		return nil, fmt.Errorf("sched: read the armed timers: %w", err)
	}
	var out []store.Timer
	for _, t := range all {
		if t.TaskID == task {
			out = append(out, t)
		}
	}
	return out, nil
}

// armTimers arms the given kinds for task, each due one limit from now.
func armTimers(ctx context.Context, tx *store.Tx, p config.Project, task model.TaskID, now time.Time,
	kinds ...string) error {
	for _, kind := range kinds {
		limit := p.WorkerTimeout.Std()
		switch kind {
		case TimerStall:
			limit = p.StallAfter.Std()
		case TimerAwait:
			limit = FooterWait(p)
		}
		if _, err := tx.ArmTimer(ctx, store.Timer{Project: p.Name, TaskID: task, Kind: kind,
			DueAt: now.Add(limit)}); err != nil {
			return err
		}
	}
	return nil
}

func disarm(ctx context.Context, tx *store.Tx, timers []store.Timer) error {
	for _, t := range timers {
		if err := tx.DisarmTimer(ctx, t.ID); err != nil {
			return err
		}
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
