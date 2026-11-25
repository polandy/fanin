package hook

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/polandy/fanin/internal/runtime"
)

// The git program and the refs the pre-go check works on.
const (
	gitProgram = "git"
	remote     = "origin"
	// PreGoDir is the folder under the state directory that holds the throwaway worktrees.
	PreGoDir = "prego"
)

// The environment the pre-go check gets on top of fanind's.
const (
	EnvBase    = "FANIN_BASE"
	EnvBranch  = "FANIN_BRANCH"
	EnvBaseRef = "FANIN_BASE_REF"
)

// PreGoOutputMax is the most runes of check output that go into the owner's question.
const PreGoOutputMax = 600

// ErrPreGoFailed matches a pre-go check that ran and failed; the PR is not ready for the go.
var ErrPreGoFailed = errors.New("hook: pre-go check failed")

// PreGoRequest is one pre-go check of the PR from Branch into Base.
type PreGoRequest struct {
	Project string
	PR      int
	// Path is the project's checkout, where git fetches and adds the worktree.
	Path   string
	Base   string
	Branch string
	// Command is the project's pre_go_check; empty runs nothing.
	Command string
	// Dir is fanind's state directory; the worktree lives under Dir/PreGoDir.
	Dir string
}

// PreGo runs pre-go checks through a runner.
type PreGo struct {
	r runtime.Runner
}

// NewPreGo returns a PreGo that runs git and the check through r.
func NewPreGo(r runtime.Runner) *PreGo { return &PreGo{r: r} }

// Run fetches Base and Branch, checks the branch out in a throwaway worktree, runs the check there and removes
// the worktree. It returns the trimmed output on exit 0; a failed check is an error wrapping ErrPreGoFailed (SCENARIOS
// GT-8, FP-6).
func (g *PreGo) Run(ctx context.Context, req PreGoRequest) (string, error) {
	if req.Command == "" {
		return "", nil
	}
	if err := req.validate(); err != nil {
		return "", err
	}
	if _, err := g.git(ctx, req, "fetch", "--quiet", remote, req.Base, req.Branch); err != nil {
		return "", fmt.Errorf("hook: pre-go check of project %q #%d: fetch %s and %s: %w", req.Project, req.PR,
			req.Base, req.Branch, err)
	}
	tree := filepath.Join(req.Dir, PreGoDir, req.Project+"-"+strconv.Itoa(req.PR))
	if _, err := g.git(ctx, req, "worktree", "add", "--force", "--detach", tree, "FETCH_HEAD"); err != nil {
		return "", fmt.Errorf("hook: pre-go check of project %q #%d: add worktree %s; remove it by hand if it is left over: %w",
			req.Project, req.PR, tree, err)
	}
	res, runErr := g.r.Run(ctx, runtime.Cmd{Argv: []string{Shell, ShellFlag, req.Command}, Dir: tree, Env: []string{
		EnvProject + "=" + req.Project, EnvPR + "=" + strconv.Itoa(req.PR), EnvBase + "=" + req.Base,
		EnvBranch + "=" + req.Branch, EnvBaseRef + "=" + remote + "/" + req.Base}})
	_, rmErr := g.git(context.WithoutCancel(ctx), req, "worktree", "remove", "--force", tree)
	if rmErr != nil {
		rmErr = fmt.Errorf("hook: pre-go check of project %q #%d: remove worktree %s: %w", req.Project, req.PR, tree,
			rmErr)
	}
	switch {
	case runErr != nil && ctx.Err() != nil:
		return "", errors.Join(fmt.Errorf("hook: pre-go check of project %q #%d cut off: %w", req.Project, req.PR,
			ctx.Err()), rmErr)
	case runErr != nil:
		return "", errors.Join(fmt.Errorf("%w: project %q #%d: %s; fix pre_go_check %q", ErrPreGoFailed, req.Project,
			req.PR, tail(res, runErr), req.Command), runErr, rmErr)
	case rmErr != nil:
		return "", rmErr
	}
	return clip(string(res.Stdout)), nil
}

func (g *PreGo) git(ctx context.Context, req PreGoRequest, args ...string) (runtime.Result, error) {
	return g.r.Run(ctx, runtime.Cmd{Argv: append([]string{gitProgram, "-C", req.Path}, args...)})
}

func (req PreGoRequest) validate() error {
	switch {
	case req.Project == "":
		return fmt.Errorf("%w: no project; set PreGoRequest.Project", ErrInvalid)
	case req.PR <= 0:
		return fmt.Errorf("%w: PR %d of project %q; set PreGoRequest.PR", ErrInvalid, req.PR, req.Project)
	case req.Path == "" || req.Base == "" || req.Branch == "" || req.Dir == "":
		return fmt.Errorf("%w: project %q needs Path, Base, Branch and Dir; set them on PreGoRequest", ErrInvalid,
			req.Project)
	}
	return nil
}

// clip joins the non-empty lines of text with "; " and cuts the result at PreGoOutputMax runes.
func clip(text string) string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	out := strings.Join(lines, "; ")
	if r := []rune(out); len(r) > PreGoOutputMax {
		return string(r[:PreGoOutputMax]) + "…"
	}
	return out
}
