package model_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/polandy/fanin/internal/model"
)

func TestBudgetAddUsage(t *testing.T) {
	spent := model.Budget{FixRounds: 1, Stalls: 2, InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100,
		ContextPct: 5, SeenInput: 1000, SeenOutput: 100}
	cases := []struct {
		name    string
		in      model.Budget
		u       model.Usage
		want    model.Budget
		changed bool
	}{
		{"AW-14_first_response_is_counted", model.Budget{FixRounds: 1},
			model.Usage{InputTokens: 1000, OutputTokens: 100, ContextPct: 5},
			model.Budget{FixRounds: 1, InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100, ContextPct: 5,
				SeenInput: 1000, SeenOutput: 100}, true},
		{"AW-14_next_response_adds_its_context", spent,
			model.Usage{InputTokens: 3000, OutputTokens: 200, ContextPct: 15},
			model.Budget{FixRounds: 1, Stalls: 2, InputTokens: 4000, OutputTokens: 300, TotalTokens: 4300,
				ContextPct: 15, SeenInput: 3000, SeenOutput: 200}, true},
		{"AW-14_repeated_update_counts_nothing", spent,
			model.Usage{InputTokens: 1000, OutputTokens: 100, ContextPct: 5}, spent, false},
		{"AW-14_repeat_with_a_new_share_only_moves_the_share", spent,
			model.Usage{InputTokens: 1000, OutputTokens: 100, ContextPct: 6},
			func() model.Budget { b := spent; b.ContextPct = 6; return b }(), true},
		{"AW-14_empty_context_after_compact_counts_nothing", spent,
			model.Usage{ContextPct: 0},
			func() model.Budget { b := spent; b.ContextPct = 0; return b }(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := tc.in.Add(tc.u)
			if d := cmp.Diff(tc.want, got); d != "" {
				t.Errorf("budget (-want +got):\n%s", d)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
		})
	}
}

func TestUsageValidate(t *testing.T) {
	for _, u := range []model.Usage{{InputTokens: -1}, {OutputTokens: -1}, {ContextPct: -1}, {ContextPct: 101}} {
		if err := u.Validate(); !errors.Is(err, model.ErrInvalidUsage) {
			t.Errorf("Validate(%+v) = %v, want ErrInvalidUsage", u, err)
		}
	}
	if err := (model.Usage{InputTokens: 1, OutputTokens: 1, ContextPct: 100}).Validate(); err != nil {
		t.Errorf("Validate = %v, want nil", err)
	}
}
