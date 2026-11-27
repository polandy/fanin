package model

import "time"

// EventType names one entry of a task's append-only event log.
type EventType string

// The event types.
const (
	EventTaskCreated      EventType = "task_created"
	EventTaskState        EventType = "task_state"
	EventRunStarted       EventType = "run_started"
	EventRunState         EventType = "run_state"
	EventProgress         EventType = "progress"
	EventTokenUsage       EventType = "token_usage"
	EventResultReported   EventType = "result_reported"
	EventGateVerdict      EventType = "gate_verdict"
	EventQuestionAsked    EventType = "question_asked"
	EventQuestionAnswered EventType = "question_answered"
	EventHoldSet          EventType = "hold_set"
	EventHoldLifted       EventType = "hold_lifted"
	EventHookStarted      EventType = "hook_started"
	EventHookFinished     EventType = "hook_finished"
	EventHookFailed       EventType = "hook_failed"
	EventWorkerNudged     EventType = "worker_nudged"
	EventPROpened         EventType = "pr_opened"
	EventMerged           EventType = "merged"
	EventMergeHeld        EventType = "merge_held"
	EventOwnerGo          EventType = "owner_go"
	EventProjectPaused    EventType = "project_paused"
	EventProjectResumed   EventType = "project_resumed"
	EventPauseConsumed    EventType = "pause_consumed"
	EventNotified         EventType = "notified"
	EventNotifyFailed     EventType = "notify_failed"
	EventTaskCancelled    EventType = "task_cancelled"
)

// AllEventTypes lists every EventType.
var AllEventTypes = []EventType{
	EventTaskCreated, EventTaskState, EventRunStarted, EventRunState, EventProgress, EventTokenUsage,
	EventResultReported, EventGateVerdict, EventQuestionAsked, EventQuestionAnswered, EventHoldSet, EventHoldLifted,
	EventHookStarted, EventHookFinished, EventHookFailed, EventWorkerNudged, EventPROpened, EventMerged,
	EventMergeHeld, EventOwnerGo, EventProjectPaused, EventProjectResumed, EventPauseConsumed,
	EventNotified, EventNotifyFailed, EventTaskCancelled,
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

// ReadyVerdictPrefix leads the detail of a gate_verdict that says a PR waits for the owner's go; the PR reference
// (`#<n>`), a colon and the verdict line follow.
const ReadyVerdictPrefix = "ready "
