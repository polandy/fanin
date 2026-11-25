package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/model"
)

// SessionNameSep joins the parts of a worker's session name.
const SessionNameSep = " · "

// The worker runtime errors; adapters wrap them with the agent, the pane and herdr's last words.
var (
	// ErrInvalidSpec is returned for a RunSpec that lacks a required field.
	ErrInvalidSpec = errors.New("runtime: invalid run spec")
	// ErrOddAnswer is returned when the runtime answers in a shape the adapter cannot read.
	ErrOddAnswer = errors.New("runtime: odd answer")
	// ErrStartFailed is returned when no agent appeared after every start try.
	ErrStartFailed = errors.New("runtime: agent start failed")
	// ErrAgentTaken is returned when an agent of the same name lives in another pane.
	ErrAgentTaken = errors.New("runtime: agent name taken")
	// ErrTrustDialog is returned when the agent waits for the owner to trust the folder.
	ErrTrustDialog = errors.New("runtime: folder trust dialog")
	// ErrPromptRefused is returned when the runtime does not deliver a prompt.
	ErrPromptRefused = errors.New("runtime: prompt refused")
	// ErrNotWorking is returned when the agent did not start working after the prompt.
	ErrNotWorking = errors.New("runtime: agent not working")
	// ErrStatusUnknown is returned when the runtime cannot say whether the agent lives.
	ErrStatusUnknown = errors.New("runtime: agent status unknown")
	// ErrUnreachable is returned when the runtime stays unreadable after every retry.
	ErrUnreachable = errors.New("runtime: unreachable")
)

// The agent kinds a RunSpec names; an empty Kind is KindClaude.
const (
	KindClaude   = "claude"
	KindOpencode = "opencode"
)

// RunSpec is what a worker runtime needs to start one run.
type RunSpec struct {
	TaskID model.TaskID
	RunID  model.RunID
	// Project is the repo label; it names the herdr workspace and leads the session name.
	Project string
	// Host is the machine the worker runs on; empty means this host.
	Host     string
	Worktree string
	// Agent is the slot's agent name; it carries no lane name (concept C-SLOTS).
	Agent string
	// Title is the task's short title for the session name; empty leaves it out.
	Title     string
	Model     string
	Prompt    string
	MCPConfig string
	// Kind is the agent program, KindClaude or KindOpencode; opencode needs no MCPConfig.
	Kind string
	// Base is the project's base branch; the opencode sandbox clone is synced to it.
	Base string
	// PathDir, when set, goes first on the pane's PATH before the agent starts.
	PathDir string
}

// SessionName is "<project> · <short title> · <agent>" (see SCENARIOS SP-1).
func (s RunSpec) SessionName() string {
	parts := []string{s.Project}
	if s.Title != "" {
		parts = append(parts, s.Title)
	}
	return strings.Join(append(parts, s.Agent), SessionNameSep)
}

// RunHandle names a started run's agent and where it lives.
type RunHandle struct {
	// TaskID, RunID and Project are for logs only.
	TaskID    model.TaskID
	RunID     model.RunID
	Project   string
	Host      string
	Agent     string
	Workspace string
	Tab       string
	Pane      string
}

// RunEventType says what a RunEvent reports.
type RunEventType string

// The run event types (see docs/dev/spec/runtime-herdr.md, Observe).
const (
	RunEventWorking        RunEventType = RunEventType(model.RunWorking)
	RunEventIdle           RunEventType = "idle"
	RunEventGone           RunEventType = "gone"
	RunEventDialog         RunEventType = "dialog"
	RunEventStalled        RunEventType = RunEventType(model.RunStalled)
	RunEventAnsweredInPane RunEventType = "answered_in_pane"
	// RunEventUnreachable is the last event when the runtime cannot be read or driven; Err says why.
	RunEventUnreachable RunEventType = "unreachable"
)

// StallReason says why a run looks stalled.
type StallReason string

// The stall reasons (see SCENARIOS AW-4, AW-5).
const (
	StallQuiet   StallReason = "quiet"
	StallBGShell StallReason = "bg-shell"
)

// DialogUnreadable is the dialog text when the pane shows no readable question.
const DialogUnreadable = "unreadable"

// RunEvent is one observation of a running worker.
type RunEvent struct {
	Type RunEventType
	// Status is the runtime's word for the agent's state.
	Status string
	At     time.Time
	// Reason is set on RunEventStalled.
	Reason StallReason
	// Dialog is the question and its options on RunEventDialog, or DialogUnreadable.
	Dialog string
	// LastWords (input box and footer dropped) and Tail are set on RunEventIdle and RunEventStalled.
	LastWords string
	Tail      string
	// BGFooter is set on RunEventIdle when a live background footer outlasted the adapter's hold (SCENARIOS AW-11).
	BGFooter bool
	// Err is set on RunEventUnreachable; it wraps ErrUnreachable, or ErrPromptRefused for a failed Enter.
	Err error
}

// WorkerRuntime starts, prompts, observes and stops workers; callers never know which adapter runs a task.
type WorkerRuntime interface {
	// Start brings up the agent and hands it spec.Prompt; it returns only once the agent works.
	Start(ctx context.Context, spec RunSpec) (RunHandle, error)
	// Prompt sends text to a started agent; a refused prompt is an error at once.
	Prompt(ctx context.Context, h RunHandle, text string) error
	// Observe streams the agent's events until ctx ends.
	Observe(ctx context.Context, h RunHandle) (<-chan RunEvent, error)
	// Stop closes the run's own pane and nothing beside it.
	Stop(ctx context.Context, h RunHandle) error
}
