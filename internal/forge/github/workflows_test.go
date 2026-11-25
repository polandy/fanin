package github_test

import (
	"context"
	"errors"
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
	runListArgv = []string{"gh", "run", "list", flagRepo, repo, "--workflow", workflow, "--commit", headSHA,
		"--limit", "1", flagJSON, "databaseId"}
	runWatchArgv = []string{"gh", "run", "watch", "4711", flagRepo, repo, "--exit-status"}
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
