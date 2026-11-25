package sched

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

func TestGitUnpushed(t *testing.T) {
	p := testConfig().Projects[0]
	argv := []string{"git", "-C", p.Path, "log", "--format=%h %s", "refs/heads/feat/a1", "--not", "--remotes", "--"}

	t.Run("AW-12_lists_the_commits_of_the_run_branch_only", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		r.Expect(argv, testutil.Reply{Stdout: "abc123 add the parser\ndef456 fix the test\n\n"})
		got, err := GitUnpushed{Runner: r}.Unpushed(t.Context(), p, "feat/a1")
		if err != nil {
			t.Fatal(err)
		}
		if d := cmp.Diff([]string{"abc123 add the parser", "def456 fix the test"}, got); d != "" {
			t.Errorf("commits (-want +got):\n%s", d)
		}
	})

	t.Run("AW-12_pushed_branch_has_none", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		r.Expect(argv, testutil.Reply{})
		got, err := GitUnpushed{Runner: r}.Unpushed(t.Context(), p, "feat/a1")
		if err != nil || len(got) != 0 {
			t.Errorf("Unpushed = %q, %v; want none", got, err)
		}
	})

	t.Run("AW-12_failed_git_is_an_error_not_none", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		r.Expect(argv, testutil.Reply{ExitCode: 128, Stderr: "fatal: bad revision"})
		var exit *runtime.ExitError
		if _, err := (GitUnpushed{Runner: r}).Unpushed(t.Context(), p, "feat/a1"); !errors.As(err, &exit) {
			t.Errorf("Unpushed = %v, want the exit error", err)
		}
	})

	t.Run("AW-12_unknown_branch_is_ErrNoBranch", func(t *testing.T) {
		r := testutil.NewRecordingRunner(t)
		if _, err := (GitUnpushed{Runner: r}).Unpushed(t.Context(), p, ""); !errors.Is(err, ErrNoBranch) {
			t.Errorf("Unpushed = %v, want ErrNoBranch", err)
		}
	})
}
