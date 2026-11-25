package model_test

import (
	"errors"
	"testing"

	"github.com/polandy/fanin/internal/model"
)

func roundTrip[T ~string](t *testing.T, all []T, parse func(string) (T, error)) {
	t.Helper()
	for _, v := range all {
		got, err := parse(string(v))
		if err != nil || got != v {
			t.Errorf("parse(%q) = %q, %v", v, got, err)
		}
	}
	for _, bad := range []string{"", "bogus", " " + string(all[0])} {
		if _, err := parse(bad); !errors.Is(err, model.ErrUnknownValue) {
			t.Errorf("parse(%q): err = %v, want ErrUnknownValue", bad, err)
		}
	}
}

func TestParse(t *testing.T) {
	t.Run("task_state", func(t *testing.T) { roundTrip(t, model.AllTaskStates, model.ParseTaskState) })
	t.Run("run_state", func(t *testing.T) { roundTrip(t, model.AllRunStates, model.ParseRunState) })
	t.Run("shape", func(t *testing.T) { roundTrip(t, model.AllShapes, model.ParseShape) })
	t.Run("result_kind", func(t *testing.T) { roundTrip(t, model.AllResultKinds, model.ParseResultKind) })
	t.Run("verdict", func(t *testing.T) { roundTrip(t, model.AllVerdicts, model.ParseVerdict) })
	t.Run("hold_kind", func(t *testing.T) { roundTrip(t, model.AllHoldKinds, model.ParseHoldKind) })
	t.Run("event_type", func(t *testing.T) { roundTrip(t, model.AllEventTypes, model.ParseEventType) })
}
