package worktree

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	repoPath = "/srv/alpha"
	tree     = "/srv/alpha-7-retry-the-gate"
	branch   = "fix/7-retry-the-gate"
)

func TestFor(t *testing.T) {
	p := config.Project{Name: "alpha", Path: repoPath, Base: "main"}
	tests := []struct {
		name     string
		project  config.Project
		task     string
		analysis bool
		text     string
		want     Tree
	}{
		{name: "SP-12_type_and_slug_from_a_conventional_hint", project: p, task: "alpha-7",
			text: "fix(gate): retry the gate on a 502", want: Tree{Path: tree, Branch: branch}},
		{name: "SP-12_a_hint_without_a_known_type_is_a_feat", project: p, task: "alpha-8",
			text: "Add a Dark Mode!", want: Tree{Path: "/srv/alpha-8-add-a-dark-mode", Branch: "feat/8-add-a-dark-mode"}},
		{name: "SP-12_an_unknown_prefix_stays_in_the_slug", project: p, task: "alpha-9", text: "wip: thing",
			want: Tree{Path: "/srv/alpha-9-wip-thing", Branch: "feat/9-wip-thing"}},
		{name: "SP-12_an_analysis_is_a_docs_branch", project: p, task: "alpha-3", analysis: true,
			text: "Why is CI slow?", want: Tree{Path: "/srv/alpha-3-analysis-why-is-ci", Branch: "docs/3-analysis-why-is-ci"}},
		{name: "SP-12_no_text_leaves_the_task_number", project: p, task: "alpha-4",
			want: Tree{Path: "/srv/alpha-4", Branch: "feat/4"}},
		{name: "SP-12_a_foreign_task_id_is_kept_whole", project: p, task: "chain-x-2", text: "docs: readme",
			want: Tree{Path: "/srv/alpha-chain-x-2-readme", Branch: "docs/chain-x-2-readme"}},
		{name: "SP-12_the_configured_worktree_dir", task: "alpha-5", text: "test: cover it",
			project: config.Project{Name: "alpha", Path: repoPath, WorktreeDir: "/work/trees"},
			want:    Tree{Path: "/work/trees/alpha-5-cover-it", Branch: "test/5-cover-it"}},
		{name: "SP-12_a_long_text_is_cut_at_a_word", project: p, task: "alpha-6",
			text: "refactor: internationalization localization accessibility observability",
			want: Tree{Path: "/srv/alpha-6-internationalization-localization",
				Branch: "refactor/6-internationalization-localization"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := For(tc.project, tc.task, tc.analysis, tc.text)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("For (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("SP-12_two_tasks_of_one_project_never_share_a_worktree", func(t *testing.T) {
		a, b := For(p, "alpha-1", false, "fix: same text"), For(p, "alpha-2", false, "fix: same text")
		if a.Path == b.Path || a.Branch == b.Branch {
			t.Errorf("For = %+v and %+v, want distinct paths and branches", a, b)
		}
	})
}

func git(args ...string) []string { return append([]string{"git", "-C", repoPath}, args...) }

func listing(entries ...[2]string) string {
	out := "worktree " + repoPath + "\nHEAD aaa\nbranch refs/heads/main\n\n"
	for _, e := range entries {
		out += "worktree " + e[0] + "\nHEAD bbb\nbranch refs/heads/" + e[1] + "\n\n"
	}
	return out
}

func TestPrepare(t *testing.T) {
	p := config.Project{Name: "alpha", Path: repoPath, Base: "main"}
	list := git("worktree", "list", "--porcelain")
	fetch := git("fetch", "--quiet", "origin", "main")
	show := git("show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	add := git("worktree", "add", "-b", branch, tree, "origin/main")
	ok := testutil.Reply{}
	missing := testutil.Reply{ExitCode: 1}
	type step struct {
		argv  []string
		reply testutil.Reply
	}
	tests := []struct {
		name    string
		script  []step
		wantErr error
	}{
		{name: "SP-12_adds_the_worktree_on_a_new_branch_from_the_fresh_base",
			script: []step{{list, testutil.Reply{Stdout: listing()}}, {show, missing}, {fetch, ok}, {add, ok}}},
		{name: "SP-12_a_follow_up_run_reuses_its_worktree",
			script: []step{{list, testutil.Reply{Stdout: listing([2]string{tree, branch})}}}},
		{name: "SP-12_a_path_on_another_branch_is_refused",
			script:  []step{{list, testutil.Reply{Stdout: listing([2]string{tree, "fix/other"})}}},
			wantErr: ErrInUse},
		{name: "SP-12_a_branch_checked_out_elsewhere_is_refused",
			script:  []step{{list, testutil.Reply{Stdout: listing([2]string{"/srv/alpha-old", branch})}}},
			wantErr: ErrInUse},
		{name: "SP-12_an_existing_branch_is_refused",
			script:  []step{{list, testutil.Reply{Stdout: listing()}}, {show, ok}},
			wantErr: ErrBranchExists},
		{name: "SP-12_a_failed_add_fails_the_prepare",
			script: []step{{list, testutil.Reply{Stdout: listing()}}, {show, missing}, {fetch, ok},
				{add, testutil.Reply{ExitCode: 128, Stderr: "fatal: '" + tree + "' already exists"}}},
			wantErr: ErrGit},
		{name: "SP-12_a_full_disk_fails_the_prepare",
			script: []step{{list, testutil.Reply{Stdout: listing()}}, {show, missing}, {fetch, ok},
				{add, testutil.Reply{ExitCode: 128, Stderr: "fatal: No space left on device"}}},
			wantErr: ErrGit},
		{name: "SP-12_a_failed_fetch_adds_nothing",
			script: []step{{list, testutil.Reply{Stdout: listing()}}, {show, missing},
				{fetch, testutil.Reply{ExitCode: 128, Stderr: "could not read from remote"}}},
			wantErr: ErrGit},
		{name: "SP-12_an_unreadable_worktree_list_adds_nothing",
			script:  []step{{list, testutil.Reply{ExitCode: 128, Stderr: "not a git repository"}}},
			wantErr: ErrGit},
		{name: "SP-12_an_unreadable_branch_check_adds_nothing",
			script:  []step{{list, testutil.Reply{Stdout: listing()}}, {show, testutil.Reply{Err: errors.New("no git")}}},
			wantErr: ErrGit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := testutil.NewRecordingRunner(t)
			for _, s := range tc.script {
				r.Expect(s.argv, s.reply)
			}
			err := New(r).Prepare(t.Context(), p, Tree{Path: tree, Branch: branch})
			if tc.wantErr == nil && err != nil {
				t.Errorf("Prepare = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("Prepare = %v, want %v", err, tc.wantErr)
			}
			r.Done()
		})
	}
}
