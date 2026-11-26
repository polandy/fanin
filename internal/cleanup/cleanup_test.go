package cleanup

import (
	"context"
	"errors"
	"testing"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	repoPath = "/srv/alpha"
	tree     = "/srv/alpha-fix"
	branch   = "fix/x"
)

var errBoom = errors.New("boom")

type fakeForge struct {
	head      string
	headErr   error
	deleteErr error
	deleted   []string
}

func (f *fakeForge) HeadBranch(context.Context, config.Project, int) (string, error) {
	return f.head, f.headErr
}

func (f *fakeForge) DeleteBranch(_ context.Context, _ config.Project, b string) error {
	f.deleted = append(f.deleted, b)
	return f.deleteErr
}

func git(args ...string) []string { return append([]string{"git", "-C", repoPath}, args...) }

func listing(trees ...string) string {
	out := "worktree " + repoPath + "\nHEAD aaa\nbranch refs/heads/main\n\n"
	for _, t := range trees {
		out += "worktree " + t + "\nHEAD bbb\nbranch refs/heads/" + branch + "\n\n"
	}
	return out
}

func TestCleanup(t *testing.T) {
	p := config.Project{Name: "alpha", Path: repoPath}
	list := git("worktree", "list", "--porcelain")
	show := git("show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	rm := git("worktree", "remove", tree)
	del := git("branch", "-D", branch)
	prune := git("remote", "prune", "origin")
	wtPrune := git("worktree", "prune")
	ok := testutil.Reply{}

	tests := []struct {
		name   string
		forge  fakeForge
		script []struct {
			argv  []string
			reply testutil.Reply
		}
		wantDeleted bool
		wantErr     error
	}{
		{
			name:  "MG-7_deletes_the_remote_branch_the_worktree_and_the_local_branch",
			forge: fakeForge{head: branch},
			script: []struct {
				argv  []string
				reply testutil.Reply
			}{{list, testutil.Reply{Stdout: listing(tree)}}, {rm, ok}, {wtPrune, ok}, {show, ok}, {del, ok}, {prune, ok}},
			wantDeleted: true,
		},
		{
			name:  "MG-7_remote_branch_already_gone_is_fine",
			forge: fakeForge{head: branch, deleteErr: forge.ErrNotFound},
			script: []struct {
				argv  []string
				reply testutil.Reply
			}{{list, testutil.Reply{Stdout: listing(tree)}}, {rm, ok}, {wtPrune, ok}, {show, ok}, {del, ok}, {prune, ok}},
			wantDeleted: true,
		},
		{
			name:  "MG-7_worktree_and_local_branch_already_gone_are_fine",
			forge: fakeForge{head: branch},
			script: []struct {
				argv  []string
				reply testutil.Reply
			}{{list, testutil.Reply{Stdout: listing()}}, {wtPrune, ok}, {show, testutil.Reply{ExitCode: 1}}, {prune, ok}},
			wantDeleted: true,
		},
		{
			name:  "MG-7_dirty_worktree_stays_with_its_branch_and_is_reported",
			forge: fakeForge{head: branch},
			script: []struct {
				argv  []string
				reply testutil.Reply
			}{{list, testutil.Reply{Stdout: listing(tree)}}, {rm, testutil.Reply{ExitCode: 128, Stderr: "contains modified files"}},
				{wtPrune, ok}, {prune, ok}},
			wantDeleted: true,
			wantErr:     ErrStepFailed,
		},
		{
			name:  "MG-7_a_failed_remote_delete_does_not_stop_the_local_steps",
			forge: fakeForge{head: branch, deleteErr: errBoom},
			script: []struct {
				argv  []string
				reply testutil.Reply
			}{{list, testutil.Reply{Stdout: listing(tree)}}, {rm, ok}, {wtPrune, ok}, {show, ok}, {del, ok}, {prune, ok}},
			wantDeleted: true,
			wantErr:     errBoom,
		},
		{
			name:        "MG-7_an_unreadable_head_branch_cleans_nothing",
			forge:       fakeForge{headErr: errBoom},
			wantErr:     errBoom,
			wantDeleted: false,
		},
		{
			name:  "MG-7_an_unreadable_worktree_list_still_tries_the_branch",
			forge: fakeForge{head: branch},
			script: []struct {
				argv  []string
				reply testutil.Reply
			}{{list, testutil.Reply{ExitCode: 128, Stderr: "not a repo"}}, {wtPrune, ok}, {show, ok}, {del, ok}, {prune, ok}},
			wantDeleted: true,
			wantErr:     ErrStepFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := testutil.NewRecordingRunner(t)
			for _, s := range tc.script {
				r.Expect(s.argv, s.reply)
			}
			f := tc.forge
			err := New(r, &f).Cleanup(t.Context(), p, 42)
			if tc.wantErr == nil && err != nil {
				t.Errorf("Cleanup = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("Cleanup = %v, want %v", err, tc.wantErr)
			}
			if got := len(f.deleted) == 1; got != tc.wantDeleted {
				t.Errorf("deleted = %v, want deleted=%v", f.deleted, tc.wantDeleted)
			}
		})
	}
}
