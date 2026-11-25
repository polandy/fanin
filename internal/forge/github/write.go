package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

const (
	pullPathMarker = "/pull/"
	commentMarker  = "#issuecomment-"
)

// Open opens a pull request; the body goes through stdin, so its length and quoting never matter.
func (c *Client) Open(ctx context.Context, req forge.OpenRequest) (int, error) {
	if req.Repo == "" || req.Head == "" || req.Base == "" || req.Title == "" {
		return 0, fmt.Errorf("%w: open PR repo %q head %q base %q title %q; set all four", forge.ErrInvalidRequest,
			req.Repo, req.Head, req.Base, req.Title)
	}
	out, err := c.gh(ctx, []byte(req.Body), "pr", "create", "--repo", req.Repo, "--head", req.Head,
		"--base", req.Base, "--title", req.Title, "--body-file", "-")
	if err != nil {
		return 0, err
	}
	url := strings.TrimSpace(string(out))
	_, num, found := strings.Cut(url, pullPathMarker)
	n, convErr := strconv.Atoi(num)
	if !found || convErr != nil || n <= 0 {
		return 0, fmt.Errorf("%w: gh pr create %s answered %q, want a PR URL", forge.ErrUnreadable, req.Repo, url)
	}
	return n, nil
}

// PostComment posts body as a comment on PR number and returns the comment's URL.
func (c *Client) PostComment(ctx context.Context, repo string, number int, body string) (string, error) {
	if repo == "" || number <= 0 || strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("%w: comment on repo %q PR %d with a %d-byte body; set all three", forge.ErrInvalidRequest,
			repo, number, len(body))
	}
	out, err := c.gh(ctx, []byte(body), "pr", "comment", strconv.Itoa(number), "--repo", repo, "--body-file", "-")
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(string(out))
	if !strings.Contains(url, pullPathMarker) || !strings.Contains(url, commentMarker) {
		return "", fmt.Errorf("%w: gh pr comment %s#%d answered %q, want a comment URL", forge.ErrUnreadable, repo,
			number, url)
	}
	return url, nil
}

// EnableAutoMerge sets auto-merge with the squash message; GitHub merges once branch protection is green.
func (c *Client) EnableAutoMerge(ctx context.Context, req forge.MergeRequest) error {
	if err := validMerge(req); err != nil {
		return err
	}
	_, err := c.gh(ctx, nil, mergeArgs(req, "--auto")...)
	return err
}

// PostStatus posts a commit status on st.SHA.
func (c *Client) PostStatus(ctx context.Context, st forge.Status) error {
	if st.Repo == "" || st.SHA == "" || st.Context == "" || !slices.Contains(forge.AllStatusStates, st.State) {
		return fmt.Errorf("%w: status repo %q sha %q context %q state %q; set all four, state one of %v",
			forge.ErrInvalidRequest, st.Repo, st.SHA, st.Context, st.State, forge.AllStatusStates)
	}
	args := []string{"api", "--method", "POST", fmt.Sprintf("repos/%s/statuses/%s", st.Repo, st.SHA),
		"-f", "state=" + string(st.State), "-f", "context=" + st.Context, "-f", "description=" + st.Description}
	if st.TargetURL != "" {
		args = append(args, "-f", "target_url="+st.TargetURL)
	}
	_, err := c.gh(ctx, nil, args...)
	return err
}

// SquashMerge merges now. It reads the PR first and stops on a bad view; a network failure is retried after each
// of MergeRetryDelays, and the PR is checked for MERGED before every retry (see SCENARIOS MG-6).
func (c *Client) SquashMerge(ctx context.Context, req forge.MergeRequest) error {
	if err := validMerge(req); err != nil {
		return err
	}
	merged, err := c.merged(ctx, req)
	if err != nil || merged {
		return err
	}
	for attempt := 0; ; attempt++ {
		_, err = c.gh(ctx, nil, mergeArgs(req)...)
		if err == nil || !errors.Is(err, forge.ErrNetwork) || attempt == len(MergeRetryDelays) {
			return err
		}
		delay := MergeRetryDelays[attempt]
		c.log.WarnContext(ctx, "forge: merge failed on the network, retrying", "repo", req.Repo, "pr", req.Number,
			"attempt", attempt+1, "delay", delay, "err", err)
		if err := c.wait(ctx, delay); err != nil {
			return fmt.Errorf("forge: merge %s#%d: %w", req.Repo, req.Number, err)
		}
		if merged, err = c.merged(ctx, req); err != nil || merged {
			return err
		}
	}
}

// merged reads the PR: true when it is merged, an error when it is closed or its view is unreadable.
func (c *Client) merged(ctx context.Context, req forge.MergeRequest) (bool, error) {
	pr, err := c.PR(ctx, req.Repo, req.Number)
	if err != nil {
		return false, fmt.Errorf("forge: merge %s#%d: view before merge: %w", req.Repo, req.Number, err)
	}
	switch pr.State {
	case forge.StateMerged:
		return true, nil
	case forge.StateOpen:
		return false, nil
	default:
		return false, fmt.Errorf("%w: merge %s#%d: state %s; reopen it or drop the task", forge.ErrNotOpen,
			req.Repo, req.Number, pr.State)
	}
}

// DeleteBranch deletes refs/heads/branch; a branch that is already gone is forge.ErrNotFound.
func (c *Client) DeleteBranch(ctx context.Context, repo, branch string) error {
	if repo == "" || branch == "" {
		return fmt.Errorf("%w: delete branch repo %q branch %q; set both", forge.ErrInvalidRequest, repo, branch)
	}
	_, err := c.gh(ctx, nil, "api", "--method", "DELETE", fmt.Sprintf("repos/%s/git/refs/heads/%s", repo, branch))
	return err
}

func validMerge(req forge.MergeRequest) error {
	if req.Repo == "" || req.Number <= 0 || req.Subject == "" || req.HeadSHA == "" {
		return fmt.Errorf("%w: merge repo %q PR %d subject %q head %q; build it with forge.NewSquash",
			forge.ErrInvalidRequest, req.Repo, req.Number, req.Subject, req.HeadSHA)
	}
	return nil
}

func mergeArgs(req forge.MergeRequest, extra ...string) []string {
	args := []string{"pr", "merge", strconv.Itoa(req.Number), "--repo", req.Repo, "--squash"}
	args = append(args, extra...)
	return append(args, "--subject", req.Subject, "--body", req.Body, "--match-head-commit", req.HeadSHA)
}
