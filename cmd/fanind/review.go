package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/review"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/sched"
	"github.com/polandy/fanin/internal/store"
)

// reviews is the scheduler's review seam: review.Reviewer with the project's review settings on its GitHub forge,
// in the project's checkout (SCENARIOS OC-3).
type reviews struct {
	forges *forges
	rt     *headless.Runtime
	runner runtime.Runner
	events review.Events
}

var _ sched.PRReviewer = (*reviews)(nil)

// newReviews returns the reviewer; st records each review's token usage.
func newReviews(f *forges, runner runtime.Runner, clk clock.Clock, log *slog.Logger, st *store.Store) *reviews {
	return &reviews{forges: f, rt: headless.New(runner, clk, log), runner: runner, events: storeEvents{st}}
}

// Review reviews the head of PR pr of p and posts fanin/review on it; it returns the head it reviewed.
func (r *reviews) Review(ctx context.Context, p config.Project, run model.Run, pr int) (string, error) {
	client, kind, err := r.forges.res.For(p.Name)
	if err != nil {
		return "", err
	}
	f, ok := client.(review.Forge)
	if kind != forge.KindGitHub || !ok {
		return "", fmt.Errorf("%w: project %s is on a %s forge; fanin reviews GitHub projects only, review its PR "+
			"#%d by hand", forge.ErrInvalidRequest, p.Name, kind, pr)
	}
	repo, err := r.forges.repo(ctx, p)
	if err != nil {
		return "", err
	}
	cfg := review.Config{Skill: p.ReviewSkill, Model: p.ReviewModel, Timeout: p.ReviewTimeout.Std()}
	res, err := review.New(r.rt, r.runner, f, r.events, cfg).Review(ctx, review.Request{Project: p.Name,
		TaskID: run.TaskID, RunID: run.ID, Repo: repo, Number: pr, Dir: p.Path})
	if err != nil {
		return "", err
	}
	return res.HeadSHA, nil
}

// storeEvents appends each event in its own transaction.
type storeEvents struct{ st *store.Store }

func (e storeEvents) Append(ctx context.Context, ev model.Event) error {
	return e.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendEvent(ctx, ev)
		return err
	})
}
