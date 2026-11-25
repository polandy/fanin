// Package review runs the configured review skill headless on a PR head and posts its verdict as the
// fanin/review commit status on that head (concept D5, SCENARIOS OC-3 and OC-5). An unreadable review is an
// error, never a verdict.
package review

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
)

// The review run's limits (SCENARIOS OC-3, OC-5); the defaults of its settings live in the config schema.
const (
	// TextLimit is the most characters of the review text a Result carries, for the worker's nudge.
	TextLimit = 1500
	// MaxTries is the first run plus one retry with CorrectionNote.
	MaxTries = 2
	// LFSSkipEnv keeps LFS files as pointers: the review reads code, not binaries.
	LFSSkipEnv = "GIT_LFS_SKIP_SMUDGE=1"
)

// The descriptions of the fanin/review status; the comment it links to carries the findings.
const (
	DescriptionReady    = "ready"
	DescriptionNotReady = "not ready: see the review comment"
)

// CorrectionNote opens the prompt addition of a retry.
const CorrectionNote = "Your previous review comment was not used"

const (
	gitProgram     = "git"
	remote         = "origin"
	worktreeDir    = ".claude/worktrees"
	worktreePrefix = "fanin-review-"
	sudoMarker     = "a password is required"
)

// AllowedTools are read-only tools plus the one write the skill needs, the verdict comment.
var AllowedTools = []string{
	"Read", "Grep", "Glob", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)",
	"Bash(gh pr view:*)", "Bash(gh pr diff:*)", "Bash(gh pr checks:*)", "Bash(gh pr comment:*)",
}

// The sentinels of a review that gives no verdict; match them with errors.Is.
var (
	// ErrUnreadable is a PR head or a review comment fanin cannot read a verdict from.
	ErrUnreadable = errors.New("review: unreadable review")
	// ErrNoComment is a run that posted or edited no comment on the PR.
	ErrNoComment = errors.New("review: no review comment")
	// ErrSudoPassword is a run that stopped at a sudo password prompt.
	ErrSudoPassword = errors.New("review: the run asked for a sudo password")
)

var (
	shaPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	lfsEnv     = []string{LFSSkipEnv}
)

// Forge is what a review reads from and posts to the forge.
type Forge interface {
	PR(ctx context.Context, repo string, number int) (forge.PR, error)
	Comments(ctx context.Context, repo string, number int) ([]forge.Comment, error)
	PostStatus(ctx context.Context, st forge.Status) error
}

// Events records what a review cost.
type Events interface {
	Append(ctx context.Context, ev model.Event) error
}

// Config is the project's review settings (review_skill, review_model, review_timeout); all three are required.
type Config struct {
	Skill   string
	Model   string
	Timeout time.Duration
}

// Request is the review of one PR of one project.
type Request struct {
	Project string
	TaskID  model.TaskID
	RunID   model.RunID
	Repo    string
	Number  int
	// Dir is the project's clone; the review's worktree goes below it.
	Dir string
}

// Result is a posted verdict.
type Result struct {
	// HeadSHA is the head the review saw and the status is on.
	HeadSHA    string
	Verdict    Verdict
	CommentURL string
	// Text is the canonical review on one line, cut to TextLimit characters.
	Text  string
	Usage headless.Usage
}

// Reviewer runs reviews through a headless runtime and git through a runtime.Runner.
type Reviewer struct {
	rt     *headless.Runtime
	runner runtime.Runner
	forge  Forge
	events Events
	cfg    Config
}

// New returns a Reviewer; Review checks cfg.
func New(rt *headless.Runtime, runner runtime.Runner, f Forge, ev Events, cfg Config) *Reviewer {
	return &Reviewer{rt: rt, runner: runner, forge: f, events: ev, cfg: cfg}
}

// Worktree is the path of the detached worktree a review of PR number runs in.
func Worktree(dir string, number int) string {
	return filepath.Join(dir, worktreeDir, worktreePrefix+strconv.Itoa(number))
}

// Review runs the skill on the PR head in a detached worktree, reads the verdict from the comment it posted and
// posts it as the fanin/review status on that head. The worktree is removed either way; nothing is posted on error.
func (r *Reviewer) Review(ctx context.Context, req Request) (Result, error) {
	if err := r.validate(req); err != nil {
		return Result{}, err
	}
	pr, err := r.head(ctx, req)
	if err != nil {
		return Result{}, err
	}
	before, err := r.forge.Comments(ctx, req.Repo, req.Number)
	if err != nil {
		return Result{}, fmt.Errorf("review: comments of %s#%d before the run: %w", req.Repo, req.Number, err)
	}
	wt := Worktree(req.Dir, req.Number)
	if err := r.addWorktree(ctx, req.Dir, wt, pr); err != nil {
		return Result{}, err
	}
	res, err := r.run(ctx, req, pr, wt, before)
	if err == nil {
		err = r.post(ctx, req, pr, res)
	}
	err = errors.Join(err, r.removeWorktree(ctx, req.Dir, wt), r.recordUsage(ctx, req, res.Usage))
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func (r *Reviewer) validate(req Request) error {
	switch {
	case req.Project == "" || req.Repo == "" || req.Number <= 0 || req.Dir == "":
		return fmt.Errorf("%w: review of project %q repo %q PR %d dir %q; pass all four", runtime.ErrInvalidSpec,
			req.Project, req.Repo, req.Number, req.Dir)
	case r.cfg.Skill == "" || r.cfg.Model == "" || r.cfg.Timeout <= 0:
		return fmt.Errorf("%w: review of %q has skill %q, model %q, timeout %s; set review_skill, review_model "+
			"and a positive review_timeout", runtime.ErrInvalidSpec, req.Project, r.cfg.Skill, r.cfg.Model, r.cfg.Timeout)
	}
	return nil
}

// head reads the PR and refuses a head fanin cannot pass to git safely.
func (r *Reviewer) head(ctx context.Context, req Request) (forge.PR, error) {
	pr, err := r.forge.PR(ctx, req.Repo, req.Number)
	if err != nil {
		return forge.PR{}, fmt.Errorf("review: read %s#%d: %w", req.Repo, req.Number, err)
	}
	switch {
	case pr.State != forge.StateOpen:
		return forge.PR{}, fmt.Errorf("%w: %s#%d is %s; only an open PR is reviewed", forge.ErrNotOpen, req.Repo,
			req.Number, pr.State)
	case !shaPattern.MatchString(pr.HeadSHA):
		return forge.PR{}, fmt.Errorf("%w: %s#%d head SHA %q is no hex SHA; re-read the PR", ErrUnreadable,
			req.Repo, req.Number, pr.HeadSHA)
	case pr.HeadBranch == "" || strings.HasPrefix(pr.HeadBranch, "-"):
		return forge.PR{}, fmt.Errorf("%w: %s#%d head branch %q; re-read the PR", ErrUnreadable, req.Repo,
			req.Number, pr.HeadBranch)
	}
	return pr, nil
}

func (r *Reviewer) git(ctx context.Context, dir string, env []string, args ...string) error {
	argv := append([]string{gitProgram, "-C", dir}, args...)
	if _, err := r.runner.Run(ctx, runtime.Cmd{Argv: argv, Env: env}); err != nil {
		return fmt.Errorf("review: %s: %w", strings.Join(argv, " "), err)
	}
	return nil
}

func (r *Reviewer) addWorktree(ctx context.Context, dir, wt string, pr forge.PR) error {
	if err := r.git(ctx, dir, nil, "fetch", "-q", remote, pr.HeadBranch); err != nil {
		return err
	}
	if err := r.git(ctx, dir, lfsEnv, "worktree", "add", "-q", "--detach", wt, pr.HeadSHA); err != nil {
		return fmt.Errorf("%w; a stale worktree goes with git -C %s worktree remove --force %s", err, dir, wt)
	}
	return nil
}

// removeWorktree runs even when ctx is cancelled, so a shutdown leaves no worktree behind.
func (r *Reviewer) removeWorktree(ctx context.Context, dir, wt string) error {
	return r.git(context.WithoutCancel(ctx), dir, nil, "worktree", "remove", "--force", wt)
}

// run runs the skill and reads its comment, once more with CorrectionNote when the comment has no verdict.
func (r *Reviewer) run(ctx context.Context, req Request, pr forge.PR, wt string, before []forge.Comment) (Result, error) {
	res := Result{HeadSHA: pr.HeadSHA}
	prompt := Prompt(r.cfg.Skill, pr)
	for try := 1; ; try++ {
		out, err := r.rt.Run(ctx, headless.Spec{
			Model: r.cfg.Model, Prompt: prompt, Dir: wt, AllowedTools: AllowedTools, Timeout: r.cfg.Timeout, Env: lfsEnv,
		})
		if err != nil {
			return res, runError(req, err)
		}
		res.Usage = addUsage(res.Usage, out.Usage)
		after, err := r.forge.Comments(ctx, req.Repo, req.Number)
		if err != nil {
			return res, fmt.Errorf("review: comments of %s#%d after the run: %w", req.Repo, req.Number, err)
		}
		c, ok := changed(before, after)
		if !ok {
			return res, fmt.Errorf("%w: %s %d on %s posted or edited no comment; check that gh is authenticated "+
				"for the review run", ErrNoComment, r.cfg.Skill, req.Number, req.Repo)
		}
		verdict, text, err := Parse(c.Body)
		if err == nil {
			res.Verdict, res.CommentURL, res.Text = verdict, c.URL, oneLine(text, TextLimit)
			return res, nil
		}
		var ue unreadableError
		if try == MaxTries || !errors.As(err, &ue) {
			return res, fmt.Errorf("review of %s#%d after %d tries: %w", req.Repo, req.Number, try, err)
		}
		prompt = Prompt(r.cfg.Skill, pr) + correction(ue.reason)
	}
}

// changed returns the newest comment that the run posted or edited.
func changed(before, after []forge.Comment) (forge.Comment, bool) {
	seen := make(map[int64]string, len(before))
	for _, c := range before {
		seen[c.ID] = c.Body
	}
	for i := len(after) - 1; i >= 0; i-- {
		if body, ok := seen[after[i].ID]; !ok || body != after[i].Body {
			return after[i], true
		}
	}
	return forge.Comment{}, false
}

func runError(req Request, err error) error {
	var exit *runtime.ExitError
	if errors.As(err, &exit) && strings.Contains(exit.StderrTail, sudoMarker) {
		return fmt.Errorf("%w: review of %s#%d: reviews run without a password; remove the sudo call from the "+
			"review skill or give it a NOPASSWD sudoers line: %w", ErrSudoPassword, req.Repo, req.Number, err)
	}
	return fmt.Errorf("review of %s#%d: %w", req.Repo, req.Number, err)
}

func (r *Reviewer) post(ctx context.Context, req Request, pr forge.PR, res Result) error {
	st := forge.Status{Repo: req.Repo, SHA: pr.HeadSHA, Context: gate.ReviewContext, State: forge.StatusFailure,
		Description: DescriptionNotReady, TargetURL: res.CommentURL}
	if res.Verdict == Ready {
		st.State, st.Description = forge.StatusSuccess, DescriptionReady
	}
	if err := r.forge.PostStatus(ctx, st); err != nil {
		return fmt.Errorf("review: post %s on %s@%s: %w", gate.ReviewContext, req.Repo, pr.HeadSHA, err)
	}
	return nil
}

// recordUsage appends the review's tokens as an event; it carries counts only, never the review's text.
func (r *Reviewer) recordUsage(ctx context.Context, req Request, u headless.Usage) error {
	if u.Total() == 0 {
		return nil
	}
	ev := model.Event{Type: model.EventTokenUsage, Project: req.Project, TaskID: req.TaskID, RunID: req.RunID,
		Detail: fmt.Sprintf("review of %s#%d: %d tokens (input %d, output %d, cache creation %d, cache read %d)",
			req.Repo, req.Number, u.Total(), u.Input, u.Output, u.CacheCreation, u.CacheRead)}
	if err := r.events.Append(context.WithoutCancel(ctx), ev); err != nil {
		return fmt.Errorf("review: record the usage of %s#%d: %w", req.Repo, req.Number, err)
	}
	return nil
}

func addUsage(a, b headless.Usage) headless.Usage {
	return headless.Usage{Input: a.Input + b.Input, Output: a.Output + b.Output,
		CacheCreation: a.CacheCreation + b.CacheCreation, CacheRead: a.CacheRead + b.CacheRead}
}

// Prompt is the review run's prompt: the skill with the PR number, then fanin's rules for the run.
func Prompt(skill string, pr forge.PR) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d\n\n", skill, pr.Number)
	fmt.Fprintf(&b, "fanin rules for this run: review only. The current directory is head %s of branch %s, "+
		"checked out detached.\n", pr.HeadSHA, pr.HeadBranch)
	fmt.Fprintf(&b, "Post the verdict comment as the skill says: first line `## %s`, last line `%s: %s` or `%s: %s`.\n",
		CommentMark, VerdictLabel, Ready, VerdictLabel, NotReady)
	fmt.Fprintf(&b, "Change nothing: no commits, no pushes, no merge of %s into the branch, no CI re-runs, "+
		"no other worktrees.\n", pr.BaseBranch)
	return b.String()
}

func correction(reason string) string {
	return fmt.Sprintf("\n%s: it %s. Edit it (gh pr comment --edit-last) so its first line is `## %s` and its "+
		"last line `%s: %s` or `%s: %s`.\n", CorrectionNote, reason, CommentMark, VerdictLabel, Ready, VerdictLabel,
		NotReady)
}
