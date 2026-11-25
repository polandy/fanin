package hook

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	preGoPath = "/srv/alpha"
	preGoTree = stateDir + "/prego/alpha-42"
	preGoCmd  = "scripts/dry-activate.sh"
	preBranch = "feat/x"
)

func preGoReq() PreGoRequest {
	return PreGoRequest{Project: project, PR: pr, Path: preGoPath, Base: "main", Branch: preBranch, Command: preGoCmd,
		Dir: stateDir}
}

func gitArgv(args ...string) []string { return append([]string{"git", "-C", preGoPath}, args...) }

var (
	fetchArgv  = gitArgv("fetch", "--quiet", "origin", "main", preBranch)
	addArgv    = gitArgv("worktree", "add", "--force", "--detach", preGoTree, "FETCH_HEAD")
	checkArgv  = []string{"sh", "-c", preGoCmd}
	removeArgv = gitArgv("worktree", "remove", "--force", preGoTree)
)

func TestPreGo(t *testing.T) {
	t.Run("GT-8_output_is_joined_and_clipped_and_the_worktree_removed", func(t *testing.T) {
		long := strings.Repeat("x", PreGoOutputMax+10)
		for _, tc := range []struct{ name, out, want string }{
			{"lines", "firewall: none\n\n would: restart \n", "firewall: none; would: restart"},
			{"clipped", long, long[:PreGoOutputMax] + "…"},
			{"empty", "\n", ""},
		} {
			rr := testutil.NewRecordingRunner(t)
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(addArgv, testutil.Reply{})
			rr.Expect(checkArgv, testutil.Reply{Stdout: tc.out})
			rr.Expect(removeArgv, testutil.Reply{})
			got, err := NewPreGo(rr).Run(t.Context(), preGoReq())
			if err != nil || got != tc.want {
				t.Errorf("%s: Run = %q, %v; want %q", tc.name, got, err, tc.want)
			}
			rr.Done()
			env := rr.Calls()[2]
			wantEnv := []string{EnvProject + "=alpha", EnvPR + "=42", EnvBase + "=main", EnvBranch + "=" + preBranch,
				EnvBaseRef + "=origin/main"}
			if diff := cmp.Diff(wantEnv, env.Env); diff != "" || env.Dir != preGoTree {
				t.Errorf("%s: env (-want +got):\n%s\ndir = %q, want %q", tc.name, diff, env.Dir, preGoTree)
			}
		}
	})
	t.Run("GT-8_FP-6_a_failing_check_is_ErrPreGoFailed_and_the_worktree_is_removed", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		rr.Expect(fetchArgv, testutil.Reply{})
		rr.Expect(addArgv, testutil.Reply{})
		rr.Expect(checkArgv, testutil.Reply{ExitCode: 3, Stderr: "step\nbuild failed\n"})
		rr.Expect(removeArgv, testutil.Reply{})
		_, err := NewPreGo(rr).Run(t.Context(), preGoReq())
		if !errors.Is(err, ErrPreGoFailed) || !strings.Contains(err.Error(), "build failed") {
			t.Errorf("Run error = %v, want ErrPreGoFailed naming build failed", err)
		}
		rr.Done()
	})
	t.Run("GT-8_FP-6_git_failures_are_errors_never_an_empty_output", func(t *testing.T) {
		boom := errors.New("network down")
		for _, tc := range []struct {
			name  string
			setup func(rr *testutil.RecordingRunner)
		}{
			{"fetch", func(rr *testutil.RecordingRunner) { rr.Expect(fetchArgv, testutil.Reply{Err: boom}) }},
			{"add", func(rr *testutil.RecordingRunner) {
				rr.Expect(fetchArgv, testutil.Reply{})
				rr.Expect(addArgv, testutil.Reply{Err: boom})
			}},
			{"remove", func(rr *testutil.RecordingRunner) {
				rr.Expect(fetchArgv, testutil.Reply{})
				rr.Expect(addArgv, testutil.Reply{})
				rr.Expect(checkArgv, testutil.Reply{Stdout: "ok"})
				rr.Expect(removeArgv, testutil.Reply{Err: boom})
			}},
		} {
			rr := testutil.NewRecordingRunner(t)
			tc.setup(rr)
			got, err := NewPreGo(rr).Run(t.Context(), preGoReq())
			if !errors.Is(err, boom) || got != "" {
				t.Errorf("%s: Run = %q, %v; want the git error", tc.name, got, err)
			}
			rr.Done()
		}
	})
	t.Run("GT-8_a_cut_off_check_still_removes_the_worktree", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		rr := testutil.NewRecordingRunner(t)
		rr.Expect(fetchArgv, testutil.Reply{})
		rr.Expect(addArgv, testutil.Reply{})
		rr.Expect(checkArgv, testutil.Reply{Err: ctx.Err()})
		rr.Expect(removeArgv, testutil.Reply{})
		if _, err := NewPreGo(rr).Run(ctx, preGoReq()); !errors.Is(err, context.Canceled) {
			t.Errorf("Run error = %v, want context.Canceled", err)
		}
		rr.Done()
	})
	t.Run("an_empty_command_runs_nothing_and_a_bad_request_is_invalid", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		req := preGoReq()
		req.Command = ""
		if got, err := NewPreGo(rr).Run(t.Context(), req); got != "" || err != nil {
			t.Errorf("empty command: Run = %q, %v; want nothing", got, err)
		}
		req = preGoReq()
		req.Branch = ""
		if _, err := NewPreGo(rr).Run(t.Context(), req); !errors.Is(err, ErrInvalid) {
			t.Errorf("no branch: Run error = %v, want ErrInvalid", err)
		}
		rr.Done()
	})
}
