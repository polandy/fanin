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
	Worker    string    `json:"worker,omitempty"`
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
	Kind     string `json:"kind"`
	PR       int    `json:"pr,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Issue    int    `json:"issue,omitempty"`
	IssueURL string `json:"issue_url,omitempty"`
	Text     string `json:"text,omitempty"`
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
	// Board is the owner board, one entry per project in Projects order.
	Board []BoardProject `json:"board"`
	// Hosts are the worker hosts with their agents; empty when the daemon has no fleet view.
	Hosts   []Host `json:"hosts"`
	LastSeq int64  `json:"last_seq"`
}

// BoardTask is a task as the board lists it.
type BoardTask struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Shape     string    `json:"shape"`
	UpdatedAt time.Time `json:"updated_at"`
	// Blocked is set on a queued task with a dependency that is still open.
	Blocked bool `json:"blocked,omitempty"`
	// QuestionID names the open owner question of a task that waits for the owner.
	QuestionID string `json:"question_id,omitempty"`
}

// BoardDone is the Erledigt section: the tasks done in the project's current batch.
type BoardDone struct {
	Count  int         `json:"count"`
	Newest []BoardTask `json:"newest"`
}

// BoardProject is the board of one project (SCENARIOS ST-2).
type BoardProject struct {
	Project string `json:"project"`
	// Done counts the tasks done since the oldest task that is still open was created, and lists the newest few.
	Done BoardDone `json:"done"`
	// Next are the queued tasks that have not started.
	Next []BoardTask `json:"next"`
	// Waiting are the tasks that wait on the owner: needs_owner or paused.
	Waiting []BoardTask `json:"waiting"`
	// Holds are the project's active CI and merge holds; each can be lifted.
	Holds []Hold `json:"holds"`
	// Decisions are the open decision-shaped tasks that are not already in Waiting.
	Decisions []BoardTask `json:"decisions"`
}

// Worker is one agent on a host. Kind is "managed" for a fanin worker and "owner" for an owner session.
type Worker struct {
	Agent   string `json:"agent"`
	Project string `json:"project"`
	Session string `json:"session"`
	Status  string `json:"status"`
	Kind    string `json:"kind"`
	TaskID  string `json:"task_id,omitempty"`
	// WaitingOwner is set while the agent waits on the owner.
	WaitingOwner bool `json:"waiting_owner"`
}

// The worker kinds.
const (
	WorkerManaged = "managed"
	WorkerOwner   = "owner"
)

// Host is one machine and its agents. A non-empty Warning means the host could not be read; Workers is then empty.
type Host struct {
	Name    string   `json:"name"`
	Warning string   `json:"warning,omitempty"`
	Workers []Worker `json:"workers"`
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

// TaskRequest is the body of POST /api/v1/tasks: an owner task or an owner question.
type TaskRequest struct {
	ID      string   `json:"id,omitempty"`
	Project string   `json:"project"`
	Task    string   `json:"task,omitempty"`
	Analyze string   `json:"analyze,omitempty"`
	Model   string   `json:"model,omitempty"`
	Worker  string   `json:"worker,omitempty"`
	After   []string `json:"after,omitempty"`
}

// Tasks is the answer of GET /api/v1/tasks.
type Tasks struct {
	Tasks []Task `json:"tasks"`
}
