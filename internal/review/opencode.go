package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/runtime"
)

// The opencode review's defaults and limits (SCENARIOS OC-4).
const (
	DefaultOCUser    = "aiagent"
	DefaultOCSandbox = "/usr/local/bin/oc-sandbox"
	// DefaultOCTimeout bounds one review run; timeout(1) enforces it, as the sandbox runs outside fanin's clock.
	DefaultOCTimeout = 30 * time.Minute
	// DiffMax is the most bytes of the PR diff the prompt carries.
	DiffMax = 60000
	// PolicyClass is the class of the LLM policy whose model reviews when no model is set.
	PolicyClass = "cloud-fallback"
)

// ocStateScript prints the sandbox clone's HEAD and its changed files.
const ocStateScript = "git rev-parse HEAD && git status --porcelain"

const ocTextEvent = "text"

// ErrCloneState is a sandbox clone that is not at the PR head, is dirty, or changed during the review.
var ErrCloneState = errors.New("review: sandbox clone state")

// CommentForge is the forge of an opencode review: it reads the PR and posts the comment and the status.
type CommentForge interface {
	PR(ctx context.Context, repo string, number int) (forge.PR, error)
	PostStatus(ctx context.Context, st forge.Status) error
	PostComment(ctx context.Context, repo string, number int, body string) (string, error)
}

// OpencodeConfig names the sandbox user and launcher, the model or the policy file to read it from, and the timeout.
type OpencodeConfig struct {
	User    string
	Sandbox string
	Model   string
	Policy  string
	Timeout time.Duration
}

// DefaultOpencodeConfig returns the sandbox defaults without a model or policy.
func DefaultOpencodeConfig() OpencodeConfig {
	return OpencodeConfig{User: DefaultOCUser, Sandbox: DefaultOCSandbox, Timeout: DefaultOCTimeout}
}

// OpencodeRequest is the review of one opencode PR; Label is the project's sandbox name and Dir its checkout.
type OpencodeRequest struct {
	Project string
	Label   string
	Repo    string
	Number  int
	Dir     string
}

// Opencode reviews opencode PRs read-only in the sandbox and posts the comment itself (concept D2).
type Opencode struct {
	runner runtime.Runner
	forge  CommentForge
	cfg    OpencodeConfig
}

// NewOpencode returns an opencode reviewer; Review checks cfg.
func NewOpencode(runner runtime.Runner, f CommentForge, cfg OpencodeConfig) *Opencode {
	return &Opencode{runner: runner, forge: f, cfg: cfg}
}

// OCFooter is the last part of an opencode review comment; it names the model.
func OCFooter(model string) string {
	return "\n\n---\nReviewed by opencode (" + model + ") in the sandbox, read-only."
}

// Review reviews the PR head in the sandbox clone and posts the comment and the status; an error posts nothing.
func (o *Opencode) Review(ctx context.Context, req OpencodeRequest) (Result, error) {
	if req.Project == "" || req.Label == "" || strings.HasPrefix(req.Label, "-") || req.Repo == "" ||
		req.Number <= 0 || req.Dir == "" {
		return Result{}, fmt.Errorf("%w: opencode review of project %q label %q repo %q PR %d dir %q; pass all five",
			runtime.ErrInvalidSpec, req.Project, req.Label, req.Repo, req.Number, req.Dir)
	}
	model, err := o.model()
	if err != nil {
		return Result{}, err
	}
	pr, err := readHead(ctx, o.forge, req.Repo, req.Number)
	if err != nil {
		return Result{}, err
	}
	diff, err := o.diff(ctx, req.Dir, pr)
	if err != nil {
		return Result{}, err
	}
	if err := o.checkClone(ctx, req, pr, "before"); err != nil {
		return Result{}, err
	}
	res, err := o.run(ctx, req, pr, model, OCPrompt(pr, diff))
	if err != nil {
		return Result{}, err
	}
	url, err := o.forge.PostComment(ctx, req.Repo, req.Number, res.Text+OCFooter(model))
	if err != nil {
		return Result{}, fmt.Errorf("review: post the opencode review on %s#%d: %w", req.Repo, req.Number, err)
	}
	res.CommentURL, res.Text = url, oneLine(res.Text, TextLimit)
	if err := postStatus(ctx, o.forge, req.Repo, pr, res); err != nil {
		return Result{}, err
	}
	return res, nil
}

// model is the set model or the policy's cloud-fallback model.
func (o *Opencode) model() (string, error) {
	if o.cfg.Model != "" {
		return o.cfg.Model, nil
	}
	raw, err := os.ReadFile(o.cfg.Policy)
	if err != nil {
		return "", fmt.Errorf("review: read the %s model from the LLM policy %q; set review_model or the policy: %w",
			PolicyClass, o.cfg.Policy, err)
	}
	var policy struct {
		Classes map[string]struct {
			Model string `json:"model"`
		} `json:"classes"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		return "", fmt.Errorf("review: parse the LLM policy %q for the %s model: %w", o.cfg.Policy, PolicyClass, err)
	}
	m := policy.Classes[PolicyClass].Model
	if m == "" || strings.HasPrefix(m, "-") {
		return "", fmt.Errorf("%w: the LLM policy %q has %s model %q; set classes.%s.model", runtime.ErrInvalidSpec,
			o.cfg.Policy, PolicyClass, m, PolicyClass)
	}
	return m, nil
}

// diff fetches base and head into the checkout and returns the PR diff.
func (o *Opencode) diff(ctx context.Context, dir string, pr forge.PR) (string, error) {
	if pr.BaseBranch == "" || strings.HasPrefix(pr.BaseBranch, "-") {
		return "", fmt.Errorf("%w: PR %d base branch %q; re-read the PR", ErrUnreadable, pr.Number, pr.BaseBranch)
	}
	git := []string{gitProgram, "-C", dir}
	fetch := append(append([]string{}, git...), "fetch", "-q", remote, pr.BaseBranch, pr.HeadBranch)
	if _, err := o.runner.Run(ctx, runtime.Cmd{Argv: fetch}); err != nil {
		return "", fmt.Errorf("review: %s: %w", strings.Join(fetch, " "), err)
	}
	argv := append(append([]string{}, git...), "diff", remote+"/"+pr.BaseBranch+"..."+pr.HeadSHA)
	out, err := o.runner.Run(ctx, runtime.Cmd{Argv: argv})
	if err != nil {
		return "", fmt.Errorf("review: %s: %w", strings.Join(argv, " "), err)
	}
	return string(out.Stdout), nil
}

// checkClone refuses a sandbox clone off the PR head or with changed files.
func (o *Opencode) checkClone(ctx context.Context, req OpencodeRequest, pr forge.PR, when string) error {
	out, err := o.sandbox(ctx, req, []byte(ocStateScript), "--script")
	if err != nil {
		return fmt.Errorf("review: read the sandbox clone of %s %s the review: %w", req.Label, when, err)
	}
	head, status, _ := strings.Cut(strings.TrimRight(string(out), "\n"), "\n")
	if head != pr.HeadSHA || strings.TrimSpace(status) != "" {
		return fmt.Errorf("%w: %s the review of %s#%d the sandbox clone %s is at %q with changes %q, want head %s "+
			"and no changes; review not posted, move the leftovers out of the clone", ErrCloneState, when, req.Repo,
			req.Number, req.Label, head, status, pr.HeadSHA)
	}
	return nil
}

// run runs the review and checks the clone after it, once more with CorrectionNote when the text has no verdict.
func (o *Opencode) run(ctx context.Context, req OpencodeRequest, pr forge.PR, model, prompt string) (Result, error) {
	title, base := "review PR "+strconv.Itoa(req.Number), prompt
	for try := 1; ; try++ {
		out, err := o.sandbox(ctx, req, []byte(prompt), "--run", "--model", model, "--title", title)
		if err != nil {
			return Result{}, fmt.Errorf("review of %s#%d in the sandbox: %w", req.Repo, req.Number, err)
		}
		body, err := ocText(out)
		if err != nil {
			return Result{}, fmt.Errorf("review of %s#%d: %w", req.Repo, req.Number, err)
		}
		if err := o.checkClone(ctx, req, pr, "after"); err != nil {
			return Result{}, err
		}
		verdict, text, err := Parse(body)
		if err == nil {
			return Result{HeadSHA: pr.HeadSHA, Verdict: verdict, Text: text}, nil
		}
		var ue unreadableError
		if try == MaxTries || !errors.As(err, &ue) {
			return Result{}, fmt.Errorf("review of %s#%d after %d tries: %w", req.Repo, req.Number, try, err)
		}
		prompt = base + ocCorrection(ue.reason)
	}
}

// sandbox runs the launcher as the sandbox user; --run goes under timeout(1).
func (o *Opencode) sandbox(ctx context.Context, req OpencodeRequest, stdin []byte, args ...string) ([]byte, error) {
	if o.cfg.User == "" || o.cfg.Sandbox == "" || o.cfg.Timeout < time.Second {
		return nil, fmt.Errorf("%w: opencode review user %q, sandbox %q, timeout %s; set all three", runtime.ErrInvalidSpec,
			o.cfg.User, o.cfg.Sandbox, o.cfg.Timeout)
	}
	argv := append([]string{"sudo", "-n", "-u", o.cfg.User, o.cfg.Sandbox, req.Label}, args...)
	if args[0] == "--run" {
		argv = append([]string{"timeout", strconv.Itoa(int(o.cfg.Timeout / time.Second))}, argv...)
	}
	res, err := o.runner.Run(ctx, runtime.Cmd{Argv: argv, Stdin: stdin})
	var exit *runtime.ExitError
	if errors.As(err, &exit) && strings.Contains(exit.StderrTail, sudoMarker) {
		return nil, fmt.Errorf("%w: sudo -u %s %s; add a NOPASSWD sudoers line for it: %w", ErrSudoPassword,
			o.cfg.User, o.cfg.Sandbox, err)
	}
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// ocText joins the text parts of opencode's JSON event lines; a line that is no JSON event is unreadable.
func ocText(out []byte) (string, error) {
	var b strings.Builder
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return "", fmt.Errorf("%w: opencode printed %q, no JSON event: %w", ErrUnreadable, oneLine(string(line),
				TextLimit), err)
		}
		if ev.Type == ocTextEvent {
			b.WriteString(ev.Part.Text)
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", fmt.Errorf("%w: opencode gave no review text", ErrUnreadable)
	}
	return b.String(), nil
}

// OCPrompt is the opencode review's prompt: fanin's rules, then the diff capped at DiffMax bytes.
func OCPrompt(pr forge.PR, diff string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review PR %d, head %s of branch %s against %s. The current directory is the PR head.\n",
		pr.Number, pr.HeadSHA, pr.HeadBranch, pr.BaseBranch)
	fmt.Fprintf(&b, "Answer with the review only: first line `## %s`, then the findings, last line `%s: %s` or "+
		"`%s: %s`.\n", CommentMark, VerdictLabel, Ready, VerdictLabel, NotReady)
	b.WriteString("Change nothing: no edits, no commits, no pushes, no files outside the answer.\n")
	if len(diff) > DiffMax {
		cut := DiffMax
		for cut > 0 && !utf8.RuneStart(diff[cut]) {
			cut--
		}
		fmt.Fprintf(&b, "\nThe diff below is cut: only the first %d of %d bytes; read the rest with git diff %s/%s...%s.\n",
			DiffMax, len(diff), remote, pr.BaseBranch, pr.HeadSHA)
		diff = diff[:cut]
	}
	b.WriteString("\n```diff\n" + diff + "\n```\n")
	return b.String()
}

func ocCorrection(reason string) string {
	return fmt.Sprintf("\n%s: it %s. Answer again: first line `## %s`, last line `%s: %s` or `%s: %s`.\n",
		CorrectionNote, reason, CommentMark, VerdictLabel, Ready, VerdictLabel, NotReady)
}

// statusPoster posts the fanin/review status.
type statusPoster interface {
	PostStatus(ctx context.Context, st forge.Status) error
}

// postStatus posts the verdict as the fanin/review status on the reviewed head, linking the comment.
func postStatus(ctx context.Context, f statusPoster, repo string, pr forge.PR, res Result) error {
	st := forge.Status{Repo: repo, SHA: pr.HeadSHA, Context: gate.ReviewContext, State: forge.StatusFailure,
		Description: DescriptionNotReady, TargetURL: res.CommentURL}
	if res.Verdict == Ready {
		st.State, st.Description = forge.StatusSuccess, DescriptionReady
	}
	if err := f.PostStatus(ctx, st); err != nil {
		return fmt.Errorf("review: post %s on %s@%s: %w", gate.ReviewContext, repo, pr.HeadSHA, err)
	}
	return nil
}
