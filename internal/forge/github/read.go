package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/forge"
)

// ListLimit is the most open PRs ListOpen reads per repo.
const ListLimit = 1000

const (
	viewFields = "number,title,url,state,mergeable,headRefName,headRefOid,baseRefName,statusCheckRollup"
	listFields = "number,title,url,state,headRefName,headRefOid,baseRefName"

	typeCheckRun      = "CheckRun"
	typeStatusContext = "StatusContext"
	runCompleted      = "COMPLETED"
)

// Check run conclusions and commit status states as GitHub spells them.
var (
	conclusionStates = map[string]forge.CheckState{
		"SUCCESS": forge.CheckSuccess, "NEUTRAL": forge.CheckSuccess, "SKIPPED": forge.CheckSkipped,
		"CANCELLED": forge.CheckCancelled, "FAILURE": forge.CheckFailure, "TIMED_OUT": forge.CheckFailure,
		"ACTION_REQUIRED": forge.CheckFailure, "STARTUP_FAILURE": forge.CheckFailure, "STALE": forge.CheckFailure,
	}
	contextStates = map[string]forge.CheckState{
		"SUCCESS": forge.CheckSuccess, "PENDING": forge.CheckPending, "EXPECTED": forge.CheckPending,
		"FAILURE": forge.CheckFailure, "ERROR": forge.CheckFailure,
	}
	prStates   = []forge.PRState{forge.StateOpen, forge.StateClosed, forge.StateMerged}
	mergeables = []forge.Mergeable{forge.MergeableYes, forge.MergeableConflicted, forge.MergeableUnknown}
)

type prJSON struct {
	Number      int         `json:"number"`
	Title       string      `json:"title"`
	URL         string      `json:"url"`
	State       string      `json:"state"`
	Mergeable   string      `json:"mergeable"`
	HeadRefName string      `json:"headRefName"`
	HeadRefOid  string      `json:"headRefOid"`
	BaseRefName string      `json:"baseRefName"`
	Rollup      []checkJSON `json:"statusCheckRollup"`
}

type checkJSON struct {
	Type        string `json:"__typename"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt"`
	Context     string `json:"context"`
	State       string `json:"state"`
	CreatedAt   string `json:"createdAt"`
	Description string `json:"description"`
}

// PR reads one pull request; any missing or unknown field is forge.ErrUnreadable (see SCENARIOS GH-5).
func (c *Client) PR(ctx context.Context, repo string, number int) (forge.PR, error) {
	out, err := c.gh(ctx, nil, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", viewFields)
	if err != nil {
		return forge.PR{}, err
	}
	var raw prJSON
	if err := decodeStrict(out, &raw); err != nil {
		return forge.PR{}, fmt.Errorf("%w: gh pr view %s#%d: %w", forge.ErrUnreadable, repo, number, err)
	}
	if raw.Number != number || raw.Mergeable == "" {
		return forge.PR{}, fmt.Errorf("%w: gh pr view %s#%d answered PR %d mergeable %q", forge.ErrUnreadable, repo,
			number, raw.Number, raw.Mergeable)
	}
	pr, err := convertPR(repo, raw)
	if err != nil {
		return forge.PR{}, err
	}
	pr.Checks = make([]forge.Check, 0, len(raw.Rollup))
	for i, rc := range raw.Rollup {
		ch, err := convertCheck(rc)
		if err != nil {
			return forge.PR{}, fmt.Errorf("%w: %s#%d check %d: %w", forge.ErrUnreadable, repo, number, i, err)
		}
		pr.Checks = append(pr.Checks, ch)
	}
	return pr, nil
}

// ListOpen lists the open pull requests of repo, without checks.
func (c *Client) ListOpen(ctx context.Context, repo string) ([]forge.PR, error) {
	out, err := c.gh(ctx, nil, "pr", "list", "--repo", repo, "--state", "open",
		"--limit", strconv.Itoa(ListLimit), "--json", listFields)
	if err != nil {
		return nil, err
	}
	var raws []prJSON
	if err := decodeStrict(out, &raws); err != nil {
		return nil, fmt.Errorf("%w: gh pr list %s: %w", forge.ErrUnreadable, repo, err)
	}
	prs := make([]forge.PR, 0, len(raws))
	for _, raw := range raws {
		pr, err := convertPR(repo, raw)
		if err != nil {
			return nil, err
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// Files lists every path the pull request changes, paginated through the REST API.
func (c *Client) Files(ctx context.Context, repo string, number int) ([]string, error) {
	out, err := c.gh(ctx, nil, "api", "--paginate", fmt.Sprintf("repos/%s/pulls/%d/files", repo, number),
		"--jq", ".[].filename")
	if err != nil {
		return nil, err
	}
	files := []string{}
	for line := range strings.Lines(string(out)) {
		path := strings.TrimRight(line, "\r\n")
		if path == "" {
			return nil, fmt.Errorf("%w: files of %s#%d: empty path in the answer", forge.ErrUnreadable, repo, number)
		}
		files = append(files, path)
	}
	return files, nil
}

// decodeStrict decodes one JSON value and rejects empty output and trailing data.
func decodeStrict(out []byte, v any) error {
	if len(bytes.TrimSpace(out)) == 0 {
		return errors.New("empty output")
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// convertPR checks the fields every PR answer carries (see SCENARIOS GH-14: the head branch must be readable).
func convertPR(repo string, raw prJSON) (forge.PR, error) {
	state, okState := oneOf(raw.State, prStates)
	pr := forge.PR{
		Repo: repo, Number: raw.Number, Title: raw.Title, URL: raw.URL, State: state,
		HeadBranch: raw.HeadRefName, HeadSHA: raw.HeadRefOid, BaseBranch: raw.BaseRefName,
	}
	if raw.Mergeable != "" {
		m, ok := oneOf(raw.Mergeable, mergeables)
		if !ok {
			return forge.PR{}, fmt.Errorf("%w: %s#%d mergeable %q", forge.ErrUnreadable, repo, raw.Number, raw.Mergeable)
		}
		pr.Mergeable = m
	}
	switch {
	case raw.Number <= 0:
		return forge.PR{}, fmt.Errorf("%w: %s: PR number %d", forge.ErrUnreadable, repo, raw.Number)
	case !okState:
		return forge.PR{}, fmt.Errorf("%w: %s#%d state %q", forge.ErrUnreadable, repo, raw.Number, raw.State)
	case raw.HeadRefName == "" || raw.HeadRefOid == "" || raw.BaseRefName == "":
		return forge.PR{}, fmt.Errorf("%w: %s#%d head branch %q sha %q base %q", forge.ErrUnreadable, repo, raw.Number,
			raw.HeadRefName, raw.HeadRefOid, raw.BaseRefName)
	}
	return pr, nil
}

func convertCheck(rc checkJSON) (forge.Check, error) {
	switch rc.Type {
	case typeCheckRun:
		if rc.Name == "" {
			return forge.Check{}, errors.New("check run without a name")
		}
		if rc.Status != runCompleted {
			started, err := parseTime(rc.StartedAt, false)
			return forge.Check{Name: rc.Name, State: forge.CheckPending, UpdatedAt: started}, err
		}
		state, ok := conclusionStates[rc.Conclusion]
		if !ok {
			return forge.Check{}, fmt.Errorf("check run %q conclusion %q", rc.Name, rc.Conclusion)
		}
		done, err := parseTime(rc.CompletedAt, true)
		if err != nil {
			return forge.Check{}, fmt.Errorf("check run %q completedAt: %w", rc.Name, err)
		}
		return forge.Check{Name: rc.Name, State: state, UpdatedAt: done}, nil
	case typeStatusContext:
		state, ok := contextStates[rc.State]
		if rc.Context == "" || !ok {
			return forge.Check{}, fmt.Errorf("status %q state %q", rc.Context, rc.State)
		}
		at := rc.StartedAt
		if at == "" {
			at = rc.CreatedAt
		}
		// The review verdict is a status; its time decides which head it counts for (see SCENARIOS GH-5).
		posted, err := parseTime(at, true)
		if err != nil {
			return forge.Check{}, fmt.Errorf("status %q time: %w", rc.Context, err)
		}
		return forge.Check{Name: rc.Context, State: state, UpdatedAt: posted, Description: rc.Description}, nil
	default:
		return forge.Check{}, fmt.Errorf("unknown check type %q", rc.Type)
	}
}

// parseTime reads an RFC 3339 time; gh prints the zero time for a field that is not set yet.
func parseTime(s string, required bool) (time.Time, error) {
	if s == "" && !required {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q: %w", s, err)
	}
	if required && t.IsZero() {
		return time.Time{}, fmt.Errorf("time %q is not set", s)
	}
	return t, nil
}

func oneOf[T ~string](s string, all []T) (T, bool) {
	for _, v := range all {
		if string(v) == s {
			return v, true
		}
	}
	return "", false
}
