package runtime

import (
	"context"
	"errors"
	"strings"
	"time"
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
)

// RunSpec is what a worker runtime needs to start one run.
type RunSpec struct {
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
	Host      string
	Agent     string
	Workspace string
	Tab       string
	Pane      string
}

// RunEventType says what a RunEvent reports.
type RunEventType string

// The run event types.
const (
	RunEventStatus RunEventType = "status"
	RunEventGone   RunEventType = "gone"
)

// RunEvent is one observation of a running worker.
type RunEvent struct {
	Type RunEventType
	// Status is the runtime's word for the agent's state.
	Status string
	At     time.Time
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
