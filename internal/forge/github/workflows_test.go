package github_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	workflow = "docker.yml"
	flagJSON = "--json"
	runID    = 4711
)

var (
	runListArgv = []string{"gh", verbRun, verbList, flagRepo, repo, "--workflow", workflow, "--commit", headSHA,
		flagLimit, "1", flagJSON, "databaseId"}
	runWatchArgv = []string{"gh", verbRun, "watch", "4711", flagRepo, repo, "--exit-status"}
)

func TestLatestRun(t *testing.T) {
	t.Run("DR-1_newest_run_of_the_workflow_on_the_commit", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(runListArgv, testutil.Reply{Stdout: `[{"databaseId":4711}]`})
		got, err := c.LatestRun(context.Background(), repo, workflow, headSHA)
		if err != nil || got != runID {
			t.Fatalf("LatestRun = %d, %v; want %d", got, err, runID)
		}
	})

	cases := []struct {
		name  string
		reply testutil.Reply
		want  error
	}{
		{"DR-2_no_run_yet_is_not_found", testutil.Reply{Stdout: `[]`}, forge.ErrNotFound},
		{"DR-2_network_failure", testutil.Reply{ExitCode: 1, Stderr: netStderr}, forge.ErrNetwork},
		{"DR-2_unreadable_answer", testutil.Reply{Stdout: `not json`}, nil},
		{"DR-2_zero_run_id", testutil.Reply{Stdout: `[{"databaseId":0}]`}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := newClient(t)
			r.Expect(runListArgv, tc.reply)
			_, err := c.LatestRun(context.Background(), repo, workflow, headSHA)
			if err == nil {
				t.Fatal("LatestRun succeeded; want an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v; want %v", err, tc.want)
			}
		})
	}
}

func TestWatchRun(t *testing.T) {
	cases := []struct {
		name  string
		reply testutil.Reply
		want  error
	}{
		{"DR-1_successful_run", testutil.Reply{}, nil},
		{"DR-2_failed_run", testutil.Reply{ExitCode: 1, Stderr: "run 4711 failed"}, forge.ErrRunFailed},
		{"DR-2_network_failure_is_not_a_failed_run", testutil.Reply{ExitCode: 1, Stderr: netStderr}, forge.ErrNetwork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := newClient(t)
			r.Expect(runWatchArgv, tc.reply)
			err := c.WatchRun(context.Background(), repo, runID)
			if !errors.Is(err, tc.want) {
				t.Errorf("WatchRun = %v; want %v", err, tc.want)
			}
		})
	}
}

var _ forge.Workflows = (*github.Client)(nil)

func TestFailedLog(t *testing.T) {
	listArgv := []string{"gh", verbRun, verbList, flagRepo, repo, "--commit", headSHA, flagLimit, "20", flagJSON,
		"databaseId,conclusion"}
	viewArgv := []string{"gh", verbRun, verbView, "2", flagRepo, repo, "--log-failed"}
	const runs = `[{"databaseId":3,"conclusion":""},{"databaseId":2,"conclusion":"failure"},{"databaseId":1,"conclusion":"success"}]`

	t.Run("GH-9_the_failed_step_log_is_cut_to_its_tail_and_redacted", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(listArgv, testutil.Reply{Stdout: runs})
		r.Expect(viewArgv, testutil.Reply{Stdout: strings.Repeat("old line\n", 1000) + "token " + token + "\nFAIL TestX\n"})
		got, err := c.FailedLog(context.Background(), repo, headSHA, forge.FailLogMax)
		if err != nil {
			t.Fatalf("FailedLog: %v", err)
		}
		if len(got) > forge.FailLogMax || !strings.HasSuffix(got, "FAIL TestX\n") || strings.Contains(got, token) ||
			!strings.Contains(got, forge.Redacted) {
			t.Errorf("FailedLog = %q (%d bytes), want the redacted tail of at most %d bytes", got, len(got), forge.FailLogMax)
		}
	})

	t.Run("GH-9_a_short_log_comes_whole", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(listArgv, testutil.Reply{Stdout: runs})
		r.Expect(viewArgv, testutil.Reply{Stdout: "FAIL TestX\n"})
		if got, err := c.FailedLog(context.Background(), repo, headSHA, forge.FailLogMax); err != nil || got != "FAIL TestX\n" {
			t.Errorf("FailedLog = %q, %v; want the whole log", got, err)
		}
	})

	t.Run("GH-9_no_failed_run_is_not_found", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(listArgv, testutil.Reply{Stdout: `[{"databaseId":1,"conclusion":"success"}]`})
		_, err := c.FailedLog(context.Background(), repo, headSHA, forge.FailLogMax)
		if !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("FailedLog = %v; want ErrNotFound", err)
		}
	})

	t.Run("GH-9_gh_failures_are_errors", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(listArgv, testutil.Reply{Stdout: runs})
		r.Expect(viewArgv, testutil.Reply{ExitCode: 1, Stderr: netStderr})
		if _, err := c.FailedLog(context.Background(), repo, headSHA, forge.FailLogMax); !errors.Is(err, forge.ErrNetwork) {
			t.Errorf("FailedLog = %v; want ErrNetwork", err)
		}
	})
}

var _ forge.FailLogs = (*github.Client)(nil)
