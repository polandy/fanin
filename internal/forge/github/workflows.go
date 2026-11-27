package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime"
)

var (
	_ forge.Workflows = (*Client)(nil)
	_ forge.FailLogs  = (*Client)(nil)
)

const (
	failLogRuns   = "20"
	conclFailure  = "failure"
	failLogFields = "databaseId,conclusion"
)

// FailedLog returns the redacted tail of the failed steps' log of the newest failed run on sha.
func (c *Client) FailedLog(ctx context.Context, repo, sha string, max int) (string, error) {
	out, err := c.gh(ctx, nil, "run", "list", "--repo", repo, "--commit", sha, "--limit", failLogRuns,
		"--json", failLogFields)
	if err != nil {
		return "", err
	}
	var runs []struct {
		DatabaseID int64  `json:"databaseId"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(out, &runs); err != nil {
		return "", fmt.Errorf("forge: gh run list of %s on %s: %w", repo, sha, err)
	}
	for _, r := range runs {
		if r.Conclusion != conclFailure {
			continue
		}
		if r.DatabaseID <= 0 {
			return "", fmt.Errorf("forge: gh run list of %s on %s: run id %d", repo, sha, r.DatabaseID)
		}
		log, err := c.gh(ctx, nil, "run", "view", strconv.FormatInt(r.DatabaseID, 10), "--repo", repo, "--log-failed")
		if err != nil {
			return "", err
		}
		return forge.Redact(tailOf(string(log), max)), nil
	}
	return "", fmt.Errorf("%w: no failed run in %s on %s", forge.ErrNotFound, repo, sha)
}

// tailOf keeps at most max bytes from the end of s, never splitting a rune.
func tailOf(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	for !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

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
