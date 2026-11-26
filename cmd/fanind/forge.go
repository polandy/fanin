package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/doctor"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/gitea"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/sched"
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
	_ sched.PRGates = (*forges)(nil)
	_ sched.BaseCI  = (*forges)(nil)
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
