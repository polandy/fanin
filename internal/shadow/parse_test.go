package shadow

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

// states returns agent=state for each parsed task, in parse order.
func states(tasks []Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Agent+"="+string(t.State))
	}
	return out
}

func TestParseLog(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want []string
	}{
		{"SH-1_spawn_is_running", "2026-01-02 03:04 spawned alpha-1 (sonnet): task prompt 1\n",
			[]string{"alpha-1=running"}},
		{"SH-1_pr_is_gating", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n2026-01-02 03:05 pr #7 alpha-1 feat/x\n",
			[]string{"alpha-1=gating"}},
		{"SH-1_merged_is_done", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n2026-01-02 03:05 pr #7 alpha-1 feat/x\n" +
			"2026-01-02 03:06 merged #7 feat: change 1\n", []string{"alpha-1=done"}},
		{"SH-1_blocked_is_needs_owner", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n" +
			"2026-01-02 03:05 blocked alpha-1: dialog: question 1\n", []string{"alpha-1=needs_owner"}},
		{"SH-1_ready_is_needs_owner", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n2026-01-02 03:05 pr #7 alpha-1 fix/x\n" +
			"2026-01-02 03:06 ready #7 green; note 1\n", []string{"alpha-1=needs_owner"}},
		{"SH-1_paused", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n2026-01-02 03:05 pr #7 alpha-1 fix/x\n" +
			"2026-01-02 03:06 paused #7: note 1\n", []string{"alpha-1=paused"}},
		{"SH-1_idle_is_done", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n2026-01-02 03:05 idle alpha-1: note 1\n",
			[]string{"alpha-1=done"}},
		{"SH-1_blocked_then_pr_is_gating", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n" +
			"2026-01-02 03:05 blocked alpha-1: note\n2026-01-02 03:06 owner answer for alpha-1: answer 1\n" +
			"2026-01-02 03:07 pr #7 alpha-1 feat/x\n", []string{"alpha-1=gating"}},
		{"SH-1_new_spawn_cancels_the_lane's_open_task", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n" +
			"2026-01-02 03:05 blocked alpha-1: note\n2026-01-02 03:06 spawned alpha-2 (opus): p\n",
			[]string{"alpha-1=canceled", "alpha-2=running"}},
		{"SH-1_new_spawn_keeps_a_ready_task", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n" +
			"2026-01-02 03:05 pr #7 alpha-1 feat/x\n2026-01-02 03:06 ready #7 green; note\n" +
			"2026-01-02 03:07 spawned alpha-2 (opus): p\n", []string{"alpha-1=needs_owner", "alpha-2=running"}},
		{"SH-1_lanes_are_separate", "2026-01-02 03:04 spawned alpha-b-1 (sonnet): p [lane b]\n" +
			"2026-01-02 03:05 spawned alpha-c-1 (sonnet): p [lane c]\n2026-01-02 03:06 blocked alpha-c-1: dialog [lane c]\n",
			[]string{"alpha-b-1=running", "alpha-c-1=needs_owner"}},
		{"SH-1_done_is_final", "2026-01-02 03:04 spawned alpha-1 (sonnet): p\n2026-01-02 03:05 pr #7 alpha-1 feat/x\n" +
			"2026-01-02 03:06 merged #7 feat: c\n2026-01-02 03:07 blocked alpha-1: late\n2026-01-02 03:08 spawned alpha-2 (sonnet): p\n",
			[]string{"alpha-1=done", "alpha-2=running"}},
		{"SH-1_helpers_notes_and_headers_are_skipped", "machine: host-a\n2026-01-02 orchestrator: note 1\n" +
			"01-02 03:04 spawned alpha-9 (sonnet): cut\n2026-01-02 03:04 spawned helper-1 (host-a w1:p1): note\n" +
			"2026-01-02 03:05 spawned beta-1 (sonnet): another project\n2026-01-02 03:06 merged #99 feat: unknown pr\n" +
			"2026-01-02 03:07 blocked alpha-3: unknown agent\n", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks, err := ParseLog("alpha", []byte(tc.log))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, states(tasks)); diff != "" {
				t.Errorf("states (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseLogFields(t *testing.T) {
	log := "2026-01-02 03:04 spawned alpha-c-4 (opus, read-only): owner task: task prompt 1 [lane c]\n" +
		"2026-01-02 03:05 pr #12 alpha-c-4 feat/lane-c-change-2 [lane c]\n"
	tasks, err := ParseLog("Alpha", []byte(log))
	if err != nil {
		t.Fatal(err)
	}
	want := []Task{{
		ID: "shadow-alpha-c-4", Agent: "alpha-c-4", Lane: "c", Model: "opus", Prompt: "owner task: task prompt 1",
		OwnerTask: true, PR: 12, Branch: "feat/lane-c-change-2", State: model.TaskGating,
		Line: "2026-01-02 03:05 pr #12 alpha-c-4 feat/lane-c-change-2 [lane c]",
	}}
	if diff := cmp.Diff(want, tasks); diff != "" {
		t.Errorf("tasks (-want +got):\n%s", diff)
	}
}

func TestParseLogRefusesNonUTF8(t *testing.T) {
	// see SCENARIOS SH-4
	_, err := ParseLog("alpha", []byte("2026-01-02 03:04 spawned alpha-1 (sonnet): \xff\n"))
	if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("err = %v, want ErrUnreadable naming the label", err)
	}
}
