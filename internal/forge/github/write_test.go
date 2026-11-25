package github_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/runtime/testutil"
	"github.com/polandy/fanin/internal/safego"
)

var (
	squash = forge.MergeRequest{Repo: repo, Number: number, Subject: subject,
		Body: trailerBody, HeadSHA: headSHA}
	mergeArgv = []string{"gh", "pr", "merge", "42", flagRepo, repo, "--squash",
		"--subject", subject, "--body", trailerBody, "--match-head-commit", headSHA}
)

func TestOpen(t *testing.T) {
	req := forge.OpenRequest{Repo: repo, Head: headBranch, Base: baseBranch, Title: title, Body: "why"}
	argv := []string{"gh", "pr", "create", flagRepo, repo, "--head", headBranch, "--base", baseBranch,
		"--title", title, "--body-file", "-"}
	t.Run("returns_the_number_and_sends_the_body_on_stdin", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: "https://github.com/acme/widget/pull/42\n"})
		n, err := c.Open(context.Background(), req)
		if err != nil || n != number {
			t.Fatalf("Open = %d, %v", n, err)
		}
		if got := string(r.Calls()[0].Stdin); got != "why" {
			t.Errorf("stdin = %q, want the body", got)
		}
	})
	t.Run("GH-5_answer_without_pr_url_is_unreadable", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: "created\n"})
		if n, err := c.Open(context.Background(), req); !errors.Is(err, forge.ErrUnreadable) || n != 0 {
			t.Errorf("Open = %d, %v; want 0, ErrUnreadable", n, err)
		}
	})
	t.Run("missing_head_is_rejected_before_gh", func(t *testing.T) {
		c, _ := newClient(t)
		bad := req
		bad.Head = ""
		if _, err := c.Open(context.Background(), bad); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	})
}

func TestEnableAutoMerge(t *testing.T) {
	c, r := newClient(t)
	r.Expect([]string{"gh", "pr", "merge", "42", flagRepo, repo, "--squash", "--auto",
		"--subject", subject, "--body", trailerBody, "--match-head-commit", headSHA},
		testutil.Reply{})
	if err := c.EnableAutoMerge(context.Background(), squash); err != nil {
		t.Fatal(err)
	}
	t.Run("request_without_head_sha_is_rejected", func(t *testing.T) {
		bad := squash
		bad.HeadSHA = ""
		if err := c.EnableAutoMerge(context.Background(), bad); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	})
}

func TestPostStatus(t *testing.T) {
	st := forge.Status{Repo: repo, SHA: headSHA, Context: reviewContext, State: forge.StatusSuccess,
		Description: "review green"}
	base := []string{"gh", api, "--method", "POST", "repos/acme/widget/statuses/" + headSHA,
		"-f", "state=success", "-f", "context=fanin/review", "-f", "description=review green"}
	t.Run("without_target_url", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(base, testutil.Reply{Stdout: "{}"})
		if err := c.PostStatus(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("with_target_url", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(append(append([]string{}, base...), "-f", "target_url=https://example.com/r/1"), testutil.Reply{})
		withURL := st
		withURL.TargetURL = "https://example.com/r/1"
		if err := c.PostStatus(context.Background(), withURL); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown_state_is_rejected_before_gh", func(t *testing.T) {
		c, _ := newClient(t)
		bad := st
		bad.State = "green"
		if err := c.PostStatus(context.Background(), bad); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	})
	t.Run("network_failure", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(base, testutil.Reply{ExitCode: 1, Stderr: netStderr})
		if err := c.PostStatus(context.Background(), st); !errors.Is(err, forge.ErrNetwork) {
			t.Errorf("err = %v, want ErrNetwork", err)
		}
	})
}

func TestDeleteBranch(t *testing.T) {
	argv := []string{"gh", api, "--method", "DELETE", "repos/acme/widget/git/refs/heads/feat/widget"}
	t.Run("deletes_the_ref", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{})
		if err := c.DeleteBranch(context.Background(), repo, headBranch); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("gone_branch_is_not_found", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{ExitCode: 1, Stderr: "gh: Reference does not exist (HTTP 422)"})
		if err := c.DeleteBranch(context.Background(), repo, headBranch); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestSquashMerge(t *testing.T) {
	t.Run("MG-6_open_pr_merges_with_subject_and_trailer_body", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, testutil.Reply{})
		if err := c.SquashMerge(context.Background(), squash); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("MG-6_already_merged_pr_is_not_merged_again", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, withState("MERGED"))})
		if err := c.SquashMerge(context.Background(), squash); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("MG-6_view_without_head_branch_stops_before_the_merge", func(t *testing.T) {
		c, r := newClient(t)
		d := prDoc()
		delete(d, "headRefName")
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, d)})
		if err := c.SquashMerge(context.Background(), squash); !errors.Is(err, forge.ErrUnreadable) {
			t.Errorf("err = %v, want ErrUnreadable", err)
		}
		if n := len(r.Calls()); n != 1 {
			t.Errorf("%d gh calls, want only the view", n)
		}
	})
	t.Run("closed_pr_is_not_open", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, withState("CLOSED"))})
		if err := c.SquashMerge(context.Background(), squash); !errors.Is(err, forge.ErrNotOpen) {
			t.Errorf("err = %v, want ErrNotOpen", err)
		}
	})
	t.Run("MG-6_non_network_failure_is_not_retried", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, testutil.Reply{ExitCode: 1, Stderr: "Pull request is not mergeable: the base branch policy prohibits the merge"})
		err := c.SquashMerge(context.Background(), squash)
		if err == nil || errors.Is(err, forge.ErrNetwork) {
			t.Errorf("err = %v, want a non-network error", err)
		}
	})
}

// runMerge starts SquashMerge on a signalClock and returns the channel its result arrives on.
func runMerge(ctx context.Context, t *testing.T, c *github.Client) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	safego.Go(ctx, slog.Default(), func(ctx context.Context) { done <- c.SquashMerge(ctx, squash) })
	return done
}

// drive advances clk through every wait of the merge until it returns, so a regression fails instead of hanging.
func drive(clk *signalClock, done <-chan error) ([]time.Duration, error) {
	var waits []time.Duration
	for {
		select {
		case d := <-clk.waits:
			waits = append(waits, d)
			clk.Advance(d)
		case err := <-done:
			return waits, err
		}
	}
}

func TestSquashMergeRetries(t *testing.T) {
	netFail := testutil.Reply{ExitCode: 1, Stderr: netStderr}
	t.Run("MG-6_network_failures_retry_at_10_30_60s_and_check_merged_first", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		t.Cleanup(r.Done)
		clk := newSignalClock()
		c := github.New(r, clk, nil)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, netFail)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, netFail)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, netFail)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, netFail)
		waits, err := drive(clk, runMerge(context.Background(), t, c))
		if !errors.Is(err, forge.ErrNetwork) {
			t.Errorf("err = %v, want ErrNetwork after the last retry", err)
		}
		if diff := cmp.Diff([]time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})
	t.Run("MG-6_merged_during_the_wait_ends_without_a_second_merge", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		t.Cleanup(r.Done)
		clk := newSignalClock()
		c := github.New(r, clk, nil)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, netFail)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, withState("MERGED"))})
		waits, err := drive(clk, runMerge(context.Background(), t, c))
		if err != nil || len(waits) != 1 {
			t.Errorf("waits %v err %v, want one wait and success: the PR is merged", waits, err)
		}
	})
	t.Run("MG-6_cancelled_wait_stops_the_retry", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		t.Cleanup(r.Done)
		clk := newSignalClock()
		c := github.New(r, clk, nil)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		r.Expect(mergeArgv, netFail)
		ctx, cancel := context.WithCancel(context.Background())
		done := runMerge(ctx, t, c)
		select {
		case <-clk.waits:
			cancel()
		case err := <-done:
			t.Fatalf("merge returned %v without waiting", err)
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

func TestPostComment(t *testing.T) {
	argv := []string{"gh", "pr", "comment", "42", flagRepo, repo, "--body-file", "-"}
	t.Run("OC-4_posts_the_body_on_stdin_and_returns_the_comment_url", func(t *testing.T) {
		c, r := newClient(t)
		url := "https://github.com/acme/widget/pull/42#issuecomment-9"
		r.Expect(argv, testutil.Reply{Stdout: url + "\n"})
		got, err := c.PostComment(context.Background(), repo, number, "review")
		if err != nil || got != url {
			t.Fatalf("PostComment = %q, %v", got, err)
		}
		if body := string(r.Calls()[0].Stdin); body != "review" {
			t.Errorf("stdin = %q, want the body", body)
		}
	})
	t.Run("OC-4_an_answer_without_a_comment_url_is_unreadable", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: "ok\n"})
		if _, err := c.PostComment(context.Background(), repo, number, "review"); !errors.Is(err, forge.ErrUnreadable) {
			t.Errorf("err = %v, want ErrUnreadable", err)
		}
	})
	t.Run("OC-4_an_empty_body_is_rejected_before_gh", func(t *testing.T) {
		c, _ := newClient(t)
		if _, err := c.PostComment(context.Background(), repo, number, " "); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	})
}
