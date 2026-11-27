package sched

import (
	"context"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
)

// failLog reads the failed CI step's log for the fix nudge; an unreadable log is warned about and left out, as the
// nudge still names the failed checks.
func (s *Scheduler) failLog(ctx context.Context, p config.Project, run model.Run, pr int) string {
	tail, err := s.d.FailLog.FailedLog(ctx, p, pr)
	if err != nil {
		s.logFor(p.Name, run.TaskID, run.ID).WarnContext(ctx, "sched: failed step log unreadable; nudging without it",
			"pr", pr, "err", err)
		return ""
	}
	return tail
}

// reviewHint is the review comment's text for the review nudge; an unreadable review is warned about and the gate's
// reasons stand in.
func (s *Scheduler) reviewHint(ctx context.Context, p config.Project, run model.Run, pr int, why string) string {
	if s.d.ReviewText == nil {
		return why
	}
	text, err := s.d.ReviewText.ReviewText(ctx, p, pr)
	if err != nil || text == "" {
		s.logFor(p.Name, run.TaskID, run.ID).WarnContext(ctx, "sched: review text unreadable; nudging with the gate reasons",
			"pr", pr, "err", err)
		return why
	}
	return text
}
