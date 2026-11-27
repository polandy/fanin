package sched

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// watch is what the loop knows of one live run's worker.
type watch struct {
	project string
	run     model.Run
	cancel  context.CancelFunc
	// seen is set by the first runtime event; last is the newest one.
	seen bool
	last runtime.RunEvent
	// unknown is set while the worker's status cannot be read (WD-3).
	unknown bool
	// activity is the newest runtime sign of work; bgSince is when a bg-shell stall began.
	activity time.Time
	bgSince  time.Time
	// owed is the newest dialog or pane answer not yet handed to the owner channel.
	owed *runtime.RunEvent
}

// SpawnFailedDetail leads the detail of a run whose worker did not start.
const SpawnFailedDetail = "spawn failed "

// started records the live worker and arms its timers, or fails the run; the pane of a run cancelled meanwhile
// is closed, and a stop by ctx changes nothing.
func (s *Scheduler) started(ctx context.Context, p config.Project, run model.Run, h runtime.RunHandle, err error) {
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.spawnFailed(ctx, p, run, err)
		return
	}
	run.Worker = model.Worker{Host: run.Worker.Host, Agent: h.Agent, Workspace: h.Workspace, Tab: h.Tab, Pane: h.Pane}
	now := s.d.Clock.Now()
	live, cancelled := false, false
	err = s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		cur, err := tx.Run(ctx, run.ID)
		if err != nil {
			return err
		}
		cancelled = cur.State == model.RunCanceled
		if err := tx.SetRunWorker(ctx, run.ID, run.Worker); err != nil || cur.State != model.RunStarting {
			return err
		}
		live = true
		if err := tx.SetRunState(ctx, run.ID, model.RunWorking, ""); err != nil {
			return err
		}
		return armTimers(ctx, tx, p, run.TaskID, now, TimerStall, TimerTimeout)
	})
	log := s.logFor(p.Name, run.TaskID, run.ID)
	if err != nil {
		log.ErrorContext(ctx, "sched: cannot record the started worker; its status is unknown", "agent", h.Agent,
			"err", err)
		s.fail(ctx, err)
		return
	}
	if live {
		s.watch(ctx, p.Name, run)
	}
	if cancelled {
		s.closePane(ctx, p, run)
	}
}

// spawnFailed ends the run and its task failed (SCENARIOS SP-8); a run that already ended, as by a cancel, stays.
func (s *Scheduler) spawnFailed(ctx context.Context, p config.Project, run model.Run, cause error) {
	detail := fmt.Sprintf("%s%s: %v", SpawnFailedDetail, run.Worker.Agent, cause)
	s.logFor(p.Name, run.TaskID, run.ID).ErrorContext(ctx, "sched: spawn failed", "agent", run.Worker.Agent,
		"err", cause)
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if cur, err := tx.Run(ctx, run.ID); err != nil || cur.State.Terminal() {
			return err
		}
		if err := tx.SetRunState(ctx, run.ID, model.RunFailed, detail); err != nil {
			return err
		}
		return tx.SetTaskState(ctx, run.TaskID, model.TaskFailed, detail)
	})
	if err != nil {
		s.fail(ctx, fmt.Errorf("sched: record the failed spawn of %s: %w", run.ID, err))
	}
}

// end moves a queued task through running to done, or to failed, with detail.
func (s *Scheduler) end(ctx context.Context, task model.Task, detail string, failed bool) {
	to := model.TaskDone
	if failed {
		to = model.TaskFailed
	}
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.SetTaskState(ctx, task.ID, model.TaskRunning, detail); err != nil {
			return err
		}
		return tx.SetTaskState(ctx, task.ID, to, detail)
	})
	if err != nil {
		s.fail(ctx, fmt.Errorf("sched: end task %s: %w", task.ID, err))
	}
}

// watch observes the run's worker until it is unwatched; an observe that cannot start leaves the status unknown.
func (s *Scheduler) watch(ctx context.Context, project string, run model.Run) {
	wctx, cancel := context.WithCancel(ctx)
	w := &watch{project: project, run: run, cancel: cancel}
	s.watched[run.ID] = w
	log := s.logFor(project, run.TaskID, run.ID)
	h := handle(project, run)
	ch, err := s.d.Runtime.Observe(wctx, h)
	if err != nil {
		w.unknown = true
		log.ErrorContext(ctx, "sched: status of "+h.Agent+" unknown, no second worker started", "err", err)
		return
	}
	safego.Go(wctx, log, func(c context.Context) {
		for ev := range ch {
			s.post(c, func(ctx context.Context) { s.onRunEvent(ctx, w, ev) })
		}
		s.post(c, func(context.Context) { s.observeEnded(w) })
	})
}

// onRunEvent applies one runtime event; working moves a stalled run back, stalled marks it and climbs the stall
// ladder, idle climbs the silent-stop ladder, a dialog asks the owner and a pane answer closes the question (AW-4,
// AW-5, AW-9, AW-10, AW-11, WA-3).
func (s *Scheduler) onRunEvent(ctx context.Context, w *watch, ev runtime.RunEvent) {
	defer s.applied.Add(1)
	if s.watched[w.run.ID] != w {
		return
	}
	now := s.d.Clock.Now()
	w.seen, w.last = true, ev
	var err error
	switch ev.Type {
	case runtime.RunEventWorking, runtime.RunEventAnsweredInPane:
		w.unknown, w.activity, w.bgSince = false, now, time.Time{}
		err = s.moveRun(ctx, w.run.ID, model.RunStalled, model.RunWorking, string(ev.Type))
		if ev.Type == runtime.RunEventAnsweredInPane {
			w.owed = &ev
			err = errors.Join(err, s.payOwner(ctx, w))
		}
	case runtime.RunEventDialog:
		w.unknown, w.bgSince, w.owed = false, time.Time{}, &ev
		err = s.payOwner(ctx, w)
	case runtime.RunEventStalled:
		w.unknown = false
		if ev.Reason == runtime.StallBGShell {
			if w.bgSince.IsZero() {
				w.bgSince = now
			}
			err = s.moveRun(ctx, w.run.ID, model.RunWorking, model.RunStalled, StalledDetail+string(ev.Reason))
			break
		}
		w.bgSince = time.Time{}
		if p, ok := s.project(w.project); ok {
			err = s.stall(ctx, p, w.run.ID, StalledDetail+string(ev.Reason), ev.Tail)
		}
	case runtime.RunEventIdle:
		w.unknown, w.bgSince = false, time.Time{}
		if p, ok := s.project(w.project); ok {
			err = s.silentStop(ctx, p, w, ev)
		}
	case runtime.RunEventUnreachable:
		w.unknown = true
		s.logFor(w.project, w.run.TaskID, w.run.ID).ErrorContext(ctx, "sched: worker status unknown",
			"agent", w.run.Worker.Agent, "err", ev.Err)
	default:
		w.unknown, w.bgSince = false, time.Time{}
	}
	if err != nil {
		s.fail(ctx, err)
	}
}

// observeEnded marks the status unknown when the stream closed without the agent being gone.
func (s *Scheduler) observeEnded(w *watch) {
	defer s.applied.Add(1)
	if s.watched[w.run.ID] == w && w.last.Type != runtime.RunEventGone {
		w.unknown = true
	}
}

// moveRun moves the run from one state to another; a run in any other state is left alone.
func (s *Scheduler) moveRun(ctx context.Context, id model.RunID, from, to model.RunState, detail string) error {
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		run, err := tx.Run(ctx, id)
		if err != nil || run.State != from {
			return err
		}
		return tx.SetRunState(ctx, id, to, detail)
	})
	if err != nil {
		return fmt.Errorf("sched: move run %s to %s: %w", id, to, err)
	}
	return nil
}

func (s *Scheduler) unwatch(id model.RunID) {
	if w := s.watched[id]; w != nil {
		if w.cancel != nil {
			w.cancel()
		}
		delete(s.watched, id)
	}
}

func (s *Scheduler) unwatchAll() {
	for id := range s.watched {
		s.unwatch(id)
	}
}
