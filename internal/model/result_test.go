package model_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/polandy/fanin/internal/model"
)

func TestResultNormalize(t *testing.T) {
	t.Run("AW-2_long_blocked_question_is_one_line_cut_to_400", func(t *testing.T) {
		long := strings.Repeat("why ü?\n\tthen ", 200)
		r := model.Result{Kind: model.ResultBlocked, Text: long}.Normalize()
		if strings.ContainsAny(r.Text, "\n\r\t") {
			t.Errorf("text has line breaks or tabs: %q", r.Text)
		}
		if n := utf8.RuneCountInString(r.Text); n != model.FieldCap {
			t.Errorf("text has %d chars, want %d", n, model.FieldCap)
		}
		if !strings.HasPrefix(r.Text, "why ü? then why ü?") {
			t.Errorf("whitespace runs not joined to one space: %q", r.Text[:30])
		}
		if !utf8.ValidString(r.Text) {
			t.Error("cut split a multi-byte char")
		}
	})
	t.Run("short_text_kept", func(t *testing.T) {
		in := model.Result{Kind: model.ResultNothing, Text: "  no open task \n"}
		if got := in.Normalize(); got.Text != "no open task" {
			t.Errorf("got %q", got.Text)
		}
	})
	t.Run("branch_capped_too", func(t *testing.T) {
		in := model.Result{Kind: model.ResultPushed, Branch: "feat/x\n" + strings.Repeat("b", 500)}
		got := in.Normalize()
		if strings.Contains(got.Branch, "\n") || utf8.RuneCountInString(got.Branch) != model.FieldCap {
			t.Errorf("branch not capped to one line of %d: %q", model.FieldCap, got.Branch)
		}
	})
}

func TestResultValidate(t *testing.T) {
	tests := []struct {
		name string
		in   model.Result
		ok   bool
	}{
		{"done_pr", model.Result{Kind: model.ResultDonePR, PR: 7}, true},
		{"done_pr_without_number", model.Result{Kind: model.ResultDonePR}, false},
		{"pushed", model.Result{Kind: model.ResultPushed, Branch: "feat/x"}, true},
		{"pushed_without_branch", model.Result{Kind: model.ResultPushed}, false},
		{"committed", model.Result{Kind: model.ResultCommitted, Branch: "feat/x"}, true},
		{"committed_without_branch", model.Result{Kind: model.ResultCommitted}, false},
		{"issue", model.Result{Kind: model.ResultIssue, Issue: 3}, true},
		{"issue_without_number", model.Result{Kind: model.ResultIssue}, false},
		{"nothing", model.Result{Kind: model.ResultNothing, Text: "queue empty"}, true},
		{"nothing_without_reason", model.Result{Kind: model.ResultNothing}, false},
		{"blocked", model.Result{Kind: model.ResultBlocked, Text: "which DB?"}, true},
		{"blocked_without_question", model.Result{Kind: model.ResultBlocked, Text: " \n "}, false},
		{"unknown_kind", model.Result{Kind: "done"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.ok && !errors.Is(err, model.ErrInvalidResult) {
				t.Fatalf("err = %v, want ErrInvalidResult", err)
			}
		})
	}
}

func TestCapField(t *testing.T) {
	tests := map[string]string{
		"":                "",
		"a\r\nb":          "a b",
		"  a  \t b  ":     "a b",
		"one line stays": "one line stays",
	}
	for in, want := range tests {
		if got := model.CapField(in); got != want {
			t.Errorf("CapField(%q) = %q, want %q", in, got, want)
		}
	}
	if diff := cmp.Diff(strings.Repeat("x", model.FieldCap), model.CapField(strings.Repeat("x", 1000))); diff != "" {
		t.Errorf("cap (-want +got):\n%s", diff)
	}
}
