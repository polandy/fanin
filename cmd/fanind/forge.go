package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/cleanup"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/doctor"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/gitea"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/review"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/sched"
	"github.com/polandy/fanin/internal/scope"
)

// GiteaTimeout bounds one call to a Gitea or Forgejo API.
const GiteaTimeout = 30 * time.Second

// forges is the scheduler's forge seam: the gate of each PR and the checks of each base branch of the projects
// with loop: fanin. The repo is read from the origin of the project's checkout on every call.
type forges struct {
	res    *forge.Resolver
	runner runtime.Runner
}

var (
	_ sched.PRGates     = (*forges)(nil)
	_ sched.BaseCI      = (*forges)(nil)
	_ sched.FailLogs    = (*forges)(nil)
	_ sched.Merger      = (*forges)(nil)
	_ sched.ReviewTexts = (*forges)(nil)
	_ cleanup.Forge     = (*forges)(nil)
)

// newForges builds a client for each forge a loop: fanin project names; a Gitea forge without its token is an
// error wrapping forge.ErrNoSecret.
func newForges(cfg config.Config, runner runtime.Runner, getenv func(string) string, clk clock.Clock,
	log *slog.Logger) (*forges, error) {
	named := map[string]forge.Named{}
	projects := map[string]string{}
	lookup := func(k string) (string, bool) { v := getenv(k); return v, v != "" }
	for _, p := range cfg.Projects {
		if p.Loop != config.LoopFanin {
			continue
		}
		projects[p.Name] = p.Forge
		if _, ok := named[p.Forge]; ok {
			continue
		}
		f, ok := cfg.Forges[p.Forge]
		if !ok {
			continue
		}
		switch forge.Kind(f.Kind) {
		case forge.KindGitHub:
			named[p.Forge] = forge.Named{Kind: forge.KindGitHub, Client: github.New(runner, clk, log)}
		case forge.KindGitea:
			token, err := forge.SecretFromEnv(f.TokenEnv, lookup)
			if err != nil {
				return nil, fmt.Errorf("forge %q of project %s: %w", p.Forge, p.Name, err)
			}
			c, err := gitea.New(&http.Client{Timeout: GiteaTimeout}, f.URL, token, log)
			if err != nil {
				return nil, fmt.Errorf("forge %q of project %s: %w", p.Forge, p.Name, err)
			}
			named[p.Forge] = forge.Named{Kind: forge.KindGitea, Client: c}
		}
	}
	res, err := forge.NewResolver(named, projects)
	if err != nil {
		return nil, fmt.Errorf("fix forges.yaml: %w", err)
	}
	return &forges{res: res, runner: runner}, nil
}

// For returns the forge reader and gate target of PR pr of p.
func (f *forges) For(ctx context.Context, p config.Project, pr int) (gate.Reader, gate.Target, error) {
	client, kind, err := f.res.For(p.Name)
	if err != nil {
		return nil, gate.Target{}, err
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return nil, gate.Target{}, err
	}
	return client, gate.Target{Kind: kind, Repo: repo, Number: pr, Base: p.Base}, nil
}

// BaseChecks reads the checks on the head of p's base branch (SCENARIOS MG-8).
func (f *forges) BaseChecks(ctx context.Context, p config.Project) ([]forge.Check, error) {
	client, _, err := f.res.For(p.Name)
	if err != nil {
		return nil, err
	}
	bc, ok := client.(forge.BranchChecks)
	if !ok {
		return nil, fmt.Errorf("%w: forge %q of %s cannot read branch checks", forge.ErrInvalidRequest, p.Forge, p.Name)
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return nil, err
	}
	return bc.BranchChecks(ctx, repo, p.Base)
}

// FailedLog reads the tail of the failed CI step's log on the head of PR pr of p (SCENARIOS GH-9).
func (f *forges) FailedLog(ctx context.Context, p config.Project, pr int) (string, error) {
	client, _, err := f.res.For(p.Name)
	if err != nil {
		return "", err
	}
	fl, ok := client.(forge.FailLogs)
	if !ok {
		return "", fmt.Errorf("%w: forge %q of %s cannot read CI logs", forge.ErrInvalidRequest, p.Forge, p.Name)
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return "", err
	}
	head, err := client.PR(ctx, repo, pr)
	if err != nil {
		return "", err
	}
	return fl.FailedLog(ctx, repo, head.HeadSHA, forge.FailLogMax)
}

// ReviewText reads the latest review comment of PR pr of p, cut to review.TextLimit (SCENARIOS GH-10).
func (f *forges) ReviewText(ctx context.Context, p config.Project, pr int) (string, error) {
	client, _, err := f.res.For(p.Name)
	if err != nil {
		return "", err
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return "", err
	}
	rf, ok := client.(review.Forge)
	if !ok {
		return "", fmt.Errorf("%w: forge %q of %s cannot read PR comments", forge.ErrInvalidRequest, p.Forge, p.Name)
	}
	comments, err := rf.Comments(ctx, repo, pr)
	if err != nil {
		return "", err
	}
	return review.LatestText(comments)
}

// Merge squash-merges PR pr of p with the subject "<title> (#n)"; a PR already merged is success, and the forge
// retries a network failure only after checking for MERGED (SCENARIOS MG-6).
func (f *forges) Merge(ctx context.Context, p config.Project, pr int) error {
	client, _, err := f.res.For(p.Name)
	if err != nil {
		return err
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return err
	}
	view, err := client.PR(ctx, repo, pr)
	if err != nil {
		return fmt.Errorf("read %s#%d before its merge: %w", repo, pr, err)
	}
	if view.State == forge.StateMerged {
		return nil
	}
	req, err := forge.NewSquash(view, nil)
	if err != nil {
		return err
	}
	return client.SquashMerge(ctx, req)
}

// HeadBranch reads the head branch of PR pr of p; a merged PR keeps it.
func (f *forges) HeadBranch(ctx context.Context, p config.Project, pr int) (string, error) {
	client, _, err := f.res.For(p.Name)
	if err != nil {
		return "", err
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return "", err
	}
	view, err := client.PR(ctx, repo, pr)
	if err != nil {
		return "", fmt.Errorf("read %s#%d for its head branch: %w", repo, pr, err)
	}
	return view.HeadBranch, nil
}

// DeleteBranch deletes branch on p's forge; one already gone is forge.ErrNotFound.
func (f *forges) DeleteBranch(ctx context.Context, p config.Project, branch string) error {
	client, _, err := f.res.For(p.Name)
	if err != nil {
		return err
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return err
	}
	return client.DeleteBranch(ctx, repo, branch)
}

// repo reads owner/name from the origin remote of p's checkout.
func (f *forges) repo(ctx context.Context, p config.Project) (string, error) {
	res, err := f.runner.Run(ctx, runtime.Cmd{Argv: []string{"git", "-C", p.Path, "remote", "get-url", "origin"}})
	if err != nil {
		return "", fmt.Errorf("read the origin of %s at %s: %w; set origin to the forge URL of the repo", p.Name,
			p.Path, err)
	}
	repo, err := doctor.ParseRepo(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		return "", fmt.Errorf("origin of %s at %s: %w", p.Name, p.Path, err)
	}
	return repo, nil
}

// PostScope posts fanin/scope on head of PR pr of p from the task's allowed paths (SCENARIOS SC-2).
func (f *forges) PostScope(ctx context.Context, p config.Project, pr int, head string, allowed []string) (
	forge.StatusState, error) {
	client, _, err := f.res.For(p.Name)
	if err != nil {
		return "", err
	}
	repo, err := f.repo(ctx, p)
	if err != nil {
		return "", err
	}
	return scope.Post(ctx, client, repo, pr, head, allowed)
}
