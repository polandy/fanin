package sched

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/runtime"
)

// ErrNoBranch is a run whose branch is not recorded, so its unpushed commits cannot be listed.
var ErrNoBranch = errors.New("sched: run branch not recorded")

// ErrNoScan is a scheduler without an Unpushed; it counts as unknown unpushed commits.
var ErrNoScan = errors.New("sched: no unpushed scan configured")

// Unpushed lists the commits of one branch that no remote has; *GitUnpushed is the real one.
type Unpushed interface {
	Unpushed(ctx context.Context, p config.Project, branch string) ([]string, error)
}

// GitUnpushed asks git in the project's checkout, through the runner.
type GitUnpushed struct {
	Runner runtime.Runner
}

// Unpushed lists "<hash> <subject>" of every commit on branch that no remote-tracking ref holds (SCENARIOS
// AW-12): the scan covers only this branch.
func (g GitUnpushed) Unpushed(ctx context.Context, p config.Project, branch string) ([]string, error) {
	if branch == "" {
		return nil, ErrNoBranch
	}
	res, err := g.Runner.Run(ctx, runtime.Cmd{Argv: []string{"git", "-C", p.Path, "log", "--format=%h %s",
		"refs/heads/" + branch, "--not", "--remotes", "--"}})
	if err != nil {
		return nil, fmt.Errorf("sched: list the unpushed commits of %s in %s: %w", branch, p.Path, err)
	}
	var out []string
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}
