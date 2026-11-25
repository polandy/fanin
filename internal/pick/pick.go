// Package pick asks a short headless run which task to work on next and with which model (SCENARIOS PK-1..PK-4).
package pick

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
)

// DefaultTimeout bounds one picker run.
const DefaultTimeout = 600 * time.Second

// Forge says which forge the project lives on; it decides the picker's tools and in-progress rule.
type Forge string

// The forges a picker knows.
const (
	ForgeGitHub Forge = "github"
	ForgeGitea  Forge = "gitea"
)

// The models a picker may choose for the worker.
const (
	ModelSonnet = "sonnet"
	ModelOpus   = "opus"
)

// ErrBadPick is returned when the picker fails or its last line is no valid PICK or NOTHING line.
var ErrBadPick = errors.New("pick: no valid pick")

var (
	toolsRead   = []string{"Read", "Glob", "Grep"}
	toolsGitHub = append(append([]string{}, toolsRead...), "Bash(gh pr list:*)", "Bash(gh issue list:*)")
	pickLine    = regexp.MustCompile(`^PICK (` + ModelSonnet + `|` + ModelOpus + `): (\S.*)$`)
	nothingLine = regexp.MustCompile(`^NOTHING: (\S.*)$`)
)

// Config is the picker's settings; Model is the picker model of the worker kind's config.
type Config struct {
	Model   string
	Timeout time.Duration
}

// Request is one pick for one project.
type Request struct {
	Project string
	Forge   Forge
	// Dir is the project's repo directory, where the picker runs.
	Dir        string
	BaseBranch string
	// InProgress names the tasks that are already taken; the picker skips them.
	InProgress []string
}

// Result is a pick or an idle answer; Usage is what the picker cost either way.
type Result struct {
	// Idle is true when the picker answered NOTHING; no worker is started then.
	Idle   bool
	Reason string
	Task   string
	Model  string
	Usage  headless.Usage
}

// Picker runs the picker through a headless runtime.
type Picker struct {
	rt  *headless.Runtime
	cfg Config
}

// New returns a Picker; a zero Config.Timeout means DefaultTimeout.
func New(rt *headless.Runtime, cfg Config) *Picker {
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Picker{rt: rt, cfg: cfg}
}

// Tools returns the read-only tool rules for a forge; gh is allowed only on GitHub.
func Tools(f Forge) ([]string, error) {
	switch f {
	case ForgeGitHub:
		return append([]string{}, toolsGitHub...), nil
	case ForgeGitea:
		return append([]string{}, toolsRead...), nil
	}
	return nil, fmt.Errorf("%w: forge %q; use %q or %q", runtime.ErrInvalidSpec, f, ForgeGitHub, ForgeGitea)
}

// Pick runs the picker and reads its last line; junk, an unknown model or a timeout is ErrBadPick, never a pick.
func (p *Picker) Pick(ctx context.Context, req Request) (Result, error) {
	tools, err := Tools(req.Forge)
	if err != nil {
		return Result{}, err
	}
	if req.Dir == "" || req.BaseBranch == "" {
		return Result{}, fmt.Errorf("%w: picker request for %q needs the repo dir and the base branch", runtime.ErrInvalidSpec, req.Project)
	}
	out, err := p.rt.Run(ctx, headless.Spec{
		Model: p.cfg.Model, Prompt: Prompt(req), Dir: req.Dir, AllowedTools: tools, Timeout: p.cfg.Timeout,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: picker for %q failed: %w", ErrBadPick, req.Project, err)
	}
	res, err := parseLastLine(out.Output)
	if err != nil {
		return Result{}, fmt.Errorf("%w: picker for %q: %w; output tail: %s", ErrBadPick, req.Project, err, runtime.Tail([]byte(out.Output)))
	}
	res.Usage = out.Usage
	return res, nil
}

var errLastLine = errors.New("the last line is neither `PICK sonnet|opus: <task>` nor `NOTHING: <reason>`")

func parseLastLine(output string) (Result, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if m := pickLine.FindStringSubmatch(last); m != nil {
		return Result{Model: m[1], Task: strings.TrimSpace(m[2])}, nil
	}
	if m := nothingLine.FindStringSubmatch(last); m != nil {
		return Result{Idle: true, Reason: strings.TrimSpace(m[1])}, nil
	}
	return Result{}, errLastLine
}

// Prompt builds the picker's instructions; the in-progress rule depends on the forge.
func Prompt(req Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You pick the next task for project %s. You are read-only: change nothing.\n", req.Project)
	fmt.Fprintf(&b, "The base branch is %s. Find the next open task in this repo's todo or roadmap docs, in the order they define.\n", req.BaseBranch)
	if req.Forge == ForgeGitHub {
		b.WriteString("Skip a task that is already in progress: check `gh pr list` and `gh issue list`.\n")
	} else {
		b.WriteString("Skip a task that is already in progress: only the in-progress list below counts.\n")
	}
	if len(req.InProgress) > 0 {
		fmt.Fprintf(&b, "In progress: %s.\n", strings.Join(req.InProgress, ", "))
	}
	fmt.Fprintf(&b, "Choose %s for work that needs deep design or tricky changes, else %s.\n", ModelOpus, ModelSonnet)
	b.WriteString("End your answer with exactly one last line, nothing after it:\n")
	fmt.Fprintf(&b, "PICK %s|%s: <task>\n", ModelSonnet, ModelOpus)
	b.WriteString("or, when no open task is left:\nNOTHING: <reason>\n")
	return b.String()
}
