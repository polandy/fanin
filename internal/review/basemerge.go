package review

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/runtime"
)

const (
	baseMergeFormat = "ready: only merges the base into reviewed head %s"
	// shortSHA is how many characters of a SHA the base-merge description shows.
	shortSHA = 12
	// mergeParents is the parent count of a two-way merge commit.
	mergeParents = 2
	// gitNo is the exit code of a git query that answers no: not an ancestor, or a merge with conflicts.
	gitNo = 1
)

// BaseMergeText is the fanin/review description and Result.Text of a head whose review carries over from the
// ready head reviewed.
func BaseMergeText(reviewed string) string {
	return fmt.Sprintf(baseMergeFormat, reviewed[:min(len(reviewed), shortSHA)])
}

// carriedReview returns the first parent of the PR head when the head is a merge of a commit on the base into that
// parent, without conflicts and without changes of its own, and the parent's fanin/review is ready; otherwise ""
// and the head gets a run of its own (SCENARIOS OC-3). The head branch is fetched already.
func (r *Reviewer) carriedReview(ctx context.Context, req Request, pr forge.PR) (string, error) {
	out, err := r.gitOut(ctx, req.Dir, "rev-list", "--parents", "-n", "1", pr.HeadSHA)
	if err != nil {
		return "", err
	}
	ids := strings.Fields(out)
	if len(ids) == 0 || ids[0] != pr.HeadSHA {
		return "", fmt.Errorf("%w: git rev-list --parents of %s#%d head %s answered %q; fetch the branch again",
			ErrUnreadable, req.Repo, req.Number, pr.HeadSHA, out)
	}
	if len(ids)-1 != mergeParents {
		return "", nil
	}
	reviewed, merged := ids[1], ids[2]
	if ready, err := r.ready(ctx, req.Repo, reviewed); err != nil || !ready {
		return "", err
	}
	if pr.BaseBranch == "" || strings.HasPrefix(pr.BaseBranch, "-") {
		return "", fmt.Errorf("%w: %s#%d base branch %q; re-read the PR", ErrUnreadable, req.Repo, req.Number,
			pr.BaseBranch)
	}
	if err := r.git(ctx, req.Dir, nil, "fetch", "-q", remote, pr.BaseBranch); err != nil {
		return "", err
	}
	if _, onBase, err := r.gitAsk(ctx, req.Dir, "merge-base", "--is-ancestor", merged,
		remote+"/"+pr.BaseBranch); err != nil || !onBase {
		return "", err
	}
	out, clean, err := r.gitAsk(ctx, req.Dir, "merge-tree", "--write-tree", reviewed, merged)
	if err != nil || !clean {
		return "", err
	}
	tree, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if !shaPattern.MatchString(tree) {
		return "", fmt.Errorf("%w: git merge-tree of %s and %s answered %q; check the git version (2.38 or newer)",
			ErrUnreadable, reviewed, merged, out)
	}
	headTree, err := r.gitOut(ctx, req.Dir, "rev-parse", pr.HeadSHA+"^{tree}")
	if err != nil || strings.TrimSpace(headTree) != tree {
		return "", err
	}
	return reviewed, nil
}

// ready reports whether the newest fanin/review status on sha is a success.
func (r *Reviewer) ready(ctx context.Context, repo, sha string) (bool, error) {
	checks, err := r.forge.CommitChecks(ctx, repo, sha)
	if err != nil {
		return false, fmt.Errorf("review: checks of %s@%s: %w", repo, sha, err)
	}
	var newest *forge.Check
	for i, c := range checks {
		if c.Name == gate.ReviewContext && (newest == nil || c.UpdatedAt.After(newest.UpdatedAt)) {
			newest = &checks[i]
		}
	}
	return newest != nil && newest.State == forge.CheckSuccess, nil
}

// carry posts fanin/review ready on the PR head for the ready review of its first parent reviewed.
func (r *Reviewer) carry(ctx context.Context, req Request, pr forge.PR, reviewed string) (Result, error) {
	text := BaseMergeText(reviewed)
	st := forge.Status{Repo: req.Repo, SHA: pr.HeadSHA, Context: gate.ReviewContext, State: forge.StatusSuccess,
		Description: text}
	if err := r.forge.PostStatus(ctx, st); err != nil {
		return Result{}, fmt.Errorf("review: post %s on %s@%s: %w", gate.ReviewContext, req.Repo, pr.HeadSHA, err)
	}
	return Result{HeadSHA: pr.HeadSHA, Verdict: Ready, ReviewedHead: reviewed, Text: text}, nil
}

// gitOut runs git and returns its stdout.
func (r *Reviewer) gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, yes, err := r.gitAsk(ctx, dir, args...)
	if err == nil && !yes {
		err = fmt.Errorf("review: git -C %s %s exited %d", dir, strings.Join(args, " "), gitNo)
	}
	return out, err
}

// gitAsk runs a git query: exit 0 is yes, exit gitNo is no, anything else an error.
func (r *Reviewer) gitAsk(ctx context.Context, dir string, args ...string) (string, bool, error) {
	argv := append([]string{gitProgram, "-C", dir}, args...)
	res, err := r.runner.Run(ctx, runtime.Cmd{Argv: argv})
	var exit *runtime.ExitError
	switch {
	case err == nil:
		return string(res.Stdout), true, nil
	case errors.As(err, &exit) && exit.Code == gitNo:
		return string(res.Stdout), false, nil
	default:
		return "", false, fmt.Errorf("review: %s: %w", strings.Join(argv, " "), err)
	}
}
