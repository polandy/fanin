package sched

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

// The opencode delivery's names (SCENARIOS OC-1): the sandbox remote in the project's checkout and the PR body.
const (
	// OCRemote is the git remote of the project's checkout that points at the sandbox clone.
	OCRemote = "aiagent"
	// OCBranchPrefix leads every opencode branch.
	OCBranchPrefix = "oc/"
	// OCReviewerName is who reviews an opencode PR.
	OCReviewerName = "opencode in the sandbox (model in its comment)"
	// OCPRBody is the body of an opencode PR; it names the reviewer.
	OCPRBody = "Built by an opencode worker in the sandbox. Reviewed by " + OCReviewerName + "."
)

// The opencode delivery's errors; match them with errors.Is.
var (
	// ErrOCBranch is a committed branch without the oc/ prefix or with odd characters.
	ErrOCBranch = errors.New("sched: not an opencode branch")
	// ErrOCForge is an opencode project on a forge other than GitHub.
	ErrOCForge = errors.New("sched: opencode runs for GitHub projects only")
	// ErrOCRemote is a checkout without the sandbox remote.
	ErrOCRemote = errors.New("sched: no sandbox remote")
	// ErrOCFetch is a branch the sandbox clone does not have.
	ErrOCFetch = errors.New("sched: cannot fetch from the sandbox clone")
	// ErrOCPush is a push the forge refused; it is never forced.
	ErrOCPush = errors.New("sched: push refused")
)

var ocBranchPattern = regexp.MustCompile(`^oc/[A-Za-z0-9._-]+$`)

// OCPR is the PR an opencode branch was delivered to.
type OCPR struct {
	Repo   string
	Number int
}

// OCDelivery pushes an opencode branch and opens or reuses its PR; OCPush is the real one (SCENARIOS OC-1).
type OCDelivery interface {
	Deliver(ctx context.Context, p config.Project, branch string) (OCPR, error)
}

// OCReviewer reviews an opencode PR in the sandbox and posts its comment and status (SCENARIOS OC-4).
type OCReviewer interface {
	Review(ctx context.Context, p config.Project, pr OCPR) error
}

// committed delivers and reviews an opencode branch in the background, then gates it as a done_pr (SCENARIOS
// OC-1, OC-2, OC-6).
func (s *Scheduler) committed(ctx context.Context, p config.Project, task model.Task, run model.Run,
	res model.Result) error {
	if kind := cmp.Or(task.WorkerKind, p.WorkerKind); kind != intake.KindOpencode {
		return s.askOwner(ctx, p, run, fmt.Sprintf("%s%s: COMMITTED from a %s worker; only %s workers commit "+
			"without a PR", GateErrorDetail, res.Branch, kind, intake.KindOpencode))
	}
	if s.d.OCDeliver == nil || s.d.OCReview == nil {
		return s.askOwner(ctx, p, run, fmt.Sprintf("%s%s: no opencode delivery or review is configured; push and "+
			"review the branch by hand", GateErrorDetail, res.Branch))
	}
	log := s.logFor(p.Name, run.TaskID, run.ID)
	safego.Go(ctx, log, func(ctx context.Context) {
		pr, err := s.d.OCDeliver.Deliver(ctx, p, res.Branch)
		if err == nil {
			err = s.d.OCReview.Review(ctx, p, pr)
		}
		s.post(ctx, func(ctx context.Context) {
			if err != nil {
				err = s.askOwner(ctx, p, run, fmt.Sprintf("%s%s: %v", GateErrorDetail, res.Branch, err))
			} else {
				err = s.delivered(ctx, run, res, pr)
			}
			if err != nil {
				log.ErrorContext(ctx, "sched: opencode delivery not recorded", "err", err)
			}
		})
	})
	return nil
}

// delivered records the PR as the run's done_pr, so the PR path gates it; a task that moved on is left alone.
func (s *Scheduler) delivered(ctx context.Context, run model.Run, res model.Result, pr OCPR) error {
	return s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		task, err := tx.Task(ctx, run.TaskID)
		if err != nil || task.State != model.TaskRunning {
			return err
		}
		return tx.SetRunResult(ctx, run.ID, model.Result{Kind: model.ResultDonePR, PR: pr.Number, Branch: res.Branch,
			Text: res.Text})
	})
}

// OCForge is what the delivery reads from and opens on the forge.
type OCForge interface {
	ListOpen(ctx context.Context, repo string) ([]forge.PR, error)
	Open(ctx context.Context, req forge.OpenRequest) (int, error)
}

// OCPush fetches an opencode branch from the sandbox remote, pushes it fast-forward only and opens or reuses its PR.
type OCPush struct {
	Runner runtime.Runner
	Forge  OCForge
	// Forges are the configured forges by name; Repo resolves a project's repo slug.
	Forges map[string]config.Forge
	Repo   func(p config.Project) (string, error)
}

// Deliver pushes branch to origin and returns its open PR, opening it with the branch's subject when none is open.
func (o OCPush) Deliver(ctx context.Context, p config.Project, branch string) (OCPR, error) {
	if !ocBranchPattern.MatchString(branch) {
		return OCPR{}, fmt.Errorf("%w: %q; an opencode worker commits on %s<slug> (letters, digits, . _ -)",
			ErrOCBranch, branch, OCBranchPrefix)
	}
	if kind := o.Forges[p.Forge].Kind; kind != config.ForgeGitHub {
		return OCPR{}, fmt.Errorf("%w: project %s has forge %q of kind %q; run its tasks with a Claude worker",
			ErrOCForge, p.Name, p.Forge, kind)
	}
	if _, err := o.git(ctx, p, "remote", "get-url", OCRemote); err != nil {
		return OCPR{}, fmt.Errorf("%w %s in %s; add it: git remote add %s <sandbox clone>: %w", ErrOCRemote,
			OCRemote, p.Path, OCRemote, err)
	}
	ref := "refs/remotes/" + OCRemote + "/" + branch
	if _, err := o.git(ctx, p, "fetch", "-q", OCRemote, "+refs/heads/"+branch+":"+ref); err != nil {
		return OCPR{}, fmt.Errorf("%w: cannot fetch %s from the sandbox clone; check the worker's branch name: %w",
			ErrOCFetch, branch, err)
	}
	if _, err := o.git(ctx, p, "push", "-q", "origin", ref+":refs/heads/"+branch); err != nil {
		return OCPR{}, fmt.Errorf("%w: push of %s refused (it is never forced); the worker must not rewrite its "+
			"branch: %w", ErrOCPush, branch, err)
	}
	repo, err := o.Repo(p)
	if err != nil {
		return OCPR{}, fmt.Errorf("sched: repo of project %s: %w", p.Name, err)
	}
	open, err := o.Forge.ListOpen(ctx, repo)
	if err != nil {
		return OCPR{}, fmt.Errorf("sched: list the open PRs of %s: %w", repo, err)
	}
	for _, pr := range open {
		if pr.HeadBranch == branch {
			return OCPR{Repo: repo, Number: pr.Number}, nil
		}
	}
	subject, err := o.git(ctx, p, "log", "-1", "--format=%s", ref)
	if err != nil {
		return OCPR{}, fmt.Errorf("sched: subject of %s: %w", branch, err)
	}
	n, err := o.Forge.Open(ctx, forge.OpenRequest{Repo: repo, Head: branch, Base: p.Base,
		Title: strings.TrimSpace(subject), Body: OCPRBody})
	if err != nil {
		return OCPR{}, fmt.Errorf("sched: open the PR of %s on %s: %w", branch, repo, err)
	}
	return OCPR{Repo: repo, Number: n}, nil
}

func (o OCPush) git(ctx context.Context, p config.Project, args ...string) (string, error) {
	res, err := o.Runner.Run(ctx, runtime.Cmd{Argv: append([]string{"git", "-C", p.Path}, args...)})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}
