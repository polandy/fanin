package model

import (
	"errors"
	"fmt"
)

// MaxPct is the largest share of a context window.
const MaxPct = 100

// ErrInvalidUsage is a usage update with a negative count or a share outside 0 to MaxPct.
var ErrInvalidUsage = errors.New("model: invalid usage")

// Usage is one status-line update of a worker: the tokens in its context after its latest response and the share
// of the context window they fill (SCENARIOS AW-14).
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	ContextPct   int
}

// Validate refuses negative counts and a share outside 0 to MaxPct.
func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 {
		return fmt.Errorf("%w: tokens %d in, %d out; send counts of 0 or more", ErrInvalidUsage, u.InputTokens,
			u.OutputTokens)
	}
	if u.ContextPct < 0 || u.ContextPct > MaxPct {
		return fmt.Errorf("%w: context_pct %d; send 0 to %d", ErrInvalidUsage, u.ContextPct, MaxPct)
	}
	return nil
}

// Add counts u into b: a new response's tokens add to the totals, while a repeat of the last counted one or an
// empty context only moves the share. It reports whether b changed.
func (b Budget) Add(u Usage) (Budget, bool) {
	next := b
	next.ContextPct = u.ContextPct
	empty := u.InputTokens == 0 && u.OutputTokens == 0
	if !empty && (u.InputTokens != b.SeenInput || u.OutputTokens != b.SeenOutput) {
		next.InputTokens += u.InputTokens
		next.OutputTokens += u.OutputTokens
		next.TotalTokens = next.InputTokens + next.OutputTokens
		next.SeenInput, next.SeenOutput = u.InputTokens, u.OutputTokens
	}
	return next, next != b
}
