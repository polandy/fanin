package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/forge"
)

// BranchChecksPage is the most checks BranchChecks reads; more is forge.ErrUnreadable.
const BranchChecksPage = 100

// BranchChecksQuery reads the check runs and statuses on the head of one branch, one page of BranchChecksPage.
var BranchChecksQuery = fmt.Sprintf(`query($owner: String!, $name: String!, $ref: String!) {
  repository(owner: $owner, name: $name) {
    ref(qualifiedName: $ref) {
      target {
        ... on Commit {
          statusCheckRollup {
            contexts(first: %d) {
              pageInfo { hasNextPage }
              nodes {
                __typename
                ... on CheckRun { name status conclusion startedAt completedAt }
                ... on StatusContext { context state createdAt description }
              }
            }
          }
        }
      }
    }
  }
}`, BranchChecksPage)

type branchJSON struct {
	Data struct {
		Repository *struct {
			Ref *struct {
				Target struct {
					Rollup *struct {
						Contexts struct {
							PageInfo struct {
								HasNextPage bool `json:"hasNextPage"`
							} `json:"pageInfo"`
							Nodes []checkJSON `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"target"`
			} `json:"ref"`
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
	rollup := raw.Data.Repository.Ref.Target.Rollup
	checks := []forge.Check{}
	if rollup == nil {
		return checks, nil
	}
	if rollup.Contexts.PageInfo.HasNextPage {
		return nil, fmt.Errorf("%w: %s %s has more than %d checks", forge.ErrUnreadable, repo, branch,
			BranchChecksPage)
	}
	for i, rc := range rollup.Contexts.Nodes {
		ch, err := convertCheck(rc)
		if err != nil {
			return nil, fmt.Errorf("%w: %s %s check %d: %w", forge.ErrUnreadable, repo, branch, i, err)
		}
		checks = append(checks, ch)
	}
	return checks, nil
}
