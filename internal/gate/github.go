package gate

import (
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

// GitHub returns the verdict of a GitHub PR as read with its head checks (see SCENARIOS GH-1 to GH-4). The review
// is the newest ReviewContext status on the head; review findings wait until CI settles.
func GitHub(pr forge.PR) Result {
	if pr.State != forge.StateOpen {
		return red(CauseClosed, fmt.Sprintf("PR is %s, not %s", pr.State, forge.StateOpen))
	}
	if pr.Mergeable == forge.MergeableConflicted {
		return red(CauseConflict, string(forge.MergeableConflicted))
	}
	var ci []forge.Check
	var review *forge.Check
	for i, c := range pr.Checks {
		if c.Name != ReviewContext {
			ci = append(ci, c)
			continue
		}
		if review == nil || c.UpdatedAt.After(review.UpdatedAt) {
			review = &pr.Checks[i]
		}
	}
	verdict := reviewReason(review, pr.HeadSHA)
	bad, running := sortChecks(ci)
	switch {
	case len(bad) > 0:
		reasons := []string{"failed: " + strings.Join(bad, ", ")}
		if verdict != "" {
			reasons = append(reasons, verdict)
		}
		return red(CauseCI, reasons...)
	case len(running) > 0:
		return pending(WaitChecks)
	case len(ci) == 0:
		return pending(WaitNoChecks)
	case pr.Mergeable != forge.MergeableYes:
		return pending(WaitMergeable)
	case review == nil:
		res := red(CauseNoReview, verdict)
		res.Head = pr.HeadSHA
		return res
	case verdict != "":
		return red(CauseReview, verdict)
	}
	return green(Green, pr.Title)
}

// reviewReason is why the review keeps the PR red, or "" when it is ready for this head.
func reviewReason(review *forge.Check, head string) string {
	switch {
	case review == nil:
		return fmt.Sprintf("no review for head %s", head)
	case review.State == forge.CheckSuccess:
		return ""
	case review.State == forge.CheckPending || review.State == forge.CheckSkipped:
		return "review has no verdict"
	case review.Description == "":
		return "review: not ready"
	}
	return "review: not ready: " + review.Description
}
