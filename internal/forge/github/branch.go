package github

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

// shaPattern is a commit SHA CommitChecks passes to gh.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// BranchChecksPage is the most checks BranchChecks reads; more is forge.ErrUnreadable.
const BranchChecksPage = 100

// rollupFields is one page of BranchChecksPage of a commit's check runs and statuses.
var rollupFields = fmt.Sprintf(`statusCheckRollup {
  contexts(first: %d) {
    pageInfo { hasNextPage }
    nodes {
      __typename
      ... on CheckRun { name status conclusion startedAt completedAt }
      ... on StatusContext { context state createdAt description }
    }
  }
}`, BranchChecksPage)

// BranchChecksQuery reads the check runs and statuses on the head of one branch, one page of BranchChecksPage.
var BranchChecksQuery = `query($owner: String!, $name: String!, $ref: String!) {
  repository(owner: $owner, name: $name) {
    ref(qualifiedName: $ref) { target { ... on Commit { ` + rollupFields + ` } } }
  }
}`

// CommitChecksQuery reads the check runs and statuses on one commit, one page of BranchChecksPage.
var CommitChecksQuery = `query($owner: String!, $name: String!, $oid: GitObjectID!) {
  repository(owner: $owner, name: $name) {
    object(oid: $oid) { ... on Commit { ` + rollupFields + ` } }
  }
}`

// rollupJSON is one commit's statusCheckRollup.
type rollupJSON struct {
	Contexts struct {
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []checkJSON `json:"nodes"`
	} `json:"contexts"`
}

type branchJSON struct {
	Data struct {
		Repository *struct {
			Ref *struct {
				Target struct {
					Rollup *rollupJSON `json:"statusCheckRollup"`
				} `json:"target"`
			} `json:"ref"`
		} `json:"repository"`
	} `json:"data"`
}

type commitJSON struct {
	Data struct {
		Repository *struct {
			Object *struct {
				Rollup *rollupJSON `json:"statusCheckRollup"`
			} `json:"object"`
		} `json:"repository"`
	} `json:"data"`
}

// BranchChecks reads the checks on the head of branch; a missing branch is forge.ErrNotFound (see SCENARIOS MG-8).
func (c *Client) BranchChecks(ctx context.Context, repo, branch string) ([]forge.Check, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || branch == "" {
		return nil, fmt.Errorf("%w: checks of repo %q branch %q; pass owner/name and a branch", forge.ErrInvalidRequest,
			repo, branch)
	}
	out, err := c.gh(ctx, nil, "api", "graphql", "-f", "query="+BranchChecksQuery, "-f", "owner="+owner,
		"-f", "name="+name, "-f", "ref=refs/heads/"+branch)
	if err != nil {
		return nil, err
	}
	var raw branchJSON
	if err := decodeStrict(out, &raw); err != nil {
		return nil, fmt.Errorf("%w: checks of %s %s: %w", forge.ErrUnreadable, repo, branch, err)
	}
	switch repoRef := raw.Data.Repository; {
	case repoRef == nil:
		return nil, fmt.Errorf("%w: checks of %s %s: answer without a repository", forge.ErrUnreadable, repo, branch)
	case repoRef.Ref == nil:
		return nil, fmt.Errorf("%w: branch %s of %s; push it or fix base in the project file", forge.ErrNotFound,
			branch, repo)
	}
	return convertRollup(raw.Data.Repository.Ref.Target.Rollup, repo+" "+branch)
}

// CommitChecks reads the checks on commit sha; an unknown commit is forge.ErrNotFound (see SCENARIOS OC-3).
func (c *Client) CommitChecks(ctx context.Context, repo, sha string) ([]forge.Check, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || !shaPattern.MatchString(sha) {
		return nil, fmt.Errorf("%w: checks of repo %q commit %q; pass owner/name and a hex SHA",
			forge.ErrInvalidRequest, repo, sha)
	}
	out, err := c.gh(ctx, nil, "api", "graphql", "-f", "query="+CommitChecksQuery, "-f", "owner="+owner,
		"-f", "name="+name, "-f", "oid="+sha)
	if err != nil {
		return nil, err
	}
	var raw commitJSON
	if err := decodeStrict(out, &raw); err != nil {
		return nil, fmt.Errorf("%w: checks of %s@%s: %w", forge.ErrUnreadable, repo, sha, err)
	}
	switch repoRef := raw.Data.Repository; {
	case repoRef == nil:
		return nil, fmt.Errorf("%w: checks of %s@%s: answer without a repository", forge.ErrUnreadable, repo, sha)
	case repoRef.Object == nil:
		return nil, fmt.Errorf("%w: commit %s of %s; fetch and push it, or re-read the PR", forge.ErrNotFound, sha,
			repo)
	}
	return convertRollup(raw.Data.Repository.Object.Rollup, repo+"@"+sha)
}

// convertRollup converts one page of a rollup of what; no rollup is no checks.
func convertRollup(rollup *rollupJSON, what string) ([]forge.Check, error) {
	checks := []forge.Check{}
	if rollup == nil {
		return checks, nil
	}
	if rollup.Contexts.PageInfo.HasNextPage {
		return nil, fmt.Errorf("%w: %s has more than %d checks", forge.ErrUnreadable, what, BranchChecksPage)
	}
	for i, rc := range rollup.Contexts.Nodes {
		ch, err := convertCheck(rc)
		if err != nil {
			return nil, fmt.Errorf("%w: %s check %d: %w", forge.ErrUnreadable, what, i, err)
		}
		checks = append(checks, ch)
	}
	return checks, nil
}
