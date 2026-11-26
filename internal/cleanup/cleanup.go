// Package cleanup removes what a merged PR leaves behind: its remote branch, worktree and local branch
// (SCENARIOS MG-7).
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime"
)

// The git program and the remote it works against.
const (
	gitProgram = "git"
	remote     = "origin"
	headsRef   = "refs/heads/"
)

// The porcelain keys of `git worktree list`.
const (
	keyWorktree = "worktree "
	keyBranch   = "branch "
)

// ErrStepFailed matches a cleanup step that git refused, such as removing a dirty worktree.
var ErrStepFailed = errors.New("cleanup: step failed")

// Forge reads a PR's head branch and deletes a branch on the project's forge; cmd/fanind's forges is the real one.
type Forge interface {
	HeadBranch(ctx context.Context, p config.Project, pr int) (string, error)
	DeleteBranch(ctx context.Context, p config.Project, branch string) error
}

// Cleaner cleans up after merged PRs through a runner and a forge.
type Cleaner struct {
	r runtime.Runner
	f Forge
}

// New returns a Cleaner that runs git through r and talks to the forge through f.
func New(r runtime.Runner, f Forge) *Cleaner { return &Cleaner{r: r, f: f} }

// Cleanup deletes the head branch of merged PR pr on the forge, removes its worktree and deletes the local branch.
// A step that finds its target gone is done; every other failed step is reported in the joined error and the
// remaining steps still run. A dirty worktree stays, and with it its branch.
func (c *Cleaner) Cleanup(ctx context.Context, p config.Project, pr int) error {
	branch, err := c.f.HeadBranch(ctx, p, pr)
	if err != nil {
		return fmt.Errorf("cleanup: read the head branch of #%d of project %s: %w", pr, p.Name, err)
	}
	if branch == "" {
		return fmt.Errorf("%w: #%d of project %s has no head branch; delete its branch by hand", ErrStepFailed, pr,
			p.Name)
	}
	var errs []error
	if err := c.f.DeleteBranch(ctx, p, branch); err != nil && !errors.Is(err, forge.ErrNotFound) {
		errs = append(errs, fmt.Errorf("cleanup: delete remote branch %s of project %s: %w", branch, p.Name, err))
	}
	keep, err := c.removeWorktree(ctx, p, branch)
	if err != nil {
		errs = append(errs, err)
	}
	if _, err := c.git(ctx, p, "worktree", "prune"); err != nil {
		errs = append(errs, err)
	}
	if !keep {
		if err := c.deleteLocal(ctx, p, branch); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := c.git(ctx, p, "remote", "prune", remote); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// removeWorktree removes the worktree that has branch checked out; keep is true when it stays.
func (c *Cleaner) removeWorktree(ctx context.Context, p config.Project, branch string) (keep bool, err error) {
	res, err := c.git(ctx, p, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	tree := worktreeOf(string(res.Stdout), branch)
	if tree == "" {
		return false, nil
	}
	if filepath.Clean(tree) == filepath.Clean(p.Path) {
		return true, fmt.Errorf("%w: branch %s is checked out in the project checkout %s; switch it to %s by hand",
			ErrStepFailed, branch, p.Path, p.Base)
	}
	if _, err := c.git(ctx, p, "worktree", "remove", tree); err != nil {
		return true, err
	}
	return false, nil
}

// deleteLocal deletes the local branch; one that does not exist is done.
func (c *Cleaner) deleteLocal(ctx context.Context, p config.Project, branch string) error {
	_, err := c.git(ctx, p, "show-ref", "--verify", "--quiet", headsRef+branch)
	var exit *runtime.ExitError
	if errors.As(err, &exit) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = c.git(ctx, p, "branch", "-D", branch)
	return err
}

// git runs git in p's checkout; a failure wraps ErrStepFailed and names the command.
func (c *Cleaner) git(ctx context.Context, p config.Project, args ...string) (runtime.Result, error) {
	argv := append([]string{gitProgram, "-C", p.Path}, args...)
	res, err := c.r.Run(ctx, runtime.Cmd{Argv: argv})
	if err != nil {
		return res, fmt.Errorf("%w: project %s: git %s: %w", ErrStepFailed, p.Name, strings.Join(args, " "), err)
	}
	return res, nil
}

// worktreeOf returns the path of the worktree with branch checked out in a porcelain listing, or "".
func worktreeOf(porcelain, branch string) string {
	var tree string
	for _, line := range strings.Split(porcelain, "\n") {
		switch {
		case strings.HasPrefix(line, keyWorktree):
			tree = strings.TrimPrefix(line, keyWorktree)
		case line == keyBranch+headsRef+branch:
			return tree
		}
	}
	return ""
}
