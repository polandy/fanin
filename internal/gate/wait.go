package gate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// The timer kinds the gate arms; a fired timer of these kinds re-gates the task.
const (
	// TimerRetry re-reads a gate whose read failed (see SCENARIOS GH-6).
	TimerRetry = "gate_retry"
	// TimerWait is one GitHub wait on pending checks; a webhook may re-gate earlier (see SCENARIOS GH-7).
	TimerWait = "gate_wait"
	// TimerSettle re-reads once after pending checks settled.
	TimerSettle = "gate_settle"
	// TimerPoll is one Gitea poll (see SCENARIOS GT-4).
	TimerPoll = "gate_poll"
)

// The gate retry limits (see SCENARIOS GH-6).
const (
	RetryInterval = 60 * time.Second
	MaxReadErrors = 3
)

// Timers arms timer rows; a store.Tx is one.
type Timers interface {
	ArmTimer(ctx context.Context, timer store.Timer) (string, error)
}

// State is what one gate episode has used up so far; a new head starts from the zero State.
type State struct {
	// ReadErrors counts the failed reads in a row.
	ReadErrors int
	// Waits counts the GitHub waits on pending checks.
	Waits int
	// WatchFailures counts the network failures while waiting; they use up no wait.
	WatchFailures int
	// Settled is set once the settle re-read is armed.
	Settled bool
	// NoCheckPolls counts the Gitea polls without any status.
	NoCheckPolls int
	// Polls counts the other pending Gitea polls.
	Polls int
}

// Step is the outcome of one gate turn: a final Result, a final error, or a timer armed to come back.
type Step struct {
	// Final is set when the gate is done; Result or Err holds the answer.
	Final  bool
	Result Result
	// Err wraps ErrUnreachable when the reads kept failing, or forge.ErrInvalidRequest for a bad target; nobody is
	// nudged then.
	Err error
	// Timer is the armed timer when the gate is not final.
	Timer store.Timer
	State State
}

// Watcher runs the gate of one task with the wait limits of its project.
type Watcher struct {
	reader  Reader
	clock   clock.Clock
	limits  config.Gate
	target  Target
	project string
	task    model.TaskID
}

// NewWatcher returns the watcher of task in project; limits come from the project config.
func NewWatcher(r Reader, c clock.Clock, limits config.Gate, project string, task model.TaskID, t Target) *Watcher {
	return &Watcher{reader: r, clock: c, limits: limits, target: t, project: project, task: task}
}

// Turn reads the gate once and, unless it is final, arms the timer that brings it back. A timer that cannot be
// armed is an error, never a verdict.
func (w *Watcher) Turn(ctx context.Context, timers Timers, st State) (Step, error) {
	res, err := Read(ctx, w.reader, w.target)
	step := w.next(st, res, err)
	if step.Final {
		return step, nil
	}
	id, err := timers.ArmTimer(ctx, step.Timer)
	if err != nil {
		return Step{}, fmt.Errorf("gate: arm %s timer of task %s: %w", step.Timer.Kind, w.task, err)
	}
	step.Timer.ID = id
	return step, nil
}

// next applies the wait policy to one read.
func (w *Watcher) next(st State, res Result, err error) Step {
	if errors.Is(err, forge.ErrInvalidRequest) {
		return Step{Final: true, Err: err, State: st}
	}
	if err != nil {
		return w.readFailed(st, err)
	}
	st.ReadErrors = 0
	if w.target.Kind == forge.KindGitea {
		return w.giteaNext(st, res)
	}
	return w.githubNext(st, res)
}

// readFailed retries a failed read; a network failure while waiting uses up no wait (see SCENARIOS GH-6, GH-7).
func (w *Watcher) readFailed(st State, err error) Step {
	if st.Waits > 0 && errors.Is(err, forge.ErrNetwork) {
		st.WatchFailures++
		if st.WatchFailures > w.limits.GitHub.MaxWatchFailures {
			return Step{Final: true, State: st, Err: fmt.Errorf("%w: %d network failures while waiting: %w",
				ErrUnreachable, st.WatchFailures, err)}
		}
		return w.arm(st, TimerRetry, RetryInterval)
	}
	st.ReadErrors++
	if st.ReadErrors >= MaxReadErrors {
		return Step{Final: true, State: st, Err: fmt.Errorf("%w: %d failed reads %s apart: %w", ErrUnreachable,
			st.ReadErrors, RetryInterval, err)}
	}
	return w.arm(st, TimerRetry, RetryInterval)
}

// githubNext waits on pending checks up to MaxWaits times, then settles once (see SCENARIOS GH-7).
func (w *Watcher) githubNext(st State, res Result) Step {
	lim := w.limits.GitHub
	if res.Verdict != Pending {
		if st.Waits > 0 && !st.Settled && lim.Settle > 0 {
			st.Settled = true
			return w.arm(st, TimerSettle, lim.Settle.Std())
		}
		return Step{Final: true, Result: res, State: st}
	}
	if st.Waits >= lim.MaxWaits {
		return Step{Final: true, State: st, Result: red(fmt.Sprintf("still %s after %d waits", res.Wait, st.Waits))}
	}
	st.Waits++
	st.Settled = false
	return w.arm(st, TimerWait, lim.WatchTimeout.Std())
}

// giteaNext polls a pending gate: no status becomes NOCI, running checks become RED (see SCENARIOS GT-4).
func (w *Watcher) giteaNext(st State, res Result) Step {
	lim := w.limits.Gitea
	if res.Verdict != Pending {
		return Step{Final: true, Result: res, State: st}
	}
	if res.Wait == WaitNoChecks {
		if st.NoCheckPolls >= lim.NociAfterPolls {
			return Step{Final: true, State: st, Result: Result{Verdict: NoCI,
				Reasons: []string{fmt.Sprintf("%s after %d polls", res.Wait, st.NoCheckPolls)}}}
		}
		st.NoCheckPolls++
		return w.arm(st, TimerPoll, lim.PollInterval.Std())
	}
	if st.Polls >= lim.MaxPolls {
		return Step{Final: true, State: st, Result: red(fmt.Sprintf("still %s after %d polls", res.Wait, st.Polls))}
	}
	st.Polls++
	return w.arm(st, TimerPoll, lim.PollInterval.Std())
}

func (w *Watcher) arm(st State, kind string, d time.Duration) Step {
	return Step{State: st, Timer: store.Timer{Project: w.project, TaskID: w.task, Kind: kind,
		DueAt: w.clock.Now().Add(d)}}
}
