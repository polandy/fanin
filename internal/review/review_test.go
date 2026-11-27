package review_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/review"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

// scriptOK queues a whole review of a head with own changes: fetch, parents, worktree, the claude replies and the removal.
func scriptOK(rr *testutil.RecordingRunner, claude ...testutil.Reply) {
	rr.Expect(fetchArgv, testutil.Reply{})
	rr.Expect(parentsArgv, ownCommit)
	rr.Expect(addArgv, testutil.Reply{})
	for _, r := range claude {
		rr.Expect(claudeArgv, r)
	}
	rr.Expect(removeArgv, testutil.Reply{})
}

func TestReview(t *testing.T) {
	t.Run("OC-3_detached_worktree_read_only_tools_and_status_on_the_head", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		f := newForge([]forge.Comment{comment(1, "lgtm")}, []forge.Comment{comment(1, "lgtm"), comment(9, readyBody)})
		s := newSetup(rr, f)
		got, err := s.reviewer().Review(context.Background(), request())
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		rr.Done()
		calls := rr.Calls()
		claude := calls[2]
		if claude.Dir != worktree {
			t.Errorf("claude runs in %q, want the worktree %q", claude.Dir, worktree)
		}
		for _, c := range []runtime.Cmd{calls[1], claude} {
			if diff := cmp.Diff([]string{review.LFSSkipEnv}, c.Env); diff != "" {
				t.Errorf("%q env (-want +got):\n%s", c.Argv, diff)
			}
		}
		prompt := string(claude.Stdin)
		for _, want := range []string{"/pr-review 42", headSHA, "review only", "skip the fix step", "do not run make ci",
			"skip the CI status and branch freshness sections"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt lacks %q:\n%s", want, prompt)
			}
		}
		for _, tool := range review.AllowedTools {
			if strings.Contains(tool, "push") || strings.Contains(tool, "merge") || tool == "Edit" || tool == "Write" {
				t.Errorf("allowed tool %q writes", tool)
			}
		}
		if !strings.Contains(strings.Join(review.AllowedTools, ","), "Bash(gh pr comment:*)") {
			t.Errorf("allowed tools %v lack the comment", review.AllowedTools)
		}
		want := []forge.Status{{Repo: repo, SHA: headSHA, Context: gate.ReviewContext, State: forge.StatusSuccess,
			Description: review.DescriptionReady, TargetURL: commentURL}}
		if diff := cmp.Diff(want, f.posted()); diff != "" {
			t.Errorf("statuses (-want +got):\n%s", diff)
		}
		if got.HeadSHA != headSHA || got.Verdict != review.Ready || got.CommentURL != commentURL {
			t.Errorf("result = %+v", got)
		}
		if got.Text != "## PR quality review Head "+headSHA+". - No findings. Merge readiness: Ready" {
			t.Errorf("text = %q", got.Text)
		}
	})
	t.Run("OC-3_not_ready_posts_a_failure_status", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		f := newForge([]forge.Comment{}, []forge.Comment{comment(9, notReadyBody)})
		got, err := newSetup(rr, f).reviewer().Review(context.Background(), request())
		if err != nil || got.Verdict != review.NotReady {
			t.Fatalf("Review = %+v, %v; want not ready", got, err)
		}
		st := f.posted()
		if len(st) != 1 || st[0].State != forge.StatusFailure || st[0].Description != review.DescriptionNotReady {
			t.Errorf("statuses = %+v, want one failure", st)
		}
		if r := headGate(t, f); r.Verdict != gate.Red {
			t.Errorf("gate = %s, want RED", r)
		}
	})
	t.Run("OC-3_text_is_cut_to_1500_chars", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		long := "## PR quality review\n" + strings.Repeat("- ä finding\n", 400) + "Merge readiness: Not ready"
		f := newForge(nil, []forge.Comment{comment(9, long)})
		got, err := newSetup(rr, f).reviewer().Review(context.Background(), request())
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		if n := len([]rune(got.Text)); n != review.TextLimit || review.TextLimit != 1500 {
			t.Errorf("text has %d chars, limit %d; want 1500", n, review.TextLimit)
		}
		if strings.Contains(got.Text, "\n") {
			t.Errorf("text spans lines: %q", got.Text[:80])
		}
	})
	t.Run("OC-3_timeout_is_1800s_and_the_worktree_is_removed", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		rr.Expect(fetchArgv, testutil.Reply{})
		rr.Expect(parentsArgv, ownCommit)
		rr.Expect(addArgv, testutil.Reply{})
		rr.Expect(removeArgv, testutil.Reply{})
		h := hangingClaude{RecordingRunner: rr, started: make(chan struct{})}
		f := newForge(nil)
		s := newSetup(h, f)
		errc := make(chan error, 1)
		go func() {
			_, err := s.reviewer().Review(context.Background(), request())
			errc <- err
		}()
		<-h.started
		s.clock.Advance(reviewTimeout - time.Second)
		select {
		case err := <-errc:
			t.Fatalf("Review returned before the timeout: %v", err)
		default:
		}
		s.clock.Advance(time.Second)
		if err := <-errc; !errors.Is(err, headless.ErrTimeout) {
			t.Errorf("err = %v; want ErrTimeout after %s", err, reviewTimeout)
		}
		rr.Done()
		if len(f.posted()) != 0 {
			t.Errorf("a timed out review posted %+v", f.posted())
		}
	})
	t.Run("OC-3_configured_skill_model_and_timeout", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		rr.Expect(fetchArgv, testutil.Reply{})
		rr.Expect(parentsArgv, ownCommit)
		rr.Expect(addArgv, testutil.Reply{})
		rr.Expect([]string{"claude", "-p", "--output-format", "json", flagModel, "opus", "--allowedTools",
			strings.Join(review.AllowedTools, ",")}, testutil.Reply{Stdout: okJSON})
		rr.Expect(removeArgv, testutil.Reply{})
		f := newForge(nil, []forge.Comment{comment(9, readyBody)})
		s := newSetup(rr, f)
		s.cfg = review.Config{Skill: "/team-review", Model: "opus", Timeout: time.Minute}
		if _, err := s.reviewer().Review(context.Background(), request()); err != nil {
			t.Fatalf("Review: %v", err)
		}
		rr.Done()
		if p := string(rr.Calls()[3].Stdin); !strings.HasPrefix(p, "/team-review 42\n") {
			t.Errorf("prompt = %q, want the configured skill first", p)
		}
	})
}

func TestReviewFailures(t *testing.T) {
	sudo := testutil.Reply{ExitCode: 1, Stderr: "sudo: a terminal is required to read the password; " +
		"either use the -S option to read from standard input or configure an askpass helper\n" +
		"sudo: a password is required"}
	tests := []struct {
		name string
		// claude is the scripted claude replies; nil means the run never starts.
		claude   []testutil.Reply
		comments [][]forge.Comment
		want     error
	}{
		{"OC-3_no_new_comment_is_an_error", []testutil.Reply{{Stdout: okJSON}},
			[][]forge.Comment{{comment(1, readyBody)}}, review.ErrNoComment},
		{"OC-3_no_comment_at_all_is_an_error", []testutil.Reply{{Stdout: okJSON}}, nil, review.ErrNoComment},
		{"OC-3_failed_run_is_an_error", []testutil.Reply{{ExitCode: 1, Stderr: "boom"}}, nil, nil},
		{"OC-5_no_text_is_an_error", []testutil.Reply{{Stdout: `{"is_error":false,"result":""}`}}, nil,
			runtime.ErrOddAnswer},
		{"OC-5_sudo_password_prompt_is_an_error", []testutil.Reply{sudo}, nil, review.ErrSudoPassword},
		{"OC-5_no_header_twice_is_an_error", []testutil.Reply{{Stdout: okJSON}, {Stdout: okJSON}},
			[][]forge.Comment{{}, {comment(9, "Looks fine.\nMerge readiness: Ready")},
				{comment(9, "Still fine.\nMerge readiness: Ready")}}, review.ErrUnreadable},
		{"OC-5_no_verdict_twice_is_an_error", []testutil.Reply{{Stdout: okJSON}, {Stdout: okJSON}},
			[][]forge.Comment{{}, {comment(9, "## PR quality review\n- fine")}}, review.ErrUnreadable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			scriptOK(rr, tc.claude...)
			f := newForge(tc.comments...)
			s := newSetup(rr, f)
			_, err := s.reviewer().Review(context.Background(), request())
			if err == nil {
				t.Fatal("Review succeeded, want an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			rr.Done()
			if len(f.posted()) != 0 {
				t.Errorf("a failed review posted %+v", f.posted())
			}
			if r := headGate(t, f); r.Verdict != gate.Red || !strings.Contains(r.String(), "no review for head") {
				t.Errorf("gate = %s, want RED without a review", r)
			}
		})
	}
	t.Run("OC-5_sudo_error_names_the_fix", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, sudo)
		_, err := newSetup(rr, newForge()).reviewer().Review(context.Background(), request())
		if err == nil || !strings.Contains(err.Error(), "NOPASSWD") {
			t.Errorf("err = %v, want the sudoers hint", err)
		}
	})
}

func TestReviewUnreadableForge(t *testing.T) {
	for _, cfg := range []review.Config{
		{Model: reviewModel, Timeout: reviewTimeout},
		{Skill: reviewSkill, Timeout: reviewTimeout},
		{Skill: reviewSkill, Model: reviewModel},
	} {
		t.Run("incomplete_settings_start_nothing", func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			s := newSetup(rr, newForge())
			s.cfg = cfg
			if _, err := s.reviewer().Review(context.Background(), request()); !errors.Is(err, runtime.ErrInvalidSpec) {
				t.Errorf("err = %v, want ErrInvalidSpec", err)
			}
			if len(rr.Calls()) != 0 {
				t.Errorf("ran %q for incomplete settings", rr.Calls())
			}
		})
	}
	t.Run("PR_read_fails_before_any_git", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		f := newForge()
		f.prErr = forge.ErrNetwork
		if _, err := newSetup(rr, f).reviewer().Review(context.Background(), request()); !errors.Is(err, forge.ErrNetwork) {
			t.Errorf("err = %v, want ErrNetwork", err)
		}
		if len(rr.Calls()) != 0 {
			t.Errorf("git ran for an unreadable PR: %q", rr.Calls())
		}
	})
	for _, tc := range []struct {
		name string
		mut  func(*forge.PR)
		want error
	}{
		{"no_head_sha", func(p *forge.PR) { p.HeadSHA = "" }, review.ErrUnreadable},
		{"odd_head_sha", func(p *forge.PR) { p.HeadSHA = "--upload-pack=x" }, review.ErrUnreadable},
		{"no_head_branch", func(p *forge.PR) { p.HeadBranch = "" }, review.ErrUnreadable},
		{"option_branch", func(p *forge.PR) { p.HeadBranch = "-x" }, review.ErrUnreadable},
		{"merged", func(p *forge.PR) { p.State = forge.StateMerged }, forge.ErrNotOpen},
	} {
		t.Run("PR_"+tc.name+"_is_refused", func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			f := newForge()
			tc.mut(&f.pr)
			_, err := newSetup(rr, f).reviewer().Review(context.Background(), request())
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if len(rr.Calls()) != 0 {
				t.Errorf("git ran: %q", rr.Calls())
			}
		})
	}
	t.Run("comments_unreadable_before_the_run_start_nothing", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		f := newForge()
		f.commentsErr = forge.ErrUnreadable
		if _, err := newSetup(rr, f).reviewer().Review(context.Background(), request()); !errors.Is(err, forge.ErrUnreadable) {
			t.Errorf("err = %v, want ErrUnreadable", err)
		}
		if len(rr.Calls()) != 0 {
			t.Errorf("git ran although the comments before the run were unreadable: %q", rr.Calls())
		}
	})
	t.Run("OC-3_comments_unreadable_after_the_run_is_an_error_and_removes_the_worktree", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		f := newForge(nil)
		f.commentsErr, f.commentsErrFrom = forge.ErrUnreadable, 1
		if _, err := newSetup(rr, f).reviewer().Review(context.Background(), request()); !errors.Is(err, forge.ErrUnreadable) {
			t.Errorf("err = %v, want ErrUnreadable", err)
		}
		rr.Done()
		if len(f.posted()) != 0 {
			t.Errorf("an unreadable review posted %+v", f.posted())
		}
	})
	t.Run("OC-3_worktree_add_fails_without_a_run", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		rr.Expect(fetchArgv, testutil.Reply{})
		rr.Expect(parentsArgv, ownCommit)
		rr.Expect(addArgv, testutil.Reply{ExitCode: 128, Stderr: "fatal: already exists"})
		_, err := newSetup(rr, newForge()).reviewer().Review(context.Background(), request())
		if err == nil || !strings.Contains(err.Error(), "worktree remove --force") {
			t.Errorf("err = %v, want the cleanup hint", err)
		}
		rr.Done()
	})
	t.Run("OC-3_failed_removal_is_an_error", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		rr.Expect(fetchArgv, testutil.Reply{})
		rr.Expect(parentsArgv, ownCommit)
		rr.Expect(addArgv, testutil.Reply{})
		rr.Expect(claudeArgv, testutil.Reply{Stdout: okJSON})
		rr.Expect(removeArgv, testutil.Reply{ExitCode: 1, Stderr: "locked"})
		f := newForge(nil, []forge.Comment{comment(9, readyBody)})
		if _, err := newSetup(rr, f).reviewer().Review(context.Background(), request()); err == nil {
			t.Error("Review succeeded although the worktree stayed")
		}
		rr.Done()
	})
	t.Run("post_status_fails", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		f := newForge(nil, []forge.Comment{comment(9, readyBody)})
		f.postErr = forge.ErrNetwork
		if _, err := newSetup(rr, f).reviewer().Review(context.Background(), request()); !errors.Is(err, forge.ErrNetwork) {
			t.Errorf("err = %v, want ErrNetwork", err)
		}
	})
	t.Run("cancelled_review_still_removes_the_worktree", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		rr.Expect(fetchArgv, testutil.Reply{})
		rr.Expect(parentsArgv, ownCommit)
		rr.Expect(addArgv, testutil.Reply{})
		rr.Expect(removeArgv, testutil.Reply{})
		h := hangingClaude{RecordingRunner: rr, started: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		errc := make(chan error, 1)
		go func() {
			_, err := newSetup(h, newForge()).reviewer().Review(ctx, request())
			errc <- err
		}()
		<-h.started
		cancel()
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		rr.Done()
	})
}

func TestReviewRetry(t *testing.T) {
	t.Run("OC-5_missing_verdict_is_retried_once_with_a_correction_note", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON}, testutil.Reply{Stdout: okJSON})
		f := newForge(nil, []forge.Comment{comment(9, "## PR quality review\n- fine")},
			[]forge.Comment{comment(9, readyBody)})
		s := newSetup(rr, f)
		got, err := s.reviewer().Review(context.Background(), request())
		if err != nil || got.Verdict != review.Ready {
			t.Fatalf("Review = %+v, %v; want ready on the retry", got, err)
		}
		rr.Done()
		first, second := string(rr.Calls()[3].Stdin), string(rr.Calls()[4].Stdin)
		if strings.Contains(first, review.CorrectionNote) || !strings.Contains(second, review.CorrectionNote) {
			t.Errorf("correction note only belongs to the retry:\nfirst %q\nsecond %q", first, second)
		}
		if !strings.Contains(second, "no merge readiness verdict") {
			t.Errorf("retry prompt does not say what was wrong: %q", second)
		}
		ev := s.events.all()
		want := []model.Event{{Type: model.EventTokenUsage, Project: project, TaskID: "t-1", RunID: "r-1",
			Detail: "review of acme/widget#42: 260 tokens (input 200, output 40, cache creation 6, cache read 14)"}}
		if diff := cmp.Diff(want, ev); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
		if got.Usage.Total() != 260 {
			t.Errorf("usage = %+v, want both tries", got.Usage)
		}
	})
	t.Run("usage_event_on_a_failed_review_has_no_review_text", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		s := newSetup(rr, newForge([]forge.Comment{comment(1, "secret finding")}))
		if _, err := s.reviewer().Review(context.Background(), request()); !errors.Is(err, review.ErrNoComment) {
			t.Fatalf("err = %v, want ErrNoComment", err)
		}
		ev := s.events.all()
		if len(ev) != 1 || ev[0].Type != model.EventTokenUsage || strings.Contains(ev[0].Detail, "secret") ||
			strings.Contains(ev[0].Detail, "posted") {
			t.Errorf("events = %+v, want one usage event without contents", ev)
		}
	})
	t.Run("event_append_failure_is_an_error", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		s := newSetup(rr, newForge(nil, []forge.Comment{comment(9, readyBody)}))
		s.events.err = errors.New("disk full")
		if _, err := s.reviewer().Review(context.Background(), request()); err == nil {
			t.Error("Review succeeded although the usage was not recorded")
		}
	})
}

// TestReviewD5HeadOnly: a review counts only for the head it saw (concept D5).
func TestReviewD5HeadOnly(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	scriptOK(rr, testutil.Reply{Stdout: okJSON})
	f := newForge(nil, []forge.Comment{comment(9, readyBody)})
	if _, err := newSetup(rr, f).reviewer().Review(context.Background(), request()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if r := headGate(t, f); !r.Verdict.IsGreen() {
		t.Fatalf("gate on the reviewed head = %s, want GREEN", r)
	}
	f.push(newSHA)
	r := headGate(t, f)
	if r.Verdict != gate.Red || !strings.Contains(r.String(), "no review for head "+newSHA) {
		t.Errorf("gate after a push = %s, want RED no review for head %s", r, newSHA)
	}
	for _, st := range f.posted() {
		if st.SHA != headSHA {
			t.Errorf("status on %s, want only the reviewed head %s", st.SHA, headSHA)
		}
	}
}

const plainBody = "## PR quality review\n- ok\n\nMerge readiness: Ready"

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want review.Verdict
		text string
	}{
		{"OC-5_plain", plainBody, review.Ready,
			plainBody},
		{"OC-5_preamble_is_dropped", "Here is my review.\n\n## PR quality review\n- ok\nMerge readiness: ready",
			review.Ready, plainBody},
		{"OC-5_fences_are_dropped", "```markdown\n## PR quality review of #42\n- ok\n```\nMerge readiness: Not ready",
			review.NotReady, "## PR quality review of #42\n- ok\n\nMerge readiness: Not ready"},
		{"OC-5_bold_reworded_not_ready", "**PR quality review**\n- x.go:1 bug\n**Merge readiness:** NOT READY ❌",
			review.NotReady, "## PR quality review\n- x.go:1 bug\n\nMerge readiness: Not ready"},
		{"OC-5_verdict_on_the_next_line", "## PR Quality Review\n### Merge-readiness\n\nReady to merge.",
			review.Ready, "## PR quality review\n\nMerge readiness: Ready"},
		{"OC-5_last_verdict_wins", "## PR quality review\nMerge readiness: ready\n- one more\nMerge readiness: not ready",
			review.NotReady, "## PR quality review\nMerge readiness: ready\n- one more\n\nMerge readiness: Not ready"},
		{"OC-5_template_line_counts_as_not_ready", "## PR quality review\nMerge readiness: ready / not ready",
			review.NotReady, "## PR quality review\n\nMerge readiness: Not ready"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, text, err := review.Parse(tc.body)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if v != tc.want {
				t.Errorf("verdict = %q, want %q", v, tc.want)
			}
			if diff := cmp.Diff(tc.text, text); diff != "" {
				t.Errorf("text (-want +got):\n%s", diff)
			}
		})
	}
	for name, body := range map[string]string{
		"OC-5_empty":                   "",
		"OC-5_no_header":               "- ok\nMerge readiness: Ready",
		"OC-5_header_mid_line":         "See the PR quality review below.\nMerge readiness: Ready",
		"OC-5_no_verdict":              "## PR quality review\n- ok",
		"OC-5_verdict_before_header":   "Merge readiness: Ready\n## PR quality review\n- ok",
		"OC-5_label_without_a_verdict": "## PR quality review\nMerge readiness:\n\nsee above",
	} {
		t.Run(name+"_is_unreadable", func(t *testing.T) {
			if v, _, err := review.Parse(body); !errors.Is(err, review.ErrUnreadable) {
				t.Errorf("Parse = %q, %v; want ErrUnreadable", v, err)
			}
		})
	}
}
