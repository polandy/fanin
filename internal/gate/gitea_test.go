package gate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
)

// see SCENARIOS GT-3, GT-4, GT-5, GT-6
func TestGiteaVerdicts(t *testing.T) {
	ci := check("ci", forge.CheckSuccess, 0)
	withPR := func(edit func(*forge.PR)) forge.PR {
		pr := openPR(ci)
		edit(&pr)
		return pr
	}
	running := pendingFor(gate.WaitChecks)
	tests := []struct {
		name  string
		pr    forge.PR
		files []string
		want  gate.Result
	}{
		{"GT-3_docs_only_is_green_docs", openPR(ci), []string{"docs/user/a.md", "README.md"},
			gate.Result{Verdict: gate.GreenDocs, Title: title}},
		{"GT-3_any_code_file_is_green_code", openPR(ci), []string{docFile, "main.go"},
			gate.Result{Verdict: gate.GreenCode, Title: title}},
		{"GT-3_claude_md_is_green_code", openPR(ci), []string{"CLAUDE.md"},
			gate.Result{Verdict: gate.GreenCode, Title: title}},
		{"GT-4_no_status_yet", openPR([]forge.Check{}...), nil,
			pendingFor(gate.WaitNoChecks)},
		{"GT-4_running_checks", openPR(ci, check("build", forge.CheckPending, 1)), nil, running},
		{"GT-5_failed_check", openPR(ci, check("build", forge.CheckFailure, 1)), nil,
			redFor(gate.CauseCI, "failed: build failure")},
		{"GT-5_conflict_wins_over_running_checks", withPR(func(pr *forge.PR) {
			pr.Mergeable = forge.MergeableConflicted
			pr.Checks = append(pr.Checks, check("build", forge.CheckPending, 1))
		}), nil, redFor(gate.CauseConflict, string(forge.MergeableConflicted))},
		{"GT-5_all_reasons_together", withPR(func(pr *forge.PR) {
			pr.State, pr.BaseBranch, pr.Mergeable = forge.StateClosed, "release", forge.MergeableConflicted
			pr.Checks = append(pr.Checks, check("build", forge.CheckFailure, 1))
		}), nil, redFor(gate.CauseClosed, "PR is CLOSED, not OPEN",
			"PR aims at release, not main", string(forge.MergeableConflicted), "failed: build failure")},
		{"GT-5_merged", withPR(func(pr *forge.PR) { pr.State = forge.StateMerged }), nil,
			redFor(gate.CauseClosed, "PR is MERGED, not OPEN")},
		{"GT-6_mergeable_unknown_is_pending", withPR(func(pr *forge.PR) { pr.Mergeable = forge.MergeableUnknown }),
			nil, pendingFor(gate.WaitMergeable)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := answer(tc.pr)
			f.files = tc.files
			if diff := cmp.Diff(tc.want, mustRead(t, f, giteaTarget())); diff != "" {
				t.Errorf("Read (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGiteaUnreadable(t *testing.T) {
	t.Run("GT-6_pr_unreadable_is_an_error", func(t *testing.T) {
		got, err := gate.Read(context.Background(), failing(forge.ErrUnreadable), giteaTarget())
		if !errors.Is(err, gate.ErrUnreadable) || !errors.Is(err, forge.ErrUnreadable) {
			t.Fatalf("Read err = %v, want gate.ErrUnreadable wrapping forge.ErrUnreadable", err)
		}
		if diff := cmp.Diff(gate.Result{}, got); diff != "" {
			t.Errorf("an unreadable PR gave a verdict (-want +got):\n%s", diff)
		}
	})
	t.Run("GT-6_statuses_unreadable_is_pending", func(t *testing.T) {
		f := failing(errors.Join(forge.ErrChecksUnreadable, errBroken))
		want := pendingFor(gate.WaitChecksUnreadable)
		if diff := cmp.Diff(want, mustRead(t, f, giteaTarget())); diff != "" {
			t.Errorf("Read (-want +got):\n%s", diff)
		}
		if f.fileCall != 0 {
			t.Errorf("Files calls = %d, want 0 before the statuses are read", f.fileCall)
		}
	})
	t.Run("GT-6_files_unreadable_is_pending_never_green", func(t *testing.T) {
		f := answer(openPR(check("ci", forge.CheckSuccess, 0)))
		f.filesErr = forge.ErrNetwork
		want := pendingFor(gate.WaitFilesUnreadable)
		if diff := cmp.Diff(want, mustRead(t, f, giteaTarget())); diff != "" {
			t.Errorf("Read (-want +got):\n%s", diff)
		}
		if f.fileCall != 1 {
			t.Errorf("Files calls = %d, want 1", f.fileCall)
		}
	})
	t.Run("GT-6_non_numeric_pr_dies_before_any_call", func(t *testing.T) {
		n, err := forge.ParseNumber("abc")
		if !errors.Is(err, forge.ErrInvalidRequest) {
			t.Fatalf("ParseNumber err = %v, want forge.ErrInvalidRequest", err)
		}
		f := answer(openPR())
		tg := giteaTarget()
		tg.Number = n
		if _, err := gate.Read(context.Background(), f, tg); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Fatalf("Read err = %v, want forge.ErrInvalidRequest", err)
		}
		if f.prCalls != 0 {
			t.Errorf("PR calls = %d, want 0", f.prCalls)
		}
	})
}

// see SCENARIOS GH-12
func TestDocsOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"GH-12_docs_prefix", []string{"docs/dev/concept.md", "docs/brand/logo.svg"}, true},
		{"GH-12_md_suffix_anywhere", []string{"README.md", "internal/gate/notes.md"}, true},
		{"GH-12_empty_list_is_not_docs", nil, false},
		{"GH-12_code_file", []string{docFile, "internal/gate/gate.go"}, false},
		{"GH-12_claude_md_is_code", []string{"CLAUDE.md"}, false},
		{"GH-12_nested_claude_md_is_code", []string{"docs/CLAUDE.md"}, false},
		{"GH-12_dot_claude_is_code", []string{".claude/skills/pr-review/SKILL.md"}, false},
		{"GH-12_nested_dot_claude_is_code", []string{"docs/.claude/x.md"}, false},
		{"GH-12_docs_lookalike_is_code", []string{"docsx/a.txt", "mydocs/b.txt"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gate.DocsOnly(tc.files); got != tc.want {
				t.Errorf("DocsOnly(%q) = %t, want %t", tc.files, got, tc.want)
			}
		})
	}
}
