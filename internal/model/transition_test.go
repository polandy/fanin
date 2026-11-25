package model_test

import (
	"errors"
	"testing"

	"github.com/polandy/fanin/internal/model"
)

// The allowed transitions, written out independently of the package's table.
var wantTaskTransitions = map[model.TaskState][]model.TaskState{
	model.TaskQueued: {model.TaskRunning, model.TaskCanceled},
	model.TaskRunning: {
		model.TaskGating, model.TaskQueued, model.TaskNeedsOwner, model.TaskPaused,
		model.TaskDone, model.TaskFailed, model.TaskCanceled,
	},
	model.TaskGating: {
		model.TaskRunning, model.TaskPostMerge, model.TaskDone, model.TaskNeedsOwner,
		model.TaskPaused, model.TaskFailed, model.TaskCanceled,
	},
	model.TaskPostMerge:  {model.TaskDone},
	model.TaskNeedsOwner: {model.TaskQueued, model.TaskRunning, model.TaskDone, model.TaskCanceled},
	model.TaskPaused:     {model.TaskQueued, model.TaskCanceled},
	model.TaskFailed:     {model.TaskQueued, model.TaskCanceled},
}

var wantRunTransitions = map[model.RunState][]model.RunState{
	model.RunStarting: {model.RunWorking, model.RunFailed, model.RunCanceled},
	model.RunWorking: {
		model.RunReported, model.RunStalled, model.RunFailed, model.RunTimedOut,
		model.RunRolledOver, model.RunCanceled,
	},
	model.RunStalled: {
		model.RunWorking, model.RunFailed, model.RunTimedOut, model.RunRolledOver, model.RunCanceled,
	},
	model.RunReported: {model.RunWorking, model.RunSucceeded, model.RunRolledOver, model.RunCanceled},
}

func contains[S comparable](list []S, s S) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestTaskTransitions(t *testing.T) {
	for _, from := range model.AllTaskStates {
		for _, to := range model.AllTaskStates {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				err := from.CanTransition(to)
				if contains(wantTaskTransitions[from], to) {
					if err != nil {
						t.Fatalf("allowed transition rejected: %v", err)
					}
					return
				}
				if !errors.Is(err, model.ErrTransition) {
					t.Fatalf("err = %v, want ErrTransition", err)
				}
			})
		}
	}
}

func TestRunTransitions(t *testing.T) {
	for _, from := range model.AllRunStates {
		for _, to := range model.AllRunStates {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				err := from.CanTransition(to)
				if contains(wantRunTransitions[from], to) {
					if err != nil {
						t.Fatalf("allowed transition rejected: %v", err)
					}
					return
				}
				if !errors.Is(err, model.ErrTransition) {
					t.Fatalf("err = %v, want ErrTransition", err)
				}
			})
		}
	}
}

func TestTransitionFromUnknownState(t *testing.T) {
	if err := model.TaskState("bogus").CanTransition(model.TaskRunning); !errors.Is(err, model.ErrTransition) {
		t.Errorf("task: err = %v, want ErrTransition", err)
	}
	if err := model.RunState("bogus").CanTransition(model.RunWorking); !errors.Is(err, model.ErrTransition) {
		t.Errorf("run: err = %v, want ErrTransition", err)
	}
}

func TestTerminal(t *testing.T) {
	for _, s := range model.AllTaskStates {
		if got, want := s.Terminal(), len(wantTaskTransitions[s]) == 0; got != want {
			t.Errorf("TaskState(%s).Terminal() = %v, want %v", s, got, want)
		}
	}
	for _, s := range model.AllRunStates {
		if got, want := s.Terminal(), len(wantRunTransitions[s]) == 0; got != want {
			t.Errorf("RunState(%s).Terminal() = %v, want %v", s, got, want)
		}
	}
}
