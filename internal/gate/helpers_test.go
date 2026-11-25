package gate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/store"
)

const (
	repo    = "acme/widget"
	number  = 42
	title   = "feat: add the widget"
	headSHA = "abc123def456"
	base    = "main"
	project = "alpha"
	taskID  = "t-1"
	docFile = "docs/a.md"

	waitTimer = "gate_wait+30m0s"
	pollTimer = "gate_poll+1m0s"
)

var start = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

var (
	errBroken = errors.New("broken JSON")
)

// read is one forge answer of the fake.
type read struct {
	pr  forge.PR
	err error
}

// fakeForge answers PR from a queue (the last answer repeats) and Files from fixed values.
type fakeForge struct {
	reads    []read
	files    []string
	filesErr error
	prCalls  int
	fileCall int
}

func (f *fakeForge) PR(_ context.Context, gotRepo string, gotNumber int) (forge.PR, error) {
	if gotRepo != repo || gotNumber != number {
		return forge.PR{}, forge.ErrNotFound
	}
	i := min(f.prCalls, len(f.reads)-1)
	f.prCalls++
	return f.reads[i].pr, f.reads[i].err
}

func (f *fakeForge) Files(context.Context, string, int) ([]string, error) {
	f.fileCall++
	return f.files, f.filesErr
}

func answer(pr forge.PR) *fakeForge { return &fakeForge{reads: []read{{pr: pr}}} }

func failing(err error) *fakeForge { return &fakeForge{reads: []read{{err: err}}} }

// fakeTimers records the armed timers, or fails every arm with err.
type fakeTimers struct {
	armed []store.Timer
	err   error
}

func (f *fakeTimers) ArmTimer(_ context.Context, tm store.Timer) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.armed = append(f.armed, tm)
	return tm.Kind + "-id", nil
}

// openPR is an open, mergeable PR whose CI and review passed on its head.
func openPR(checks ...forge.Check) forge.PR {
	if checks == nil {
		checks = []forge.Check{check("ci", forge.CheckSuccess, 0), review(forge.CheckSuccess, 1, "")}
	}
	return forge.PR{Repo: repo, Number: number, Title: title, State: forge.StateOpen, Mergeable: forge.MergeableYes,
		HeadBranch: "feat/widget", HeadSHA: headSHA, BaseBranch: base, Checks: checks}
}

func check(name string, state forge.CheckState, minute int) forge.Check {
	return forge.Check{Name: name, State: state, UpdatedAt: start.Add(time.Duration(minute) * time.Minute)}
}

func review(state forge.CheckState, minute int, text string) forge.Check {
	c := check(gate.ReviewContext, state, minute)
	c.Description = text
	return c
}

func githubTarget() gate.Target {
	return gate.Target{Kind: forge.KindGitHub, Repo: repo, Number: number, Base: base}
}

func giteaTarget() gate.Target {
	return gate.Target{Kind: forge.KindGitea, Repo: repo, Number: number, Base: base}
}

// limits are the schema defaults of the gate section (see SCENARIOS GH-7, GT-4).
func limits() config.Gate {
	return config.Gate{
		GitHub: config.GitHubGate{WatchTimeout: config.Duration(30 * time.Minute),
			Settle: config.Duration(20 * time.Second), MaxWaits: 12, MaxWatchFailures: 6},
		Gitea: config.GiteaGate{PollInterval: config.Duration(60 * time.Second), NociAfterPolls: 5, MaxPolls: 40},
	}
}

func mustRead(t *testing.T, f *fakeForge, tg gate.Target) gate.Result {
	t.Helper()
	got, err := gate.Read(context.Background(), f, tg)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return got
}

// pendingFor is the PENDING result of wait w.
func pendingFor(w gate.Wait) gate.Result {
	return gate.Result{Verdict: gate.Pending, Wait: w, Reasons: []string{string(w)}}
}

// redFor is the RED result with reasons.
func redFor(cause gate.Cause, reasons ...string) gate.Result {
	return gate.Result{Verdict: gate.Red, Cause: cause, Reasons: reasons}
}
