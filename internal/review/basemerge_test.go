package review_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/review"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const mergeTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

var (
	mergeParents   = testutil.Reply{Stdout: headSHA + " " + parentSHA + " " + baseSHA + "\n"}
	fetchBaseArgv  = []string{gitBin, "-C", repoDir, "fetch", "-q", "origin", "main"}
	ancestorArgv   = []string{gitBin, "-C", repoDir, "merge-base", "--is-ancestor", baseSHA, "origin/main"}
	mergeTreeArgv  = []string{gitBin, "-C", repoDir, "merge-tree", "--write-tree", parentSHA, baseSHA}
	headTreeArgv   = []string{gitBin, "-C", repoDir, "rev-parse", headSHA + "^{tree}"}
	cleanMergeTree = testutil.Reply{Stdout: mergeTree + "\n"}
)

// reviewedParent is a forge whose PR head merges main into parentSHA, on which a review posted state.
func reviewedParent(state forge.StatusState, comments ...[]forge.Comment) *fakeForge {
	f := newForge(comments...)
	f.statuses = []forge.Status{{Repo: repo, SHA: parentSHA, Context: gate.ReviewContext, State: state}}
	return f
}

// scriptBaseMerge queues the checks of a head that merges main into parentSHA, up to its own tree.
func scriptBaseMerge(rr *testutil.RecordingRunner, headTree testutil.Reply) {
	rr.Expect(fetchArgv, testutil.Reply{})
	rr.Expect(parentsArgv, mergeParents)
	rr.Expect(fetchBaseArgv, testutil.Reply{})
	rr.Expect(ancestorArgv, testutil.Reply{})
	rr.Expect(mergeTreeArgv, cleanMergeTree)
	rr.Expect(headTreeArgv, headTree)
}

// scriptRun queues the review run after the base-merge checks: worktree, one claude run, removal.
func scriptRun(rr *testutil.RecordingRunner) {
	rr.Expect(addArgv, testutil.Reply{})
	rr.Expect(claudeArgv, testutil.Reply{Stdout: okJSON})
	rr.Expect(removeArgv, testutil.Reply{})
}

func TestReviewOC3BaseMerge(t *testing.T) {
	t.Run("OC-3_clean_merge_of_the_base_into_a_ready_head_is_ready_without_a_run", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptBaseMerge(rr, cleanMergeTree)
		f := reviewedParent(forge.StatusSuccess)
		s := newSetup(rr, f)
		got, err := s.reviewer().Review(context.Background(), request())
		if err != nil {
			t.Fatalf("Review: %v", err)
		}
		rr.Done()
		want := review.Result{HeadSHA: headSHA, Verdict: review.Ready, ReviewedHead: parentSHA,
			Text: review.BaseMergeText(parentSHA)}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Result (-want +got):\n%s", diff)
		}
		wantSt := forge.Status{Repo: repo, SHA: headSHA, Context: gate.ReviewContext, State: forge.StatusSuccess,
			Description: review.BaseMergeText(parentSHA)}
		if diff := cmp.Diff(wantSt, f.posted()[1]); len(f.posted()) != 2 || diff != "" {
			t.Errorf("posted %+v (-want +got):\n%s", f.posted(), diff)
		}
		if r := headGate(t, f); !r.Verdict.IsGreen() {
			t.Errorf("gate = %s, want GREEN: CI still gates, the review carries over", r)
		}
		if ev := s.events.all(); len(ev) != 0 {
			t.Errorf("a carried review recorded %+v, want no usage", ev)
		}
	})
	t.Run("OC-3_a_head_with_own_changes_is_reviewed", func(t *testing.T) {
		rr := testutil.NewRecordingRunner(t)
		scriptOK(rr, testutil.Reply{Stdout: okJSON})
		f := reviewedParent(forge.StatusSuccess, nil, []forge.Comment{comment(9, readyBody)})
		got, err := newSetup(rr, f).reviewer().Review(context.Background(), request())
		if err != nil || got.ReviewedHead != "" || got.CommentURL != commentURL {
			t.Errorf("Review = %+v, %v; want a run of its own", got, err)
		}
		rr.Done()
	})
	reviewed := []struct {
		name   string
		state  forge.StatusState
		script func(rr *testutil.RecordingRunner)
	}{
		{"OC-3_a_merge_into_a_not_ready_head_is_reviewed", forge.StatusFailure, func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, mergeParents)
		}},
		{"OC-3_a_merge_into_an_unreviewed_head_is_reviewed", "", func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, mergeParents)
		}},
		{"OC-3_an_octopus_merge_is_reviewed", forge.StatusSuccess, func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, testutil.Reply{Stdout: headSHA + " " + parentSHA + " " + baseSHA + " " + newSHA})
		}},
		{"OC-3_a_merge_of_a_branch_that_is_not_the_base_is_reviewed", forge.StatusSuccess,
			func(rr *testutil.RecordingRunner) {
				rr.Expect(fetchArgv, testutil.Reply{})
				rr.Expect(parentsArgv, mergeParents)
				rr.Expect(fetchBaseArgv, testutil.Reply{})
				rr.Expect(ancestorArgv, testutil.Reply{ExitCode: 1})
			}},
		{"OC-3_a_merge_with_conflicts_is_reviewed", forge.StatusSuccess, func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, mergeParents)
			rr.Expect(fetchBaseArgv, testutil.Reply{})
			rr.Expect(ancestorArgv, testutil.Reply{})
			rr.Expect(mergeTreeArgv, testutil.Reply{ExitCode: 1, Stdout: mergeTree + "\nCONFLICT (content)\n"})
		}},
		{"OC-3_a_merge_with_own_changes_in_it_is_reviewed", forge.StatusSuccess, func(rr *testutil.RecordingRunner) {
			scriptBaseMerge(rr, testutil.Reply{Stdout: "1111111111111111111111111111111111111111\n"})
		}},
	}
	for _, tc := range reviewed {
		t.Run(tc.name, func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			tc.script(rr)
			scriptRun(rr)
			f := newForge(nil, []forge.Comment{comment(9, readyBody)})
			if tc.state != "" {
				f = reviewedParent(tc.state, nil, []forge.Comment{comment(9, readyBody)})
			}
			got, err := newSetup(rr, f).reviewer().Review(context.Background(), request())
			if err != nil || got.ReviewedHead != "" || got.CommentURL != commentURL {
				t.Errorf("Review = %+v, %v; want a run of its own", got, err)
			}
			rr.Done()
		})
	}
}

func TestReviewOC3BaseMergeFailures(t *testing.T) {
	failures := []struct {
		name   string
		script func(rr *testutil.RecordingRunner)
		forge  func(f *fakeForge)
		want   error
	}{
		{"parents_unreadable", func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, testutil.Reply{ExitCode: 128, Stderr: "fatal: bad object"})
		}, nil, nil},
		{"parents_of_another_commit", func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, testutil.Reply{Stdout: newSHA + " " + parentSHA + "\n"})
		}, nil, review.ErrUnreadable},
		{"parent_checks_unreadable", func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, mergeParents)
		}, func(f *fakeForge) { f.commitChecksErr = forge.ErrNetwork }, forge.ErrNetwork},
		{"ancestry_unreadable", func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, mergeParents)
			rr.Expect(fetchBaseArgv, testutil.Reply{})
			rr.Expect(ancestorArgv, testutil.Reply{ExitCode: 128, Stderr: "fatal: not a valid object"})
		}, nil, nil},
		{"merge_tree_fails", func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, mergeParents)
			rr.Expect(fetchBaseArgv, testutil.Reply{})
			rr.Expect(ancestorArgv, testutil.Reply{})
			rr.Expect(mergeTreeArgv, testutil.Reply{ExitCode: 129, Stderr: "usage: git merge-tree"})
		}, nil, nil},
		{"merge_tree_without_a_tree", func(rr *testutil.RecordingRunner) {
			rr.Expect(fetchArgv, testutil.Reply{})
			rr.Expect(parentsArgv, mergeParents)
			rr.Expect(fetchBaseArgv, testutil.Reply{})
			rr.Expect(ancestorArgv, testutil.Reply{})
			rr.Expect(mergeTreeArgv, testutil.Reply{})
		}, nil, review.ErrUnreadable},
		{"post_fails", func(rr *testutil.RecordingRunner) {
			scriptBaseMerge(rr, cleanMergeTree)
		}, func(f *fakeForge) { f.postErr = forge.ErrNetwork }, forge.ErrNetwork},
	}
	for _, tc := range failures {
		t.Run("OC-3_"+tc.name+"_is_an_error_and_posts_nothing", func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			tc.script(rr)
			f := reviewedParent(forge.StatusSuccess)
			if tc.forge != nil {
				tc.forge(f)
			}
			_, err := newSetup(rr, f).reviewer().Review(context.Background(), request())
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			rr.Done()
			if n := len(f.posted()); n != 1 {
				t.Errorf("posted %+v, want only the parent's review", f.posted())
			}
		})
	}
}
