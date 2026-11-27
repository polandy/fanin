package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The bounds on an owner question's options; a dialog's options are keys 1-9, so it may have more.
const (
	MinOptions       = 2
	MaxOptions       = 4
	MaxDialogOptions = 9
)

// ErrInvalidQuestion is returned for an owner question the owner could not answer by picking an option.
var ErrInvalidQuestion = errors.New("model: invalid owner question")

// QuestionKind says how a question reached the owner and how the answer goes back to the worker.
type QuestionKind string

// The question kinds.
const (
	// QuestionTranscript is asked in the worker's output; the answer goes in as a prompt (see SCENARIOS OA-2).
	QuestionTranscript QuestionKind = "transcript"
	// QuestionDialog is a dialog in the worker's pane; only an option number is pressed (see SCENARIOS OA-1, AW-9).
	QuestionDialog QuestionKind = "dialog"
)

// AllQuestionKinds lists every QuestionKind.
var AllQuestionKinds = []QuestionKind{QuestionTranscript, QuestionDialog}

// ParseQuestionKind reads a stored question kind; an unknown value is ErrUnknownValue.
func ParseQuestionKind(s string) (QuestionKind, error) {
	return parse("question kind", s, AllQuestionKinds)
}

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
	Kind    QuestionKind
	// AllowText lets the owner answer with free text instead of an option number; never for a dialog.
	AllowText bool
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

// Validate checks kind, text, MinOptions..MaxOptions (MaxDialogOptions for a dialog) distinct options, recommendation.
func (q OwnerQuestion) Validate() error {
	if _, err := ParseQuestionKind(string(q.Kind)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuestion, err)
	}
	if q.Kind == QuestionDialog && q.AllowText {
		return fmt.Errorf("%w: a dialog takes only an option number; clear AllowText", ErrInvalidQuestion)
	}
	if strings.TrimSpace(q.Question) == "" {
		return fmt.Errorf("%w: empty question; state what the owner decides", ErrInvalidQuestion)
	}
	limit := MaxOptions
	if q.Kind == QuestionDialog {
		limit = MaxDialogOptions
	}
	if n := len(q.Options); n < MinOptions || n > limit {
		return fmt.Errorf("%w: %d options; give %d to %d", ErrInvalidQuestion, n, MinOptions, limit)
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
