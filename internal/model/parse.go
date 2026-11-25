package model

import (
	"errors"
	"fmt"
	"slices"
)

// ErrUnknownValue is returned when a stored or reported enum value is not one fanin knows.
var ErrUnknownValue = errors.New("model: unknown value")

func parse[T ~string](what, s string, all []T) (T, error) {
	if v := T(s); slices.Contains(all, v) {
		return v, nil
	}
	return "", fmt.Errorf("%w: %s %q; use one of %v", ErrUnknownValue, what, s, all)
}
