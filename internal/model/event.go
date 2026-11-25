package model

import "time"

// EventType names one entry of a task's append-only event log.
type EventType string

// The event types.
const (
	EventTaskCreated     EventType = "task_created"
	EventTaskState       EventType = "task_state"
	EventRunStarted      EventType = "run_started"
	EventRunState        EventType = "run_state"
	EventProgress        EventType = "progress"
	EventTokenUsage      EventType = "token_usage"
	EventResultReported  EventType = "result_reported"
	EventGateVerdict     EventType = "gate_verdict"
	EventQuestionAsked   EventType = "question_asked"
	EventQuestionAnswer  EventType = "question_answered"
	EventHoldSet         EventType = "hold_set"
	EventHoldLifted      EventType = "hold_lifted"
	EventHookStarted     EventType = "hook_started"
	EventHookFinished    EventType = "hook_finished"
	EventHookFailed      EventType = "hook_failed"
	EventWorkerNudged    EventType = "worker_nudged"
	EventPullRequestOpen EventType = "pr_opened"
	EventMerged          EventType = "merged"
)

// AllEventTypes lists every EventType.
var AllEventTypes = []EventType{
	EventTaskCreated, EventTaskState, EventRunStarted, EventRunState, EventProgress, EventTokenUsage,
	EventResultReported, EventGateVerdict, EventQuestionAsked, EventQuestionAnswer, EventHoldSet, EventHoldLifted,
	EventHookStarted, EventHookFinished, EventHookFailed, EventWorkerNudged, EventPullRequestOpen, EventMerged,
}

// ParseEventType reads a stored event type; an unknown value is ErrUnknownValue.
func ParseEventType(s string) (EventType, error) { return parse("event type", s, AllEventTypes) }

// Event is one entry of the event log; the store appends it in the transaction that changes the state.
type Event struct {
	Seq     int64
	Type    EventType
	Project string
	TaskID  TaskID
	// RunID is empty for task-level events.
	RunID RunID
	At    time.Time
	// Detail is a one-line summary for status and the rendered log.
	Detail string
}
