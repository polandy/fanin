package gitea

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/polandy/fanin/internal/forge"
)

const stateOpen, stateClosed = "open", "closed"

// statusStates maps Gitea's commit status states; warning counts as failure, so it never passes a gate.
var statusStates = map[string]forge.CheckState{
	"pending": forge.CheckPending, "running": forge.CheckPending, "success": forge.CheckSuccess,
	"skipped": forge.CheckSkipped, "failure": forge.CheckFailure, "error": forge.CheckFailure,
	"warning": forge.CheckFailure,
}

type refJSON struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type prJSON struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	HTMLURL   string  `json:"html_url"`
	State     string  `json:"state"`
	Merged    bool    `json:"merged"`
	Mergeable *bool   `json:"mergeable"`
	Head      refJSON `json:"head"`
	Base      refJSON `json:"base"`
}

type statusJSON struct {
	Context     string `json:"context"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Description string `json:"description"`
}

type combinedJSON struct {
	TotalCount int          `json:"total_count"`
	Statuses   []statusJSON `json:"statuses"`
}

type fileJSON struct {
	Filename string `json:"filename"`
}

// PR reads one pull request and the commit statuses of its head. An unreadable PR is an error; unreadable
// statuses are an error wrapping forge.ErrChecksUnreadable, which a gate reads as pending (see SCENARIOS GT-6).
func (c *Client) PR(ctx context.Context, repo string, number int) (forge.PR, error) {
	pr, err := c.pull(ctx, repo, number)
	if err != nil {
		return forge.PR{}, err
	}
	checks, err := c.statuses(ctx, repo, pr.HeadSHA)
	if err != nil {
		return forge.PR{}, fmt.Errorf("%w: %s#%d head %s: %w", forge.ErrChecksUnreadable, repo, number, pr.HeadSHA, err)
	}
	pr.Checks = checks
	return pr, nil
}

// pull reads one pull request without its statuses.
func (c *Client) pull(ctx context.Context, repo string, number int) (forge.PR, error) {
	base, err := prPath(repo, number)
	if err != nil {
		return forge.PR{}, err
	}
	var raw prJSON
	if err := c.do(ctx, call{method: http.MethodGet, path: base, out: &raw}); err != nil {
		return forge.PR{}, err
	}
	if raw.Number != number {
		return forge.PR{}, fmt.Errorf("%w: GET %s answered PR %d", forge.ErrUnreadable, base, raw.Number)
	}
	return toPR(repo, raw)
}

// Files lists the paths the pull request changes; an empty path is forge.ErrUnreadable.
func (c *Client) Files(ctx context.Context, repo string, number int) ([]string, error) {
	base, err := prPath(repo, number)
	if err != nil {
		return nil, err
	}
	raw, err := pages[fileJSON](ctx, c, base+"/files", nil)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(raw))
	for _, f := range raw {
		if f.Filename == "" {
			return nil, fmt.Errorf("%w: GET %s/files answered an empty filename", forge.ErrUnreadable, base)
		}
		files = append(files, f.Filename)
	}
	return files, nil
}

// ListOpen lists the open pull requests without their statuses.
func (c *Client) ListOpen(ctx context.Context, repo string) ([]forge.PR, error) {
	base, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	raw, err := pages[prJSON](ctx, c, base+"/pulls", url.Values{"state": {stateOpen}})
	if err != nil {
		return nil, err
	}
	prs := make([]forge.PR, 0, len(raw))
	for _, r := range raw {
		pr, err := toPR(repo, r)
		if err != nil {
			return nil, err
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// statuses reads the latest commit status per context of sha.
func (c *Client) statuses(ctx context.Context, repo, sha string) ([]forge.Check, error) {
	base, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	path := base + "/commits/" + url.PathEscape(sha) + "/status"
	checks := []forge.Check{}
	for page := 1; page <= MaxPages; page++ {
		var raw combinedJSON
		if err := c.do(ctx, call{method: http.MethodGet, path: path, query: pageQuery(page, nil), out: &raw}); err != nil {
			return nil, err
		}
		for _, s := range raw.Statuses {
			ch, err := toCheck(s)
			if err != nil {
				return nil, fmt.Errorf("%w: GET %s: %w", forge.ErrUnreadable, path, err)
			}
			checks = append(checks, ch)
		}
		if len(raw.Statuses) < PageLimit || len(checks) >= raw.TotalCount {
			return checks, nil
		}
	}
	return nil, fmt.Errorf("%w: GET %s: more than %d pages of %d", forge.ErrUnreadable, path, MaxPages, PageLimit)
}

func toCheck(s statusJSON) (forge.Check, error) {
	state, ok := statusStates[s.Status]
	if s.Context == "" || !ok {
		return forge.Check{}, fmt.Errorf("status context %q state %q", s.Context, s.Status)
	}
	at := s.UpdatedAt
	if at == "" {
		at = s.CreatedAt
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return forge.Check{}, fmt.Errorf("status %s time %q: %w", s.Context, at, err)
	}
	return forge.Check{Name: s.Context, State: state, UpdatedAt: t.UTC(), Description: s.Description}, nil
}

// toPR maps a Gitea PR; mergeable false counts as conflicting, as gitea-gate.sh did.
func toPR(repo string, r prJSON) (forge.PR, error) {
	var state forge.PRState
	switch {
	case r.State == stateOpen && !r.Merged:
		state = forge.StateOpen
	case r.State == stateClosed && r.Merged:
		state = forge.StateMerged
	case r.State == stateClosed:
		state = forge.StateClosed
	default:
		return forge.PR{}, fmt.Errorf("%w: %s#%d state %q merged %t", forge.ErrUnreadable, repo, r.Number, r.State, r.Merged)
	}
	if r.Number <= 0 || r.Head.Ref == "" || r.Head.SHA == "" || r.Base.Ref == "" || r.Mergeable == nil {
		return forge.PR{}, fmt.Errorf("%w: %s#%d head %q sha %q base %q mergeable set %t", forge.ErrUnreadable, repo,
			r.Number, r.Head.Ref, r.Head.SHA, r.Base.Ref, r.Mergeable != nil)
	}
	mergeable := forge.MergeableConflicted
	if *r.Mergeable {
		mergeable = forge.MergeableYes
	}
	return forge.PR{
		Repo: repo, Number: r.Number, Title: r.Title, URL: r.HTMLURL, State: state, Mergeable: mergeable,
		HeadBranch: r.Head.Ref, HeadSHA: r.Head.SHA, BaseBranch: r.Base.Ref,
	}, nil
}

// prPath is /repos/{owner}/{name}/pulls/{number}; a number below 1 fails before any call.
func prPath(repo string, number int) (string, error) {
	base, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	if number <= 0 {
		return "", fmt.Errorf("%w: PR number %d of %s; use the number the forge gave", forge.ErrInvalidRequest, number, repo)
	}
	return base + "/pulls/" + strconv.Itoa(number), nil
}
