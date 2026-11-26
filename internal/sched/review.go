package sched

import (
	"context"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
)

// ReviewFailed is logged when fanin's review of a PR head fails; the next gate tick runs it again.
const ReviewFailed = "sched: fanin review failed, the next gate tick runs it again"

// PRReviewer runs the project's review skill on the head of PR pr and posts fanin/review from its verdict; it
// returns the head it reviewed (SCENARIOS OC-3).
type PRReviewer interface {
	Review(ctx context.Context, p config.Project, run model.Run, pr int) (string, error)
}
