package gate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

// readGitea reads a Gitea PR and its files. Unreadable statuses or files are PENDING, never GREEN; an unreadable
// PR is an error (see SCENARIOS GT-6).
func readGitea(ctx context.Context, r Reader, t Target) (Result, error) {
	pr, err := r.PR(ctx, t.Repo, t.Number)
	if errors.Is(err, forge.ErrChecksUnreadable) {
		return pending(WaitChecksUnreadable), nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("%w: PR %s#%d: %w", ErrUnreadable, t.Repo, t.Number, err)
	}
	res := Gitea(pr, t.Base)
	if res.Verdict != Green {
		return res, nil
	}
	files, err := r.Files(ctx, t.Repo, t.Number)
	if err != nil {
		return pending(WaitFilesUnreadable), nil //nolint:nilerr // unreadable files are PENDING (SCENARIOS GT-6)
	}
	if DocsOnly(files) {
		return green(GreenDocs, pr.Title), nil
	}
	return green(GreenCode, pr.Title), nil
}

// Gitea returns the verdict of a Gitea PR aimed at base, all RED reasons together (see SCENARIOS GT-3 to GT-5).
// GREEN here means checks passed; the caller sorts it into GREEN docs or GREEN code by the files.
func Gitea(pr forge.PR, base string) Result {
	var reasons []string
	if pr.State != forge.StateOpen {
		reasons = append(reasons, fmt.Sprintf("PR is %s, not %s", pr.State, forge.StateOpen))
	}
	if pr.BaseBranch != base {
		reasons = append(reasons, fmt.Sprintf("PR aims at %s, not %s", pr.BaseBranch, base))
	}
	if pr.Mergeable == forge.MergeableConflicted {
		reasons = append(reasons, string(forge.MergeableConflicted))
	}
	bad, running := sortChecks(pr.Checks)
	if len(bad) > 0 {
		reasons = append(reasons, "failed: "+strings.Join(bad, ", "))
	}
	switch {
	case len(reasons) > 0:
		return red(reasons...)
	case len(running) > 0:
		return pending(WaitChecks)
	case len(pr.Checks) == 0:
		return pending(WaitNoChecks)
	case pr.Mergeable != forge.MergeableYes:
		return pending(WaitMergeable)
	}
	return green(Green, pr.Title)
}
