package sched

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/scope"
)

// ScopeChecks posts fanin/scope on head of PR pr of p from the task's allowed paths and returns the state it
// posted (SCENARIOS SC-2).
type ScopeChecks interface {
	PostScope(ctx context.Context, p config.Project, pr int, head string, allowed []string) (forge.StatusState, error)
}

// scopeHead posts fanin/scope on the head in the background, then starts the gate from the head with that status;
// a scope that cannot be posted asks the owner and never gates (SCENARIOS SC-2).
func (s *Scheduler) scopeHead(ctx context.Context, p config.Project, run model.Run, task model.Task, pr int,
	head forge.PR) error {
	ref := "#" + strconv.Itoa(pr)
	log := s.logFor(p.Name, run.TaskID, run.ID)
	safego.Go(ctx, log, func(ctx context.Context) {
		state, err := s.d.Scopes.PostScope(ctx, p, pr, head.HeadSHA, task.AllowedPaths)
		s.post(ctx, func(ctx context.Context) {
			if ctx.Err() != nil || s.gone(ctx, run.TaskID) {
				return
			}
			if err == nil {
				log.InfoContext(ctx, "sched: fanin scope posted", "pr", pr, "head", head.HeadSHA, "state", state)
				err = s.startGate(ctx, p, run, pr, withScope(head, state, s.d.Clock.Now()))
			} else {
				err = s.askOwner(ctx, p, run, fmt.Sprintf("%s%s: %s not posted: %v", GateErrorDetail, ref,
					scope.Context, err))
			}
			if err != nil {
				s.fail(ctx, fmt.Errorf("sched: scope the head of %s of %s: %w", ref, run.TaskID, err))
			}
		})
	})
	return nil
}

// withScope returns head with the fanin/scope status just posted in place of any older one, so the gate's first
// read sees it.
func withScope(head forge.PR, state forge.StatusState, at time.Time) *forge.PR {
	checks := make([]forge.Check, 0, len(head.Checks)+1)
	for _, c := range head.Checks {
		if c.Name != scope.Context {
			checks = append(checks, c)
		}
	}
	cs := forge.CheckFailure
	if state == forge.StatusSuccess {
		cs = forge.CheckSuccess
	}
	checks = append(checks, forge.Check{Name: scope.Context, State: cs, UpdatedAt: at})
	head.Checks = checks
	return &head
}
