package prompt

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNudges(t *testing.T) {
	t.Run("AW-10_AW-11_every_nudge_renders_and_names_the_report_tool", func(t *testing.T) {
		for _, n := range Nudges {
			out := mustRender(t, n, Vars{Repo: testRepo, Base: testBase})
			if strings.Contains(out, "{{") {
				t.Errorf("%s: placeholder left in %q", n, out)
			}
			if !strings.Contains(out, "fanin_report") {
				t.Errorf("%s: lacks fanin_report: %q", n, out)
			}
		}
	})

	t.Run("AW-11_secure_nudge_asks_for_a_push", func(t *testing.T) {
		if out := mustRender(t, NudgeSecure, Vars{}); !strings.Contains(out, "Push them") {
			t.Errorf("secure nudge = %q, want it to ask for a push", out)
		}
	})

	t.Run("CF-1_conflict_nudge_merges_the_base_and_never_rebases", func(t *testing.T) {
		out := mustRender(t, NudgeConflict, Vars{Base: testBase, Hint: "#42 RED: CONFLICTING"})
		for _, want := range []string{"origin/" + testBase, "never rebase", "#42 RED: CONFLICTING"} {
			if !strings.Contains(out, want) {
				t.Errorf("conflict nudge = %q, want %q", out, want)
			}
		}
	})

	t.Run("GH-9_GH-10_fix_and_review_nudges_carry_the_reasons", func(t *testing.T) {
		for _, n := range []Name{NudgeFix, NudgeReview} {
			if out := mustRender(t, n, Vars{Hint: "#42 RED: failed: ci FAILURE"}); !strings.Contains(out,
				"#42 RED: failed: ci FAILURE") {
				t.Errorf("%s = %q, want the reasons", n, out)
			}
		}
	})
}

func TestHandover(t *testing.T) {
	t.Run("C-BUDGET_full_handover_in_the_fixed_format", func(t *testing.T) {
		h := Handover{FromRun: 2, Reason: "token budget spent: 3000000 of 3000000 tokens", State: "tests green",
			Open: []string{"PR #7 is open; carry it on instead of opening a new one."}, Branch: "feat/parser",
			Errors: []string{"run t1-run-1: stalled: quiet"}, Tail: "$ make test\nok\n"}
		want := "## Handover from run 2\n\n" +
			"Run 2 of this task ended: token budget spent: 3000000 of 3000000 tokens. You are its fresh run: carry " +
			"on from this state on the same branch and do not start over.\n\n" +
			"### State\n\ntests green\n\n" +
			"### Open\n\n- PR #7 is open; carry it on instead of opening a new one.\n\n" +
			"### Branch\n\n`feat/parser`\n\n" +
			"### Last errors\n\n- run t1-run-1: stalled: quiet\n\n" +
			"### Tail\n\n```text\n$ make test\nok\n```\n"
		if d := cmp.Diff(want, h.Markdown()); d != "" {
			t.Errorf("handover (-want +got):\n%s", d)
		}
	})

	t.Run("C-BUDGET_empty_sections_say_so", func(t *testing.T) {
		out := Handover{FromRun: 1, Reason: "context 80% full"}.Markdown()
		for _, want := range []string{NoState, "- " + NoneOpen, NoBranch, "- " + NoErrors, "### Tail\n\n" + NoTail} {
			if !strings.Contains(out, want) {
				t.Errorf("handover lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("C-BUDGET_tail_cannot_close_its_fence", func(t *testing.T) {
		out := Handover{FromRun: 1, Reason: "r", Tail: "a ```` b"}.Markdown()
		if !strings.Contains(out, "`````text\na ```` b\n`````\n") {
			t.Errorf("tail fence too short:\n%s", out)
		}
	})

	t.Run("C-BUDGET_handover_is_appended_literally", func(t *testing.T) {
		ho := Handover{FromRun: 1, Reason: "r", State: "{{REPO}} {{INCLUDE:owner-input}}"}.Markdown()
		out := mustRender(t, Worker, Vars{Repo: testRepo, Base: testBase, Hint: "fix it", Handover: ho})
		base := mustRender(t, Worker, vars)
		if want := base + "\n" + ho; out != want {
			t.Errorf("prompt with handover (-want +got):\n%s", cmp.Diff(want, out))
		}
	})
}
