package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/polandy/fanin/internal/runtime"
)

// taskPath is the UI hash route of a task page.
const taskPath = "/#/task/"

// ellipsis marks a cut text.
const ellipsis = "…"

// SignalConfig configures the Signal adapter.
type SignalConfig struct {
	// CLI is the signal-cli program.
	CLI string
	// Number is the owner's own number: the account and the only recipient.
	Number string
	// Prefix starts every message, such as "[nuc/fanin]".
	Prefix string
	// UIURL is the base URL of the web UI.
	UIURL string
}

// Signal sends notes to the owner's own number through signal-cli.
type Signal struct {
	runner runtime.Runner
	cfg    SignalConfig
	log    *slog.Logger
}

// NewSignal returns a Signal notifier; log may be nil.
func NewSignal(r runtime.Runner, cfg SignalConfig, log *slog.Logger) *Signal {
	if log == nil {
		log = slog.Default()
	}
	return &Signal{runner: r, cfg: cfg, log: log}
}

// Notify sends n as "<prefix> <text> <link>", cut to MaxChars. The number never reaches an error or the log.
func (s *Signal) Notify(ctx context.Context, n Note) error {
	if n.To != "" && n.To != s.cfg.Number {
		return fmt.Errorf("%w: signal notes go only to the configured own number; leave Note.To empty", ErrForeignRecipient)
	}
	msg := s.message(n)
	_, err := s.runner.Run(ctx, runtime.Cmd{Argv: []string{s.cfg.CLI, "-a", s.cfg.Number, "send", "-m", msg, s.cfg.Number}})
	if err != nil {
		return fmt.Errorf("%w: project %q task %q: %s; check signal_number and that signal-cli is registered",
			ErrSend, n.Project, n.TaskID, s.scrub(err))
	}
	s.log.InfoContext(ctx, "notify: signal sent", "project", n.Project, "task_id", n.TaskID, "chars", utf8.RuneCountInString(msg))
	return nil
}

func (s *Signal) message(n Note) string {
	link := strings.TrimRight(s.cfg.UIURL, "/") + taskPath + url.PathEscape(string(n.TaskID))
	head := s.cfg.Prefix + " "
	tail := " " + link
	room := MaxChars - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	text := n.Text
	if utf8.RuneCountInString(text) > room {
		text = string([]rune(text)[:max(room-1, 0)]) + ellipsis
	}
	return head + text + tail
}

// scrub describes err without the argv or stderr of an ExitError, which name the number.
func (s *Signal) scrub(err error) string {
	var ee *runtime.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("%s exited with code %d", s.cfg.CLI, ee.Code)
	}
	return strings.ReplaceAll(err.Error(), s.cfg.Number, "<number>")
}
