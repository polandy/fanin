package sched

import (
	"context"
	"fmt"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

const (
	// ReviewFailed is logged when fanin's review of a PR head fails; the next gate tick runs it again.
	ReviewFailed = "sched: fanin review failed, the next gate tick runs it again"
	// ReviewAttempts caps the failed review runs on one head; the last one hands the task to the owner.
	ReviewAttempts = 3
)

// PRReviewer runs the project's review skill on the head of PR pr and posts fanin/review from its verdict; it
// returns the head it reviewed (SCENARIOS OC-3).
type PRReviewer interface {
	Review(ctx context.Context, p config.Project, run model.Run, pr int) (string, error)
}

// reviewable reports whether res is a head without a review that fanin has not reviewed yet.
func (s *Scheduler) reviewable(id model.TaskID, res gate.Result) bool {
	return s.d.Reviewer != nil && res.Verdict == gate.Red && res.Cause == gate.CauseNoReview &&
		s.reviewed[id] != res.Head
}

// reviewFails counts the failed review runs on one head of a task.
type reviewFails struct {
	head string
	n    int
}

// review runs fanin's review of head in the background, outside the worker slots, one per task at a time. A
// posted verdict gates the PR again at once, a failed run after gate.RetryInterval; the ReviewAttempts-th failed
// run on one head asks the owner.
func (s *Scheduler) review(ctx context.Context, p config.Project, run model.Run, pr int, head string) error {
	if s.reviewing[run.TaskID] {
		return nil
	}
	// A re-gate armed up front starts the review again after a restart; one that fires mid-run starts nothing.
	if err := s.armRegate(ctx, p, run.TaskID, s.d.Clock.Now().Add(gate.RetryInterval)); err != nil {
		return err
	}
	s.reviewing[run.TaskID] = true
	log := s.logFor(p.Name, run.TaskID, run.ID)
	log.InfoContext(ctx, "sched: fanin review started", "pr", pr)
	safego.Go(ctx, log, func(ctx context.Context) {
		posted, err := s.d.Reviewer.Review(ctx, p, run, pr)
		s.post(ctx, func(ctx context.Context) {
			delete(s.reviewing, run.TaskID)
			if ctx.Err() != nil || s.gone(ctx, run.TaskID) {
				return
			}
			if err != nil {
				err = s.reviewFailed(ctx, p, run, pr, head, err)
			} else {
				delete(s.reviewFails, run.TaskID)
				s.reviewed[run.TaskID] = posted
				log.InfoContext(ctx, "sched: fanin review posted", "pr", pr, "head", posted)
				err = s.armRegate(ctx, p, run.TaskID, s.d.Clock.Now())
			}
			if err != nil {
				s.fail(ctx, err)
			}
		})
	})
	return nil
}

// reviewFailed counts a failed review run on head: the gate retries it after gate.RetryInterval, and the
// ReviewAttempts-th failure on one head moves the task to the owner instead.
func (s *Scheduler) reviewFailed(ctx context.Context, p config.Project, run model.Run, pr int, head string,
	cause error) error {
	s.logFor(p.Name, run.TaskID, run.ID).ErrorContext(ctx, ReviewFailed, "pr", pr, "head", head, "err", cause)
	f := s.reviewFails[run.TaskID]
	if f.head != head {
		f = reviewFails{head: head}
	}
	f.n++
	if f.n < ReviewAttempts {
		s.reviewFails[run.TaskID] = f
		return s.armRegate(ctx, p, run.TaskID, s.d.Clock.Now().Add(gate.RetryInterval))
	}
	delete(s.reviewFails, run.TaskID)
	return s.askOwner(ctx, p, run, fmt.Sprintf("%s#%d: fanin review of head %s failed %d of %d times; review it "+
		"by hand or post %s on the head: %v", GateErrorDetail, pr, head, f.n, ReviewAttempts, gate.ReviewContext,
		cause))
}

// armRegate arms a gate retry, so the gate reads the PR again when it is due.
func (s *Scheduler) armRegate(ctx context.Context, p config.Project, id model.TaskID, due time.Time) error {
	err := s.d.Store.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.ArmTimer(ctx, store.Timer{Project: p.Name, TaskID: id, Kind: gate.TimerRetry, DueAt: due})
		return err
	})
	if err != nil {
		return fmt.Errorf("sched: arm the re-gate of %s after its review: %w", id, err)
	}
	return nil
}
