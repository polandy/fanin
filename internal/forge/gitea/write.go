package gitea

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

// Footer ends the body of every PR OpenBranch opens.
const Footer = "Opened by fanin."

const mergeStyleSquash = "squash"

// branchPattern is the branch names OpenBranch accepts (see SCENARIOS GT-1).
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

type openJSON struct {
	Head  string `json:"head"`
	Base  string `json:"base"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type mergeJSON struct {
	Do                     string `json:"do"`
	MergeTitleField        string `json:"merge_title_field"`
	MergeMessageField      string `json:"merge_message_field"`
	HeadCommitID           string `json:"head_commit_id"`
	DeleteBranchAfterMerge bool   `json:"delete_branch_after_merge"`
	MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed,omitempty"`
}

type statusPostJSON struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url,omitempty"`
}

type compareJSON struct {
	Commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commits"`
}

// Open opens a pull request and returns its number.
func (c *Client) Open(ctx context.Context, req forge.OpenRequest) (int, error) {
	base, err := repoPath(req.Repo)
	if err != nil {
		return 0, err
	}
	if req.Head == "" || req.Base == "" || req.Title == "" {
		return 0, fmt.Errorf("%w: open PR repo %q head %q base %q title %q; set all four", forge.ErrInvalidRequest,
			req.Repo, req.Head, req.Base, req.Title)
	}
	var raw prJSON
	body := openJSON{Head: req.Head, Base: req.Base, Title: req.Title, Body: req.Body}
	if err := c.do(ctx, call{method: http.MethodPost, path: base + "/pulls", body: body, out: &raw}); err != nil {
		return 0, err
	}
	if raw.Number <= 0 {
		return 0, fmt.Errorf("%w: POST %s/pulls answered PR %d", forge.ErrUnreadable, base, raw.Number)
	}
	return raw.Number, nil
}

// OpenBranch opens the PR for a branch a worker pushed, or reuses the open one for it; reused says which. The
// title is the oldest commit's subject, the body lists every commit and ends with Footer. An odd or base branch
// is refused, and when the open PRs cannot be listed no PR is opened (see SCENARIOS GT-1).
func (c *Client) OpenBranch(ctx context.Context, repo, base, branch string) (number int, reused bool, err error) {
	if base == "" || branch == base || !branchPattern.MatchString(branch) || strings.Contains(branch, "..") ||
		strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return 0, false, fmt.Errorf("%w: refusing branch %q against base %q; push a feature branch named <type>/<slug>",
			forge.ErrInvalidRequest, branch, base)
	}
	open, err := c.ListOpen(ctx, repo)
	if err != nil {
		return 0, false, fmt.Errorf("forge: open PR for %s %s: cannot list the open PRs: %w", repo, branch, err)
	}
	if i := slices.IndexFunc(open, func(p forge.PR) bool { return p.HeadBranch == branch }); i >= 0 {
		return open[i].Number, true, nil
	}
	messages, err := c.commits(ctx, repo, base, branch)
	if err != nil {
		return 0, false, err
	}
	title, body := describe(messages)
	n, err := c.Open(ctx, forge.OpenRequest{Repo: repo, Head: branch, Base: base, Title: title, Body: body})
	return n, false, err
}

// commits returns the commit messages of branch beyond base, oldest first; Gitea lists them newest first.
func (c *Client) commits(ctx context.Context, repo, base, branch string) ([]string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var raw compareJSON
	path := rp + "/compare/" + refPath(base) + "..." + refPath(branch)
	if err := c.do(ctx, call{method: http.MethodGet, path: path, out: &raw}); err != nil {
		return nil, fmt.Errorf("forge: cannot read the commits of %s beyond %s: %w", branch, base, err)
	}
	if len(raw.Commits) == 0 {
		return nil, fmt.Errorf("%w: %s has no commits beyond %s; push the work first", forge.ErrInvalidRequest, branch, base)
	}
	messages := make([]string, 0, len(raw.Commits))
	for _, cm := range slices.Backward(raw.Commits) {
		if strings.TrimSpace(cm.Commit.Message) == "" {
			return nil, fmt.Errorf("%w: GET %s: commit %s has no message", forge.ErrUnreadable, path, cm.SHA)
		}
		messages = append(messages, cm.Commit.Message)
	}
	return messages, nil
}

// describe builds the PR title and body from commit messages, oldest first.
func describe(messages []string) (title, body string) {
	var b strings.Builder
	for i, m := range messages {
		subject, rest, _ := strings.Cut(strings.TrimSpace(m), "\n")
		subject = strings.TrimSpace(subject)
		if i == 0 {
			title = subject
		}
		fmt.Fprintf(&b, "- %s\n", subject)
		if rest = strings.TrimSpace(rest); rest != "" {
			fmt.Fprintf(&b, "\n%s\n\n", rest)
		}
	}
	return title, strings.TrimRight(b.String(), "\n") + "\n\n" + Footer
}

// EnableAutoMerge sets "merge when checks succeed" with the squash message; the forge merges once branch
// protection is green and deletes the branch.
func (c *Client) EnableAutoMerge(ctx context.Context, req forge.MergeRequest) error {
	return c.merge(ctx, req, true)
}

// SquashMerge merges now and deletes the branch. It reads the PR first: a merged PR is success, a closed one is
// forge.ErrNotOpen, and an unreadable view stops before the merge (see SCENARIOS MG-6).
func (c *Client) SquashMerge(ctx context.Context, req forge.MergeRequest) error {
	if err := validMerge(req); err != nil {
		return err
	}
	pr, err := c.pull(ctx, req.Repo, req.Number)
	if err != nil {
		return fmt.Errorf("forge: merge %s#%d: view before merge: %w", req.Repo, req.Number, err)
	}
	switch pr.State {
	case forge.StateMerged:
		return nil
	case forge.StateOpen:
		return c.merge(ctx, req, false)
	default:
		return fmt.Errorf("%w: merge %s#%d: state %s; reopen it or drop the task", forge.ErrNotOpen, req.Repo,
			req.Number, pr.State)
	}
}

func (c *Client) merge(ctx context.Context, req forge.MergeRequest, whenGreen bool) error {
	if err := validMerge(req); err != nil {
		return err
	}
	base, err := prPath(req.Repo, req.Number)
	if err != nil {
		return err
	}
	body := mergeJSON{Do: mergeStyleSquash, MergeTitleField: req.Subject, MergeMessageField: req.Body,
		HeadCommitID: req.HeadSHA, DeleteBranchAfterMerge: true, MergeWhenChecksSucceed: whenGreen}
	return c.do(ctx, call{method: http.MethodPost, path: base + "/merge", body: body})
}

// PostStatus posts a commit status on st.SHA.
func (c *Client) PostStatus(ctx context.Context, st forge.Status) error {
	base, err := repoPath(st.Repo)
	if err != nil {
		return err
	}
	if st.SHA == "" || st.Context == "" || !slices.Contains(forge.AllStatusStates, st.State) {
		return fmt.Errorf("%w: status repo %q sha %q context %q state %q; set all four, state one of %v",
			forge.ErrInvalidRequest, st.Repo, st.SHA, st.Context, st.State, forge.AllStatusStates)
	}
	body := statusPostJSON{State: string(st.State), Context: st.Context, Description: st.Description, TargetURL: st.TargetURL}
	return c.do(ctx, call{method: http.MethodPost, path: base + "/statuses/" + url.PathEscape(st.SHA), body: body})
}

// DeleteBranch deletes a branch; one that is already gone is forge.ErrNotFound.
func (c *Client) DeleteBranch(ctx context.Context, repo, branch string) error {
	base, err := repoPath(repo)
	if err != nil {
		return err
	}
	if branch == "" {
		return fmt.Errorf("%w: delete branch of %s: empty branch; set it", forge.ErrInvalidRequest, repo)
	}
	return c.do(ctx, call{method: http.MethodDelete, path: base + "/branches/" + refPath(branch)})
}

func validMerge(req forge.MergeRequest) error {
	if req.Repo == "" || req.Number <= 0 || req.Subject == "" || req.HeadSHA == "" {
		return fmt.Errorf("%w: merge repo %q PR %d subject %q head %q; build it with forge.NewSquash",
			forge.ErrInvalidRequest, req.Repo, req.Number, req.Subject, req.HeadSHA)
	}
	return nil
}
