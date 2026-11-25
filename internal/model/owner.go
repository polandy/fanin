package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The bounds on an owner question's options.
const (
	MinOptions = 2
	MaxOptions = 4
)

// ErrInvalidQuestion is returned for an owner question the owner could not answer by picking an option.
var ErrInvalidQuestion = errors.New("model: invalid owner question")

// Option is one answer the owner can pick, with what picking it causes.
type Option struct {
	Label       string
	Description string
}

// OwnerQuestion is a decision a task waits for while it is needs_owner.
type OwnerQuestion struct {
	ID      string
	TaskID  TaskID
	RunID   RunID
	Project string
	// Question stands on its own: the project, what is asked and why.
	Question string
	Options  []Option
	// Recommended is the index into Options of the asker's recommendation.
	Recommended int
	// Answer is the picked option's label or the owner's free text; empty while open.
	Answer     string
	AskedAt    time.Time
	AnsweredAt time.Time
}

// Validate checks the question text, MinOptions..MaxOptions distinct labelled options and the recommendation.
func (q OwnerQuestion) Validate() error {
	if strings.TrimSpace(q.Question) == "" {
		return fmt.Errorf("%w: empty question; state what the owner decides", ErrInvalidQuestion)
	}
	if n := len(q.Options); n < MinOptions || n > MaxOptions {
		return fmt.Errorf("%w: %d options; give %d to %d", ErrInvalidQuestion, n, MinOptions, MaxOptions)
	}
	seen := make(map[string]bool, len(q.Options))
	for i, o := range q.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" {
			return fmt.Errorf("%w: option %d has no label", ErrInvalidQuestion, i)
		}
		if seen[label] {
			return fmt.Errorf("%w: option label %q appears twice; make labels distinct", ErrInvalidQuestion, label)
		}
		seen[label] = true
	}
	if q.Recommended < 0 || q.Recommended >= len(q.Options) {
		return fmt.Errorf("%w: recommended %d; pick an option index from 0 to %d",
			ErrInvalidQuestion, q.Recommended, len(q.Options)-1)
	}
	return nil
}
