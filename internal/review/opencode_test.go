package review_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/review"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	label     = "alpha"
	ocModel   = "cloud/model-x"
	ocTimeout = "1800"
	stateCmd  = "git rev-parse HEAD && git status --porcelain"
)

var (
	ocFetch = []string{gitBin, "-C", repoDir, "fetch", "-q", "origin", "main", headBranch}
	ocDiff  = []string{gitBin, "-C", repoDir, "diff", "origin/main..." + headSHA}
	ocState = []string{"sudo", "-n", "-u", review.DefaultOCUser, review.DefaultOCSandbox, label, "--script"}
	ocRun   = []string{"timeout", ocTimeout, "sudo", "-n", "-u", review.DefaultOCUser, review.DefaultOCSandbox, label,
		"--run", flagModel, ocModel, "--title", "review PR 42"}
)

// ocForge adds the comment post an opencode review needs to fakeForge.
type ocForge struct {
	*fakeForge
	bodies     []string
	commentErr error
}

func (f *ocForge) PostComment(_ context.Context, _ string, _ int, body string) (string, error) {
	if f.commentErr != nil {
		return "", f.commentErr
	}
	f.bodies = append(f.bodies, body)
	return commentURL, nil
}

// textEvents is opencode's JSON event stream with text split over two parts.
func textEvents(text string) string {
	half := len(text) / 2
	return `{"type":"step_start","part":{}}` + "\n" +
		`{"type":"text","part":{"text":` + strconv.Quote(text[:half]) + `}}` + "\n" +
		`{"type":"tool_use","part":{"tool":"read"}}` + "\n" +
		`{"type":"text","part":{"text":` + strconv.Quote(text[half:]) + `}}` + "\n"
}

func ocRig(t *testing.T) (*testutil.RecordingRunner, *ocForge, *review.Opencode) {
	t.Helper()
	r := testutil.NewRecordingRunner(t)
	f := &ocForge{fakeForge: newForge()}
	cfg := review.DefaultOpencodeConfig()
	cfg.Model = ocModel
	return r, f, review.NewOpencode(r, f, cfg)
}

func ocRequest() review.OpencodeRequest {
	return review.OpencodeRequest{Project: project, Label: label, Repo: repo, Number: number, Dir: repoDir}
}

// expectDiff scripts the fetch, the diff and the clone state before the run.
func expectDiff(r *testutil.RecordingRunner, diff, state string) {
	r.Expect(ocFetch, testutil.Reply{})
	r.Expect(ocDiff, testutil.Reply{Stdout: diff})
	r.Expect(ocState, testutil.Reply{Stdout: state})
}

func TestOpencodeReview(t *testing.T) {
	clean := headSHA + "\n"
	t.Run("OC-4_reviews_in_the_sandbox_and_posts_the_comment_with_the_model_footer", func(t *testing.T) {
		r, f, oc := ocRig(t)
		expectDiff(r, "diff --git a/x b/x\n", clean)
		r.Expect(ocRun, testutil.Reply{Stdout: textEvents(readyBody)})
		r.Expect(ocState, testutil.Reply{Stdout: clean})

		res, err := oc.Review(t.Context(), ocRequest())
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		r.Done()
		if res.Verdict != review.Ready || res.HeadSHA != headSHA || res.CommentURL != commentURL {
			t.Errorf("result = %+v", res)
		}
		calls := r.Calls()
		if got := string(calls[2].Stdin); got != stateCmd {
			t.Errorf("state stdin = %q, want %q", got, stateCmd)
		}
		prompt := string(calls[3].Stdin)
		for _, part := range []string{"diff --git a/x b/x", "## " + review.CommentMark, "Change nothing"} {
			if !strings.Contains(prompt, part) {
				t.Errorf("prompt lacks %q:\n%s", part, prompt)
			}
		}
		if len(f.bodies) != 1 || !strings.HasPrefix(f.bodies[0], "## "+review.CommentMark) ||
			!strings.HasSuffix(f.bodies[0], review.OCFooter(ocModel)) {
			t.Errorf("bodies = %q, want one review with the footer %q", f.bodies, review.OCFooter(ocModel))
		}
		if !strings.Contains(review.OCFooter(ocModel), ocModel) {
			t.Errorf("footer %q does not name the model", review.OCFooter(ocModel))
		}
		want := []forge.Status{{Repo: repo, SHA: headSHA, Context: gate.ReviewContext, State: forge.StatusSuccess,
			Description: review.DescriptionReady, TargetURL: commentURL}}
		if diff := cmp.Diff(want, f.posted()); diff != "" {
			t.Errorf("statuses (-want +got):\n%s", diff)
		}
	})

	t.Run("OC-4_caps_the_diff_at_60000_bytes_with_a_note", func(t *testing.T) {
		r, _, oc := ocRig(t)
		big := strings.Repeat("é", review.DiffMax)
		expectDiff(r, big, clean)
		r.Expect(ocRun, testutil.Reply{Stdout: textEvents(readyBody)})
		r.Expect(ocState, testutil.Reply{Stdout: clean})

		if _, err := oc.Review(t.Context(), ocRequest()); err != nil {
			t.Fatalf("Review: %v", err)
		}
		r.Done()
		prompt := string(r.Calls()[3].Stdin)
		note := "only the first " + strconv.Itoa(review.DiffMax) + " of " + strconv.Itoa(len(big)) + " bytes"
		if !strings.Contains(prompt, note) {
			t.Errorf("prompt lacks the note %q", note)
		}
		if n := strings.Count(prompt, "é"); n*2 > review.DiffMax || n*2 < review.DiffMax-1 {
			t.Errorf("prompt carries %d bytes of the diff, want at most %d", n*2, review.DiffMax)
		}
	})

	t.Run("OC-4_reads_the_model_from_the_policy_when_none_is_set", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		f := &ocForge{fakeForge: newForge()}
		cfg := review.DefaultOpencodeConfig()
		cfg.Policy = filepath.Join(t.TempDir(), "llm-policy.json")
		policy := `{"classes":{"cloud-fallback":{"model":"` + ocModel + `"},"local":{"model":"m"}}}`
		if err := os.WriteFile(cfg.Policy, []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
		expectDiff(r, "d\n", clean)
		r.Expect(ocRun, testutil.Reply{Stdout: textEvents(readyBody)})
		r.Expect(ocState, testutil.Reply{Stdout: clean})
		if _, err := review.NewOpencode(r, f, cfg).Review(t.Context(), ocRequest()); err != nil {
			t.Fatalf("Review: %v", err)
		}
		r.Done()
	})

	t.Run("OC-4_an_unreadable_policy_is_an_error_and_posts_nothing", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		f := &ocForge{fakeForge: newForge()}
		cfg := review.DefaultOpencodeConfig()
		cfg.Policy = filepath.Join(t.TempDir(), "missing.json")
		if _, err := review.NewOpencode(r, f, cfg).Review(t.Context(), ocRequest()); err == nil ||
			!strings.Contains(err.Error(), "cloud-fallback") {
			t.Fatalf("err = %v, want the policy error naming cloud-fallback", err)
		}
		r.Done()
		if len(f.bodies) != 0 || len(f.posted()) != 0 {
			t.Errorf("posted %q %v, want nothing", f.bodies, f.posted())
		}
	})

	stops := []struct {
		name   string
		script func(*testutil.RecordingRunner)
	}{
		{"OC-4_a_clone_off_the_PR_head_posts_nothing", func(r *testutil.RecordingRunner) {
			expectDiff(r, "d\n", newSHA+"\n")
		}},
		{"OC-4_a_dirty_clone_posts_nothing", func(r *testutil.RecordingRunner) {
			expectDiff(r, "d\n", clean+" M x.go\n")
		}},
		{"OC-4_a_review_that_changed_the_clone_posts_nothing", func(r *testutil.RecordingRunner) {
			expectDiff(r, "d\n", clean)
			r.Expect(ocRun, testutil.Reply{Stdout: textEvents(readyBody)})
			r.Expect(ocState, testutil.Reply{Stdout: clean + "?? scratch.txt\n"})
		}},
	}
	for _, tc := range stops {
		t.Run(tc.name, func(t *testing.T) {
			r, f, oc := ocRig(t)
			tc.script(r)
			_, err := oc.Review(t.Context(), ocRequest())
			if !errors.Is(err, review.ErrCloneState) || !strings.Contains(err.Error(), "not posted") {
				t.Fatalf("err = %v, want ErrCloneState, not posted", err)
			}
			r.Done()
			if len(f.bodies) != 0 || len(f.posted()) != 0 {
				t.Errorf("posted %q %v, want nothing", f.bodies, f.posted())
			}
		})
	}

	t.Run("OC-5_a_review_without_a_verdict_is_retried_once_with_a_correction", func(t *testing.T) {
		r, f, oc := ocRig(t)
		expectDiff(r, "d\n", clean)
		r.Expect(ocRun, testutil.Reply{Stdout: textEvents("## PR quality review\n\nlooks fine")})
		r.Expect(ocState, testutil.Reply{Stdout: clean})
		r.Expect(ocRun, testutil.Reply{Stdout: textEvents(notReadyBody)})
		r.Expect(ocState, testutil.Reply{Stdout: clean})

		res, err := oc.Review(t.Context(), ocRequest())
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		r.Done()
		if !strings.Contains(string(r.Calls()[5].Stdin), review.CorrectionNote) {
			t.Errorf("retry prompt lacks %q", review.CorrectionNote)
		}
		if res.Verdict != review.NotReady || len(f.bodies) != 1 {
			t.Errorf("verdict %q, %d comments; want not ready and one comment", res.Verdict, len(f.bodies))
		}
		if st := f.posted(); len(st) != 1 || st[0].State != forge.StatusFailure {
			t.Errorf("statuses = %+v, want one failure", st)
		}
	})

	t.Run("OC-5_no_text_and_a_sudo_password_prompt_are_errors", func(t *testing.T) {
		r, f, oc := ocRig(t)
		expectDiff(r, "d\n", clean)
		r.Expect(ocRun, testutil.Reply{Stdout: `{"type":"step_start","part":{}}` + "\n"})
		if _, err := oc.Review(t.Context(), ocRequest()); !errors.Is(err, review.ErrUnreadable) {
			t.Errorf("no text: err = %v, want ErrUnreadable", err)
		}
		r.Done()

		r2, f2, oc2 := ocRig(t)
		r2.Expect(ocFetch, testutil.Reply{})
		r2.Expect(ocDiff, testutil.Reply{Stdout: "d\n"})
		r2.Expect(ocState, testutil.Reply{Stderr: "sudo: a password is required", ExitCode: 1})
		_, err := oc2.Review(t.Context(), ocRequest())
		if !errors.Is(err, review.ErrSudoPassword) || !strings.Contains(err.Error(), "sudoers") {
			t.Errorf("sudo: err = %v, want ErrSudoPassword naming the sudoers line", err)
		}
		r2.Done()
		if len(f.bodies)+len(f2.bodies) != 0 || len(f.posted())+len(f2.posted()) != 0 {
			t.Errorf("posted something, want nothing")
		}
	})

	t.Run("OC-6_a_failed_comment_post_is_an_error_and_posts_no_status", func(t *testing.T) {
		r, f, oc := ocRig(t)
		f.commentErr = errors.New("gh: HTTP 502")
		expectDiff(r, "d\n", clean)
		r.Expect(ocRun, testutil.Reply{Stdout: textEvents(readyBody)})
		r.Expect(ocState, testutil.Reply{Stdout: clean})
		if _, err := oc.Review(t.Context(), ocRequest()); err == nil || !strings.Contains(err.Error(), "502") {
			t.Fatalf("err = %v, want the post error", err)
		}
		r.Done()
		if len(f.posted()) != 0 {
			t.Errorf("statuses = %v, want none", f.posted())
		}
	})
}
