package review_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/review"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	project     = "alpha"
	repo        = "acme/widget"
	number      = 42
	repoDir     = "/path/to/repo"
	headBranch  = "feat/widget"
	headSHA     = "abc123def4567890"
	newSHA      = "fed987cba6543210"
	reviewModel = "sonnet"
	commentURL  = "https://github.com/acme/widget/pull/42#issuecomment-9"
	okJSON      = `{"is_error":false,"result":"review posted","usage":` +
		`{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":3,"cache_read_input_tokens":7}}`
	readyBody    = "## PR quality review\n\nHead " + headSHA + ".\n\n- No findings.\n\nMerge readiness: Ready"
	notReadyBody = "## PR quality review\n\n- internal/x.go:3 lacks a test.\n\n**Merge readiness**: not ready"
)

const (
	gitBin        = "git"
	reviewSkill   = "/pr-review"
	reviewTimeout = 1800 * time.Second
)

var (
	worktree   = repoDir + "/.claude/worktrees/fanin-review-42"
	fetchArgv  = []string{gitBin, "-C", repoDir, "fetch", "-q", "origin", headBranch}
	addArgv    = []string{gitBin, "-C", repoDir, "worktree", "add", "-q", "--detach", worktree, headSHA}
	removeArgv = []string{gitBin, "-C", repoDir, "worktree", "remove", "--force", worktree}
	claudeArgv = []string{"claude", "-p", "--output-format", "json", "--model", reviewModel, "--allowedTools",
		strings.Join(review.AllowedTools, ",")}
	start = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
)

func comment(id int64, body string) forge.Comment {
	return forge.Comment{ID: id, Body: body, URL: commentURL}
}

// fakeForge answers PR from its state, Comments from a script (the last answer repeats) and records statuses.
type fakeForge struct {
	mu          sync.Mutex
	pr          forge.PR
	prErr       error
	comments    [][]forge.Comment
	commentsErr error
	// commentsErrFrom is the first Comments call that fails with commentsErr.
	commentsErrFrom int
	commentCalls    int
	postErr         error
	statuses        []forge.Status
}

func newForge(comments ...[]forge.Comment) *fakeForge {
	return &fakeForge{
		pr: forge.PR{
			Repo: repo, Number: number, Title: "feat: add the widget", State: forge.StateOpen,
			Mergeable: forge.MergeableYes, HeadBranch: headBranch, HeadSHA: headSHA, BaseBranch: "main",
		},
		comments: comments,
	}
}

// PR returns the PR with a green CI check and the statuses posted on its current head.
func (f *fakeForge) PR(_ context.Context, _ string, _ int) (forge.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prErr != nil {
		return forge.PR{}, f.prErr
	}
	pr := f.pr
	pr.Checks = []forge.Check{{Name: "ci", State: forge.CheckSuccess, UpdatedAt: start}}
	for i, st := range f.statuses {
		if st.SHA != pr.HeadSHA {
			continue
		}
		state := forge.CheckFailure
		if st.State == forge.StatusSuccess {
			state = forge.CheckSuccess
		}
		pr.Checks = append(pr.Checks, forge.Check{Name: st.Context, State: state,
			UpdatedAt: start.Add(time.Duration(i+1) * time.Minute), Description: st.Description})
	}
	return pr, nil
}

func (f *fakeForge) Comments(_ context.Context, _ string, _ int) ([]forge.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commentsErr != nil && f.commentCalls >= f.commentsErrFrom {
		return nil, f.commentsErr
	}
	if len(f.comments) == 0 {
		return []forge.Comment{}, nil
	}
	i := min(f.commentCalls, len(f.comments)-1)
	f.commentCalls++
	return f.comments[i], nil
}

func (f *fakeForge) PostStatus(_ context.Context, st forge.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return f.postErr
	}
	f.statuses = append(f.statuses, st)
	return nil
}

// push moves the PR head, as a worker's fix commit does.
func (f *fakeForge) push(sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pr.HeadSHA = sha
}

func (f *fakeForge) posted() []forge.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forge.Status{}, f.statuses...)
}

// fakeEvents records the appended events.
type fakeEvents struct {
	mu     sync.Mutex
	events []model.Event
	err    error
}

func (e *fakeEvents) Append(_ context.Context, ev model.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return e.err
}

func (e *fakeEvents) all() []model.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.Event{}, e.events...)
}

// hangingClaude blocks the claude call until its context ends, like a hung review; git calls go to the script.
type hangingClaude struct {
	*testutil.RecordingRunner
	started chan struct{}
}

func (h hangingClaude) Run(ctx context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	if cmd.Argv[0] != headless.Command {
		return h.RecordingRunner.Run(ctx, cmd)
	}
	close(h.started)
	<-ctx.Done()
	return runtime.Result{}, ctx.Err()
}

type setup struct {
	runner runtime.Runner
	clock  *clock.Manual
	forge  *fakeForge
	events *fakeEvents
	cfg    review.Config
}

func newSetup(r runtime.Runner, f *fakeForge) *setup {
	return &setup{runner: r, clock: clock.NewManual(start), forge: f, events: &fakeEvents{},
		cfg: review.Config{Skill: reviewSkill, Model: reviewModel, Timeout: reviewTimeout}}
}

func (s *setup) reviewer() *review.Reviewer {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return review.New(headless.New(s.runner, s.clock, logger), s.runner, s.forge, s.events, s.cfg)
}

func request() review.Request {
	return review.Request{Project: project, TaskID: "t-1", RunID: "r-1", Repo: repo, Number: number, Dir: repoDir}
}

// headGate is the gate verdict of the PR at its current head.
func headGate(t *testing.T, f *fakeForge) gate.Result {
	t.Helper()
	pr, err := f.PR(context.Background(), repo, number)
	if err != nil {
		t.Fatalf("PR: %v", err)
	}
	return gate.GitHub(pr)
}
