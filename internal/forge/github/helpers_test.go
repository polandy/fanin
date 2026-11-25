package github_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	repo    = "acme/widget"
	number  = 42
	headSHA = "abc123def456"
	token   = "ghp_0123456789abcdefghijABCDEFGHIJ012345"

	viewFields = "number,title,url,state,mergeable,headRefName,headRefOid,baseRefName,statusCheckRollup"
	netStderr  = "error connecting to api.github.com: dial tcp: i/o timeout"

	title         = "feat: add the widget"
	subject       = title + " (#42)"
	trailerBody   = "Task-Id: t-1"
	headBranch    = "feat/widget"
	baseBranch    = "main"
	flagRepo      = "--repo"
	api           = "api"
	reviewContext = "fanin/review"
	keyState      = "state"
	keyType       = "__typename"
	keyStarted    = "startedAt"
	keyCreated    = "createdAt"
	completed     = "COMPLETED"
	success       = "SUCCESS"
	doneAt        = "2026-11-25T09:05:00Z"
)

var (
	viewArgv = []string{"gh", "pr", "view", "42", flagRepo, repo, "--json", viewFields}
	start    = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
)

// prDoc is the gh pr view answer for PR 42; tests change single fields to break it.
func prDoc() map[string]any {
	return map[string]any{
		"number": number, "title": title, "url": "https://github.com/acme/widget/pull/42",
		keyState: "OPEN", "mergeable": "MERGEABLE", "headRefName": headBranch, "headRefOid": headSHA,
		"baseRefName": baseBranch,
		"statusCheckRollup": []any{
			checkRun("ci", completed, success, "2026-11-25T09:00:00Z", doneAt),
			checkRun("lint", "IN_PROGRESS", "", "2026-11-25T09:01:00Z", "0001-01-01T00:00:00Z"),
			commitStatus(reviewContext, success, keyStarted, "2026-11-25T09:06:00Z"),
		},
	}
}

// checkRun is a check run of the rollup.
func checkRun(name, status, conclusion, startedAt, completedAt string) map[string]any {
	return map[string]any{keyType: "CheckRun", "name": name, "status": status, "conclusion": conclusion,
		keyStarted: startedAt, "completedAt": completedAt}
}

// commitStatus is a commit status of the rollup; timeKey is startedAt or createdAt, empty for none.
func commitStatus(context, state, timeKey, at string) map[string]any {
	st := map[string]any{keyType: "StatusContext", "context": context, keyState: state}
	if timeKey != "" {
		st[timeKey] = at
	}
	return st
}

func withState(state string) map[string]any {
	d := prDoc()
	d[keyState] = state
	return d
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// signalClock is a manual clock that reports every After call, so a test advances it only once the client waits.
type signalClock struct {
	*clock.Manual
	waits chan time.Duration
}

func newSignalClock() *signalClock {
	return &signalClock{Manual: clock.NewManual(start), waits: make(chan time.Duration, 1)}
}

func (c *signalClock) After(d time.Duration) <-chan time.Time {
	ch := c.Manual.After(d)
	c.waits <- d
	return ch
}

func newClient(t *testing.T) (*github.Client, *testutil.RecordingRunner) {
	t.Helper()
	r := testutil.NewRecordingRunner(t)
	t.Cleanup(r.Done)
	return github.New(r, clock.NewManual(start), nil), r
}

var _ forge.Forge = (*github.Client)(nil)
