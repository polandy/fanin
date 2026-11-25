// Package notify is the push channel: a short note to the owner when something needs them (spec: docs/dev/spec/notify.md).
package notify

import (
	"context"
	"errors"
	"log/slog"

	"github.com/polandy/fanin/internal/model"
)

// MaxChars is the longest message an adapter sends, prefix and link included (see SCENARIOS OA-7).
const MaxChars = 600

// Kinds of adapter, as named in the config.
const (
	KindLog    = "log"
	KindSignal = "signal"
)

// Errors of the push channel.
var (
	// ErrForeignRecipient is a note addressed to anyone but the owner's own number; nothing was sent.
	ErrForeignRecipient = errors.New("notify: foreign recipient")
	// ErrSend is a failed send; the message names the exit code, never the number.
	ErrSend = errors.New("notify: send failed")
)

// Note is one message to the owner.
type Note struct {
	Project string
	TaskID  model.TaskID
	// Text stands on its own; the adapter adds prefix and link.
	Text string
	// To is the recipient; empty means the owner's own number. Anything else is ErrForeignRecipient.
	To string
}

// Notifier delivers a note to the owner.
type Notifier interface {
	Notify(ctx context.Context, n Note) error
}

// Log is the default adapter: it only writes the note to the log.
type Log struct{ log *slog.Logger }

// NewLog returns a Log notifier; log may be nil.
func NewLog(log *slog.Logger) *Log {
	if log == nil {
		log = slog.Default()
	}
	return &Log{log: log}
}

// Notify logs n with its project and task.
func (l *Log) Notify(ctx context.Context, n Note) error {
	l.log.InfoContext(ctx, "notify: owner note", "project", n.Project, "task_id", n.TaskID, "text", n.Text)
	return nil
}
