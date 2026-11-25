package model

import (
	"errors"
	"fmt"
	"slices"
)

// ErrTransition is returned for a state change that the transition table does not list.
var ErrTransition = errors.New("model: transition not allowed")

var taskTransitions = map[TaskState][]TaskState{
	TaskQueued: {TaskRunning, TaskCanceled},
	TaskRunning: {
		TaskGating, TaskQueued, TaskNeedsOwner, TaskPaused, TaskDone, TaskFailed, TaskCanceled,
	},
	TaskGating: {
		TaskRunning, TaskPostMerge, TaskDone, TaskNeedsOwner, TaskPaused, TaskFailed, TaskCanceled,
	},
	TaskPostMerge:  {TaskDone},
	TaskNeedsOwner: {TaskQueued, TaskRunning, TaskDone, TaskCanceled},
	TaskPaused:     {TaskQueued, TaskCanceled},
	TaskFailed:     {TaskQueued, TaskCanceled},
}

var runTransitions = map[RunState][]RunState{
	RunStarting: {RunWorking, RunFailed, RunCanceled},
	RunWorking:  {RunReported, RunStalled, RunFailed, RunTimedOut, RunRolledOver, RunCanceled},
	RunStalled:  {RunWorking, RunFailed, RunTimedOut, RunRolledOver, RunCanceled},
	RunReported: {RunWorking, RunSucceeded, RunRolledOver, RunCanceled},
}

// CanTransition returns nil when the table allows s -> to, else an error wrapping ErrTransition.
func (s TaskState) CanTransition(to TaskState) error {
	if slices.Contains(taskTransitions[s], to) {
		return nil
	}
	return fmt.Errorf("%w: task %q -> %q; allowed from %q: %v", ErrTransition, s, to, s, taskTransitions[s])
}

// Terminal reports whether no transition leaves s.
func (s TaskState) Terminal() bool { return len(taskTransitions[s]) == 0 }

// CanTransition returns nil when the table allows s -> to, else an error wrapping ErrTransition.
func (s RunState) CanTransition(to RunState) error {
	if slices.Contains(runTransitions[s], to) {
		return nil
	}
	return fmt.Errorf("%w: run %q -> %q; allowed from %q: %v", ErrTransition, s, to, s, runTransitions[s])
}

// Terminal reports whether no transition leaves s.
func (s RunState) Terminal() bool { return len(runTransitions[s]) == 0 }
