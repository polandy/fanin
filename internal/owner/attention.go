package owner

import (
	"slices"

	"github.com/polandy/fanin/internal/model"
)

// AttentionEvents are the event types the owner is woken for by fanin wait --owner (see SCENARIOS WA-2).
var AttentionEvents = []model.EventType{
	model.EventQuestionAsked, model.EventResultReported, model.EventHoldSet, model.EventHookFailed,
	model.EventPauseConsumed, model.EventMerged,
}

// NeedsAttention reports whether ev is one the owner is woken for.
func NeedsAttention(ev model.Event) bool { return slices.Contains(AttentionEvents, ev.Type) }
