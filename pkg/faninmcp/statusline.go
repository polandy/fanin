package faninmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// ErrStatusLine is a status-line input that is no JSON object of Claude Code's status shape.
var ErrStatusLine = errors.New("faninmcp: unreadable status line input")

// pctScale turns a token share into a percentage.
const pctScale = 100

// Usage is what a worker's status line sends: its context tokens after the latest response and the context share.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	ContextPct   int   `json:"context_pct"`
}

// statusInput is the part of Claude Code's status-line JSON that fanin reads; other fields are ignored.
type statusInput struct {
	ContextWindow *struct {
		TotalInputTokens  int64    `json:"total_input_tokens"`
		TotalOutputTokens int64    `json:"total_output_tokens"`
		ContextWindowSize int64    `json:"context_window_size"`
		UsedPercentage    *float64 `json:"used_percentage"`
	} `json:"context_window"`
}

// ParseStatusLine reads the JSON Claude Code gives a status-line command; without a context window yet it is the
// zero Usage. A null share is computed from the input tokens and the window size.
func ParseStatusLine(data []byte) (Usage, error) {
	var in statusInput
	if err := json.Unmarshal(data, &in); err != nil {
		return Usage{}, fmt.Errorf("%w: %w; run worker-usage only as Claude Code's statusLine command",
			ErrStatusLine, err)
	}
	cw := in.ContextWindow
	if cw == nil {
		return Usage{}, nil
	}
	u := Usage{InputTokens: cw.TotalInputTokens, OutputTokens: cw.TotalOutputTokens}
	switch {
	case cw.UsedPercentage != nil:
		u.ContextPct = int(math.Round(*cw.UsedPercentage))
	case cw.ContextWindowSize > 0:
		u.ContextPct = int(math.Round(float64(cw.TotalInputTokens) * pctScale / float64(cw.ContextWindowSize)))
	}
	return u, nil
}
