package faninmcp

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseStatusLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Usage
	}{
		{"AW-14_context_tokens_and_share", `{"session_id":"s","model":{"id":"x"},"context_window":{
			"total_input_tokens":15500,"total_output_tokens":1200,"context_window_size":200000,"used_percentage":8,
			"remaining_percentage":92,"current_usage":{"input_tokens":8500}}}`,
			Usage{InputTokens: 15500, OutputTokens: 1200, ContextPct: 8}},
		{"AW-14_fractional_share_rounds", `{"context_window":{"total_input_tokens":1,"used_percentage":74.6}}`,
			Usage{InputTokens: 1, ContextPct: 75}},
		{"AW-14_null_share_is_computed_from_the_input", `{"context_window":{"total_input_tokens":150000,
			"total_output_tokens":10,"context_window_size":200000,"used_percentage":null,"current_usage":null}}`,
			Usage{InputTokens: 150000, OutputTokens: 10, ContextPct: 75}},
		{"AW-14_share_over_the_window_is_capped", `{"context_window":{"total_input_tokens":250000,
			"context_window_size":200000}}`, Usage{InputTokens: 250000, ContextPct: 100}},
		{"AW-14_before_the_first_response_is_empty", `{"session_id":"s"}`, Usage{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStatusLine([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(tc.want, got); d != "" {
				t.Errorf("usage (-want +got):\n%s", d)
			}
		})
	}
	for _, in := range []string{"", "not json", `{"context_window":{"used_percentage":"high"}}`} {
		if _, err := ParseStatusLine([]byte(in)); !errors.Is(err, ErrStatusLine) {
			t.Errorf("ParseStatusLine(%q) = %v, want ErrStatusLine", in, err)
		}
	}
}
