package model

import (
	"crypto/subtle"
	"log/slog"
	"time"
)

// RunID identifies one run of a task.
type RunID string

// RunState is the lifecycle state of one run (Symphony §7.2, with fanin's reported and rolled_over).
type RunState string

// The run states.
const (
	RunStarting   RunState = "starting"
	RunWorking    RunState = "working"
	RunStalled    RunState = "stalled"
	RunReported   RunState = "reported"
	RunSucceeded  RunState = "succeeded"
	RunFailed     RunState = "failed"
	RunTimedOut   RunState = "timed_out"
	RunRolledOver RunState = "rolled_over"
	RunCanceled   RunState = "canceled"
)

// AllRunStates lists every RunState.
var AllRunStates = []RunState{
	RunStarting, RunWorking, RunStalled, RunReported, RunSucceeded, RunFailed, RunTimedOut, RunRolledOver,
	RunCanceled,
}

// ParseRunState reads a stored run state; an unknown value is ErrUnknownValue.
func ParseRunState(s string) (RunState, error) { return parse("run state", s, AllRunStates) }

const redacted = "[redacted]"

// Token is a run's report token; it prints and logs as [redacted].
type Token string

// String hides the token from fmt.
func (Token) String() string { return redacted }

// GoString hides the token from %#v.
func (Token) GoString() string { return redacted }

// LogValue hides the token from slog.
func (Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// Equal compares the token with a presented one in constant time.
func (t Token) Equal(presented string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(presented)) == 1
}

// Budget counts what a run has spent; the limits live in config (see concept C-BUDGET).
type Budget struct {
	FixRounds    int
	ReviewNudges int
	Stalls       int
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	// ContextPct is the share of the worker's context window in use, 0 to 100.
	ContextPct int
	// SeenInput and SeenOutput are the context tokens of the last counted usage update; a repeat is not counted.
	SeenInput  int64
	SeenOutput int64
	// FooterWaits, ContinueNudges and SecureNudges count the steps of the silent-stop ladder (SCENARIOS AW-11).
	FooterWaits    int
	ContinueNudges int
	SecureNudges   int
}

// Run is one worker session on a task; a spent run is replaced by a fresh one with a higher Number.
type Run struct {
	ID     RunID
	TaskID TaskID
	// Number counts the task's runs from 1.
	Number int
	Slot   int
	Branch string
	// Hint is the task hint the run's prompt carried; a follow-up run takes it over.
	Hint   string
	Token  Token
	Budget Budget
	State  RunState
	// Tail is the worker's pane tail at its latest stop or stall (SCENARIOS AW-12).
	Tail string
	// Result is the worker's last report, nil before the first one.
	Result    *Result
	StartedAt time.Time
	EndedAt   time.Time
	// Worker names where the run's agent lives; Pane is empty until the start succeeded.
	Worker Worker
}

// Worker is the stored handle of a run's agent: enough to observe it again after a restart.
type Worker struct {
	Host      string
	Agent     string
	Workspace string
	Tab       string
	Pane      string
}
