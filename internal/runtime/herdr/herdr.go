// Package herdr is the WorkerRuntime adapter that runs workers in herdr panes (concept D2, docs/dev/spec/runtime-herdr.md).
package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
)

// The start and prompt limits (see SCENARIOS SP-2, SP-6).
const (
	DefaultStartTries        = 8
	DefaultStartTimeout      = 120 * time.Second
	DefaultStartInterval     = 120 * time.Second
	DefaultShellPause        = 5 * time.Second
	DefaultWorkChecks        = 10
	DefaultWorkCheckInterval = 3 * time.Second
	// TrustDialogLines and InputLines are how many pane lines the dialog and input box checks read.
	TrustDialogLines = 30
	InputLines       = 6
	// AnswerTail caps how much of an odd herdr answer an error quotes.
	AnswerTail = 200
)

// TrustDialog is the text of claude's folder trust dialog.
const TrustDialog = "trust this folder"

// emptyInput matches claude's input box when the prompt paste got lost.
var emptyInput = regexp.MustCompile(`(?m)^❯( *$| Try ")`)

// The herdr program, its words and flags.
const (
	Program       = "herdr"
	FlagMachine   = "--machine"
	AgentKind     = "claude"
	KeyEnter      = "enter"
	permissionArg = "auto"
)

// The agent statuses the adapter switches on; Gone marks an agent herdr no longer lists.
const (
	StatusWorking = string(model.RunWorking)
	StatusBlocked = "blocked"
	StatusIdle    = "idle"
	StatusDone    = string(model.TaskDone)
	StatusGone    = "gone"
)

// Config holds the adapter's limits; DefaultConfig gives the scenario values.
type Config struct {
	StartTries        int
	StartTimeout      time.Duration
	StartInterval     time.Duration
	ShellPause        time.Duration
	WorkChecks        int
	WorkCheckInterval time.Duration
	PollInterval      time.Duration
	ReadRetries       int
	ReadRetryInterval time.Duration
	// StallSlice times StallSlices of an unchanged pane is a quiet stall; MaxBGWaits slices cap a live footer's hold.
	StallSlice    time.Duration
	StallSlices   int
	BGShellWindow time.Duration
	MaxBGWaits    int
	ResumeEnters  int
	ResumeGrace   time.Duration
}

// DefaultConfig returns the limits of SCENARIOS SP-2, SP-6 and AW-3 to AW-8.
func DefaultConfig() Config {
	return Config{
		StartTries:        DefaultStartTries,
		StartTimeout:      DefaultStartTimeout,
		StartInterval:     DefaultStartInterval,
		ShellPause:        DefaultShellPause,
		WorkChecks:        DefaultWorkChecks,
		WorkCheckInterval: DefaultWorkCheckInterval,
		PollInterval:      DefaultPollInterval,
		ReadRetries:       DefaultReadRetries,
		ReadRetryInterval: DefaultReadRetryInterval,
		StallSlice:        DefaultStallSlice,
		StallSlices:       DefaultStallSlices,
		BGShellWindow:     DefaultBGShellWindow,
		MaxBGWaits:        DefaultMaxBGWaits,
		ResumeEnters:      DefaultResumeEnters,
		ResumeGrace:       DefaultResumeGrace,
	}
}

// Runtime drives herdr through a runtime.Runner.
type Runtime struct {
	runner runtime.Runner
	clock  clock.Clock
	logger *slog.Logger
	cfg    Config
	mu     sync.Mutex
	// prompted is when fanin last sent each agent input, keyed by host and agent.
	prompted map[string]time.Time
}

var _ runtime.WorkerRuntime = (*Runtime)(nil)

// New returns a herdr runtime.
func New(runner runtime.Runner, clk clock.Clock, logger *slog.Logger, cfg Config) *Runtime {
	return &Runtime{runner: runner, clock: clk, logger: logger, cfg: cfg, prompted: map[string]time.Time{}}
}

// Start finds or creates the project's workspace, opens a tab, starts claude and sends the prompt (SCENARIOS SP-1..SP-6).
func (r *Runtime) Start(ctx context.Context, spec runtime.RunSpec) (runtime.RunHandle, error) {
	if err := validate(spec); err != nil {
		return runtime.RunHandle{}, err
	}
	h := runtime.RunHandle{Host: spec.Host, Agent: spec.Agent}
	var err error
	if h.Workspace, err = r.workspace(ctx, spec); err != nil {
		return h, err
	}
	if h.Tab, h.Pane, err = r.tab(ctx, spec, h.Workspace); err != nil {
		return h, err
	}
	// The fresh shell needs a moment before it sits at its prompt.
	if err := r.wait(ctx, r.cfg.ShellPause); err != nil {
		return h, err
	}
	if err := r.startAgent(ctx, spec, h); err != nil {
		return h, err
	}
	if err := r.Prompt(ctx, h, spec.Prompt); err != nil {
		return h, err
	}
	return h, r.awaitWorking(ctx, h, spec.Prompt)
}

func validate(s runtime.RunSpec) error {
	fields := []struct{ key, value string }{
		{"Project", s.Project}, {"Worktree", s.Worktree}, {"Agent", s.Agent}, {"Prompt", s.Prompt},
		{"MCPConfig", s.MCPConfig},
	}
	for _, f := range fields {
		if f.value == "" {
			return fmt.Errorf("%w: RunSpec.%s is empty; set it for agent %q", runtime.ErrInvalidSpec, f.key, s.Agent)
		}
	}
	return nil
}

func (r *Runtime) workspace(ctx context.Context, spec runtime.RunSpec) (string, error) {
	out, err := r.herdr(ctx, spec.Host, "workspace", "list")
	if err != nil {
		return "", fmt.Errorf("cannot list the herdr workspaces: %w", err)
	}
	var list struct {
		Result struct {
			Workspaces []struct {
				Label       string `json:"label"`
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := decode(out, &list); err != nil {
		return "", fmt.Errorf("herdr workspace list: %w", err)
	}
	for _, w := range list.Result.Workspaces {
		if w.Label == spec.Project && w.WorkspaceID != "" {
			return w.WorkspaceID, nil
		}
	}
	out, err = r.herdr(ctx, spec.Host, "workspace", "create", "--cwd", spec.Worktree, "--label", spec.Project, "--no-focus")
	if err != nil {
		return "", fmt.Errorf("cannot create the herdr workspace %s: %w", spec.Project, err)
	}
	var created struct {
		Result struct {
			Workspace struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspace"`
		} `json:"result"`
	}
	if err := decode(out, &created); err != nil || created.Result.Workspace.WorkspaceID == "" {
		return "", fmt.Errorf("%w: herdr workspace create for %s: %s", runtime.ErrOddAnswer, spec.Project, tail(out))
	}
	return created.Result.Workspace.WorkspaceID, nil
}

func (r *Runtime) tab(ctx context.Context, spec runtime.RunSpec, ws string) (tab, pane string, err error) {
	out, err := r.herdr(ctx, spec.Host, "tab", "create", "--workspace", ws, "--cwd", spec.Worktree,
		"--label", spec.Agent, "--no-focus")
	if err != nil {
		return "", "", fmt.Errorf("cannot create a tab in workspace %s: %w", ws, err)
	}
	var created struct {
		Result struct {
			Tab struct {
				TabID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := decode(out, &created); err != nil || created.Result.Tab.TabID == "" || created.Result.RootPane.PaneID == "" {
		return "", "", fmt.Errorf("%w: herdr tab create in workspace %s: %s", runtime.ErrOddAnswer, ws, tail(out))
	}
	return created.Result.Tab.TabID, created.Result.RootPane.PaneID, nil
}

// startArgv is `agent start` with claude's flags after the `--`.
func (r *Runtime) startArgv(spec runtime.RunSpec, pane string) []string {
	argv := []string{"agent", "start", spec.Agent, "--pane", pane,
		"--timeout", strconv.FormatInt(r.cfg.StartTimeout.Milliseconds(), 10), "--kind", AgentKind, "--",
		"--permission-mode", permissionArg, "--strict-mcp-config", "--mcp-config", spec.MCPConfig}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	return append(argv, "--name", spec.SessionName())
}

func (r *Runtime) startAgent(ctx context.Context, spec runtime.RunSpec, h runtime.RunHandle) error {
	argv := r.startArgv(spec, h.Pane)
	for attempt := 1; ; attempt++ {
		_, startErr := r.herdr(ctx, h.Host, argv...)
		if startErr == nil {
			return nil
		}
		// herdr's answer can fail after the agent was created: a listed agent is started, never started twice.
		st, err := r.agentState(ctx, h.Host, h.Agent)
		if err != nil {
			return err
		}
		if st.Status != StatusGone {
			if st.Pane == h.Pane {
				return nil
			}
			return fmt.Errorf("%w: %s already lives in pane %s, not %s (tab %s left open): %w",
				runtime.ErrAgentTaken, h.Agent, st.Pane, h.Pane, h.Tab, startErr)
		}
		text, err := r.readPane(ctx, h.Host, h.Pane, TrustDialogLines)
		if err != nil {
			return err
		}
		if strings.Contains(text, TrustDialog) {
			return fmt.Errorf("%w: claude does not trust %s yet: answer the dialog in pane %s (tab %s), then close it and rerun",
				runtime.ErrTrustDialog, spec.Worktree, h.Pane, h.Tab)
		}
		if attempt >= r.cfg.StartTries {
			return fmt.Errorf("%w in pane %s after %d tries (tab %s left open): %w",
				runtime.ErrStartFailed, h.Pane, r.cfg.StartTries, h.Tab, startErr)
		}
		r.logger.WarnContext(ctx, "herdr agent start failed, retrying", "task_id", spec.TaskID, "run_id", spec.RunID,
			"project", spec.Project, "agent", h.Agent, "pane", h.Pane, "attempt", attempt, "error", startErr)
		if err := r.wait(ctx, r.cfg.StartInterval); err != nil {
			return err
		}
	}
}

// awaitWorking resends a lost paste or presses Enter until the agent works (SCENARIOS SP-6).
func (r *Runtime) awaitWorking(ctx context.Context, h runtime.RunHandle, prompt string) error {
	for attempt := 1; ; attempt++ {
		if err := r.wait(ctx, r.cfg.WorkCheckInterval); err != nil {
			return err
		}
		st, err := r.agentState(ctx, h.Host, h.Agent)
		if err != nil {
			return err
		}
		if st.Status == StatusWorking || st.Status == StatusBlocked {
			return nil
		}
		if attempt >= r.cfg.WorkChecks {
			return fmt.Errorf("%w: %s in pane %s after %d checks (status %s)",
				runtime.ErrNotWorking, h.Agent, h.Pane, r.cfg.WorkChecks, st.Status)
		}
		text, err := r.readPane(ctx, h.Host, h.Pane, InputLines)
		if err != nil {
			return err
		}
		if emptyInput.MatchString(text) {
			if err := r.Prompt(ctx, h, prompt); err != nil {
				return err
			}
		} else if err := r.enter(ctx, h); err != nil {
			return err
		}
	}
}

func (r *Runtime) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("herdr: waiting %s: %w", d, ctx.Err())
	case <-r.clock.After(d):
		return nil
	}
}

// herdr runs one herdr command on host and returns its stdout.
func (r *Runtime) herdr(ctx context.Context, host string, args ...string) ([]byte, error) {
	argv := []string{Program}
	if host != "" {
		argv = append(argv, FlagMachine, host)
	}
	res, err := r.runner.Run(ctx, runtime.Cmd{Argv: append(argv, args...)})
	if err != nil {
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args[:min(2, len(args))], " "), err)
	}
	return res.Stdout, nil
}

func decode(out []byte, v any) error {
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("%w: %s: %w", runtime.ErrOddAnswer, tail(out), err)
	}
	return nil
}

func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > AnswerTail {
		s = s[:AnswerTail]
	}
	return s
}
