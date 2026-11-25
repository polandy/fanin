package gate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
)

func TestGitHubVerdicts(t *testing.T) {
	ci := check("ci", forge.CheckSuccess, 0)
	ready := review(forge.CheckSuccess, 5, "")
	withPR := func(edit func(*forge.PR)) forge.PR {
		pr := openPR()
		edit(&pr)
		return pr
	}
	tests := []struct {
		name string
		pr   forge.PR
		want gate.Result
	}{
		{"GH-1_open_mergeable_checks_passed_review_ready", openPR(), gate.Result{Verdict: gate.Green, Title: title}},
		{"GH-1_skipped_check_is_fine", openPR(ci, check("docs", forge.CheckSkipped, 1), ready),
			gate.Result{Verdict: gate.Green, Title: title}},
		{"GH-2_no_review_for_this_head", openPR(ci),
			redFor("no review for head " + headSHA)},
		{"GH-2_verdict_not_ready", openPR(ci, review(forge.CheckFailure, 5, "tests miss GH-3")),
			redFor("review: not ready: tests miss GH-3")},
		{"GH-2_not_ready_without_text", openPR(ci, review(forge.CheckFailure, 5, "")),
			redFor("review: not ready")},
		{"GH-2_no_verdict", openPR(ci, review(forge.CheckPending, 5, "")),
			redFor("review has no verdict")},
		{"GH-2_only_the_newest_review_counts_red", openPR(ci, review(forge.CheckSuccess, 5, ""),
			review(forge.CheckFailure, 9, "regressed")),
			redFor("review: not ready: regressed")},
		{"GH-2_only_the_newest_review_counts_green", openPR(ci, review(forge.CheckSuccess, 9, ""),
			review(forge.CheckFailure, 5, "old")),
			gate.Result{Verdict: gate.Green, Title: title}},
		{"GH-3_checks_running", openPR(ci, check("lint", forge.CheckPending, 1), review(forge.CheckFailure, 5, "x")),
			pendingFor(gate.WaitChecks)},
		{"GH-3_no_checks_yet", openPR(ready),
			pendingFor(gate.WaitNoChecks)},
		{"GH-3_mergeable_unknown", withPR(func(pr *forge.PR) { pr.Mergeable = forge.MergeableUnknown }),
			pendingFor(gate.WaitMergeable)},
		{"GH-4_failed_check_ahead_of_running_with_review_reasons", openPR(check("lint", forge.CheckPending, 1),
			check("ci", forge.CheckFailure, 2), check("e2e", forge.CheckCancelled, 3), review(forge.CheckFailure, 5, "no docs")),
			redFor("failed: ci failure, e2e cancelled", "review: not ready: no docs")},
		{"GH-4_failed_check_with_ready_review", openPR(check("ci", forge.CheckFailure, 2), ready),
			redFor("failed: ci failure")},
		{"GH-4_conflicting", withPR(func(pr *forge.PR) { pr.Mergeable = forge.MergeableConflicted }),
			redFor(string(forge.MergeableConflicted))},
		{"GH-4_not_open", withPR(func(pr *forge.PR) { pr.State = forge.StateMerged }),
			redFor("PR is MERGED, not OPEN")},
		{"FP-6_unknown_check_state_is_failed", openPR(check("ci", "weird", 0), ready),
			redFor("failed: ci weird")},
		{"FP-6_unknown_mergeable_is_never_green", withPR(func(pr *forge.PR) { pr.Mergeable = "" }),
			pendingFor(gate.WaitMergeable)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, mustRead(t, answer(tc.pr), githubTarget())); diff != "" {
				t.Errorf("Read (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGitHubUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"GH-5_pr_unreadable", forge.ErrUnreadable},
		{"GH-5_review_time_unreadable", errors.Join(forge.ErrUnreadable, errBroken)},
		{"GH-5_network", forge.ErrNetwork},
		{"GH-5_not_found", forge.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gate.Read(context.Background(), failing(tc.err), githubTarget())
			if !errors.Is(err, gate.ErrUnreadable) || !errors.Is(err, tc.err) {
				t.Fatalf("Read err = %v, want gate.ErrUnreadable wrapping %v", err, tc.err)
			}
			if diff := cmp.Diff(gate.Result{}, got); diff != "" {
				t.Errorf("an unreadable gate gave a verdict (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReadRefusesBadTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		tg   gate.Target
	}{
		{"GT-6_no_pr_number", gate.Target{Kind: forge.KindGitea, Repo: repo}},
		{"GT-6_no_repo", gate.Target{Kind: forge.KindGitHub, Number: number}},
		{"unknown_kind", gate.Target{Kind: "svn", Repo: repo, Number: number}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := answer(openPR())
			if _, err := gate.Read(context.Background(), f, tc.tg); !errors.Is(err, forge.ErrInvalidRequest) {
				t.Fatalf("Read err = %v, want forge.ErrInvalidRequest", err)
			}
			if f.prCalls+f.fileCall != 0 {
				t.Errorf("forge calls = %d PR + %d Files, want none", f.prCalls, f.fileCall)
			}
		})
	}
}

func TestResultString(t *testing.T) {
	for _, tc := range []struct {
		res  gate.Result
		want string
	}{
		{gate.Result{Verdict: gate.Green, Title: title}, "GREEN " + title},
		{gate.Result{Verdict: gate.GreenDocs, Title: title}, "GREEN docs " + title},
		{gate.Result{Verdict: gate.Pending, Reasons: []string{"no checks"}}, "PENDING: no checks"},
		{redFor("a", "b"), "RED: a; b"},
		{gate.Result{Verdict: gate.Red}, "RED"},
	} {
		if got := tc.res.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
	for _, v := range gate.AllVerdicts {
		if want := v == gate.Green || v == gate.GreenDocs || v == gate.GreenCode; v.IsGreen() != want {
			t.Errorf("%s.IsGreen() = %t, want %t", v, v.IsGreen(), want)
		}
	}
}
