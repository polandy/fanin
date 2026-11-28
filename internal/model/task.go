// Package model holds fanin's domain types: tasks, runs, events, worker results, gate verdicts, holds and owner
// questions, plus the state machines that guard task and run transitions. See docs/dev/spec/model.md.
package model

import "time"

// TaskID identifies a task across the store, logs and the API.
type TaskID string

// Shape says how a task ends.
type Shape string

// The task shapes.
const (
	ShapePR       Shape = "pr"
	ShapeAnalysis Shape = "analysis"
	ShapeDecision Shape = "decision"
)

// AllShapes lists every Shape.
var AllShapes = []Shape{ShapePR, ShapeAnalysis, ShapeDecision}

// ParseShape reads a stored shape; an unknown value is ErrUnknownValue.
func ParseShape(s string) (Shape, error) { return parse("shape", s, AllShapes) }

// TaskState is fanin's orchestration state of a task (Symphony §7.1 plus fanin's gate, merge and owner states).
type TaskState string

// The task states.
const (
	TaskQueued     TaskState = "queued"
	TaskRunning    TaskState = "running"
	TaskGating     TaskState = "gating"
	TaskPostMerge  TaskState = "post_merge"
	TaskNeedsOwner TaskState = "needs_owner"
	TaskPaused     TaskState = "paused"
	TaskDone       TaskState = "done"
	TaskFailed     TaskState = "failed"
	TaskCanceled   TaskState = "canceled"
)

// AllTaskStates lists every TaskState.
var AllTaskStates = []TaskState{
	TaskQueued, TaskRunning, TaskGating, TaskPostMerge, TaskNeedsOwner, TaskPaused, TaskDone, TaskFailed,
	TaskCanceled,
}

// ParseTaskState reads a stored task state; an unknown value is ErrUnknownValue.
func ParseTaskState(s string) (TaskState, error) { return parse("task state", s, AllTaskStates) }

// Task is one unit of work of a project; it ends as a PR, a report or an owner decision.
type Task struct {
	ID      TaskID
	Project string
	Title   string
	Prompt  string
	Shape   Shape
	State   TaskState
	// OwnerTask marks a task the owner named; its project loop ends after its first merge (see SCENARIOS PK-6).
	OwnerTask bool
	// After lists the tasks that must be done before this one is dispatched.
	After []TaskID
	// WorkerKind is the worker kind the owner asked for; empty means the project's.
	WorkerKind string
	// AllowedPaths is the task's file scope for fanin/scope; empty means no scope (see SCENARIOS SC-1).
	AllowedPaths []string
	OwnerModel   string
	ProjectModel string
	PickedModel  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Model returns the worker model: the owner's, else the project's, else the picked one (see SCENARIOS PK-5).
func (t Task) Model() string {
	for _, m := range []string{t.OwnerModel, t.ProjectModel, t.PickedModel} {
		if m != "" {
			return m
		}
	}
	return ""
}
