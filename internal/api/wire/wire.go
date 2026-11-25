// Package wire holds the JSON types of the fanin HTTP API; the server and the Go client share them.
package wire

import "time"

// StatusOK is the status of a healthy fanind.
const StatusOK = "ok"

// Error codes of the JSON error format.
const (
	CodeNotFound     = "not_found"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeBadRequest   = "bad_request"
	CodeTooLarge     = "too_large"
	CodeInternal     = "internal"
	CodeConflict     = "conflict"
	// CodeDeliveryFailed is an answer that could not reach the worker; nothing was recorded.
	CodeDeliveryFailed = "delivery_failed"
)

// Error is the body of every non-2xx answer, wrapped as {"error": {...}}.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorBody is the JSON document of a failed call.
type ErrorBody struct {
	Error Error `json:"error"`
}

// Health is the answer of GET /api/v1/health.
type Health struct {
	Status string `json:"status"`
}

// Task is one task of a project.
type Task struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"`
	Shape     string    `json:"shape"`
	State     string    `json:"state"`
	OwnerTask bool      `json:"owner_task"`
	After     []string  `json:"after"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Budget is the usage of one run.
type Budget struct {
	FixRounds    int   `json:"fix_rounds"`
	ReviewNudges int   `json:"review_nudges"`
	Stalls       int   `json:"stalls"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// Result is the last report of a run.
type Result struct {
	Kind   string `json:"kind"`
	PR     int    `json:"pr,omitempty"`
	Branch string `json:"branch,omitempty"`
	Issue  int    `json:"issue,omitempty"`
	Text   string `json:"text,omitempty"`
}

// Run is one worker session; its report token is never part of the API.
type Run struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Number    int       `json:"number"`
	Slot      int       `json:"slot"`
	Branch    string    `json:"branch"`
	State     string    `json:"state"`
	Budget    Budget    `json:"budget"`
	Result    *Result   `json:"result,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

// Option is one choice of an owner question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Question is an owner question; it is open while AnsweredAt is zero.
type Question struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	RunID       string    `json:"run_id"`
	Project     string    `json:"project"`
	Kind        string    `json:"kind"`
	AllowText   bool      `json:"allow_text"`
	Question    string    `json:"question"`
	Options     []Option  `json:"options"`
	Recommended int       `json:"recommended"`
	AskedAt     time.Time `json:"asked_at"`
}

// Hold is an active project hold.
type Hold struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason"`
	FixTask   string    `json:"fix_task"`
	CreatedAt time.Time `json:"created_at"`
}

// Lock is a project lock. It is stale when its lease ran out or its holder is no open task or live run.
type Lock struct {
	Project    string    `json:"project"`
	Holder     string    `json:"holder"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Stale      bool      `json:"stale"`
}

// Snapshot is the answer of GET /api/v1/snapshot: everything open right now. Projects lists the configured
// projects in config order, then any other project the store names, sorted. Latest holds the newest event of
// each project. LastSeq is the newest event the snapshot already reflects; open the event stream with
// after=LastSeq to see only later changes.
type Snapshot struct {
	Projects  []string   `json:"projects"`
	Tasks     []Task     `json:"tasks"`
	Runs      []Run      `json:"runs"`
	Questions []Question `json:"questions"`
	Holds     []Hold     `json:"holds"`
	Locks     []Lock     `json:"locks"`
	Latest    []Event    `json:"latest"`
	LastSeq   int64      `json:"last_seq"`
}

// Event is one entry of the event log, the data of a server-sent event.
type Event struct {
	Seq     int64     `json:"seq"`
	Type    string    `json:"type"`
	Project string    `json:"project"`
	TaskID  string    `json:"task_id"`
	RunID   string    `json:"run_id"`
	At      time.Time `json:"at"`
	Detail  string    `json:"detail"`
}

// Questions is the answer of GET /api/v1/questions: the open owner questions, oldest first.
type Questions struct {
	Questions []Question `json:"questions"`
}

// AnswerRequest is the body of POST /api/v1/tasks/{id}/answer: an option number 1-9, or free text when the
// question allows it.
type AnswerRequest struct {
	Answer string `json:"answer"`
}

// Ack is the answer of an owner command; Detail says what it did.
type Ack struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
