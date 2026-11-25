package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime"
)

var _ forge.Workflows = (*Client)(nil)

// LatestRun returns the newest run of the workflow file on sha; forge.ErrNotFound when it has none yet.
func (c *Client) LatestRun(ctx context.Context, repo, workflow, sha string) (int64, error) {
	out, err := c.gh(ctx, nil, "run", "list", "--repo", repo, "--workflow", workflow, "--commit", sha,
		"--limit", "1", "--json", "databaseId")
	if err != nil {
		return 0, err
	}
	var runs []struct {
		DatabaseID int64 `json:"databaseId"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return 0, fmt.Errorf("forge: gh run list of %s in %s: %w", workflow, repo, err)
	}
	if len(runs) == 0 {
		return 0, fmt.Errorf("%w: no run of %s in %s on %s", forge.ErrNotFound, workflow, repo, sha)
	}
	if runs[0].DatabaseID <= 0 {
		return 0, fmt.Errorf("forge: gh run list of %s in %s: run id %d", workflow, repo, runs[0].DatabaseID)
	}
	return runs[0].DatabaseID, nil
}

// WatchRun waits for the run to end; a run that did not succeed is forge.ErrRunFailed.
func (c *Client) WatchRun(ctx context.Context, repo string, id int64) error {
	argv := []string{Program, "run", "watch", strconv.FormatInt(id, 10), "--repo", repo, "--exit-status"}
	_, err := c.runner.Run(ctx, runtime.Cmd{Argv: argv})
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("forge: gh run watch %d: %w", id, ctxErr)
	}
	var exit *runtime.ExitError
	if !errors.As(err, &exit) {
		return forge.RedactError(fmt.Errorf("forge: gh run watch %d: %w", id, err))
	}
	tail := forge.Redact(exit.StderrTail)
	if containsAny(tail, networkMarkers) {
		return fmt.Errorf("%w: gh run watch %d: exit %d: %s", forge.ErrNetwork, id, exit.Code, tail)
	}
	return fmt.Errorf("%w: run %d in %s: exit %d: %s", forge.ErrRunFailed, id, repo, exit.Code, tail)
}
