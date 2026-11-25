package notify_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	ownNumber = "+15550100"
	foreign   = "+15550199"
	uiURL     = "https://fanin.example/ui"
	prefix    = "[nuc/fanin]"
	project   = "alpha"
	taskID    = "t-42"
)

func cfg() notify.SignalConfig {
	return notify.SignalConfig{CLI: "signal-cli", Number: ownNumber, Prefix: prefix, UIURL: uiURL}
}

// fakeRunner records calls; with fail set it exits 3 and names the number in argv and stderr.
type fakeRunner struct {
	calls []runtime.Cmd
	fail  bool
}

func (f *fakeRunner) Run(_ context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	f.calls = append(f.calls, cmd)
	if f.fail {
		return runtime.Result{}, &runtime.ExitError{Argv: cmd.Argv, Code: 3, StderrTail: "no account " + ownNumber}
	}
	return runtime.Result{}, nil
}

func signal(t *testing.T) (*notify.Signal, *testutil.RecordingRunner, *bytes.Buffer) {
	t.Helper()
	r := testutil.NewRecordingRunner(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := notify.NewSignal(r, cfg(), log)
	return s, r, &buf
}

func TestNotify(t *testing.T) {
	ctx := context.Background()
	note := notify.Note{Project: project, TaskID: taskID, Text: "alpha: ship the cache now?"}

	t.Run("OA-7_argv_is_exact_and_goes_to_the_own_number", func(t *testing.T) {
		s, r, _ := signal(t)
		want := prefix + " alpha: ship the cache now? " + uiURL + "/#/task/t-42"
		r.Expect([]string{"signal-cli", "-a", ownNumber, "send", "-m", want, ownNumber}, testutil.Reply{})
		if err := s.Notify(ctx, note); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		r.Done()
	})

	t.Run("OA-7_foreign_recipient_is_refused", func(t *testing.T) {
		s, r, buf := signal(t)
		n := note
		n.To = foreign
		err := s.Notify(ctx, n)
		if !errors.Is(err, notify.ErrForeignRecipient) {
			t.Fatalf("err = %v, want ErrForeignRecipient", err)
		}
		if len(r.Calls()) != 0 {
			t.Errorf("signal-cli ran for a foreign recipient: %v", r.Calls())
		}
		assertNoNumber(t, err.Error()+buf.String())
	})

	t.Run("OA-7_own_number_as_recipient_is_fine", func(t *testing.T) {
		s, r, _ := signal(t)
		r.Expect([]string{"signal-cli", "-a", ownNumber, "send", "-m", prefix + " hi " + uiURL + "/#/task/t-42", ownNumber},
			testutil.Reply{})
		n := notify.Note{Project: project, TaskID: taskID, Text: "hi", To: ownNumber}
		if err := s.Notify(ctx, n); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		r.Done()
	})

	t.Run("OA-7_text_is_capped_at_600_chars_and_keeps_the_link", func(t *testing.T) {
		f := &fakeRunner{}
		s := notify.NewSignal(f, cfg(), slog.New(slog.DiscardHandler))
		n := note
		n.Text = strings.Repeat("ü", 2000)
		if err := s.Notify(ctx, n); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		if len(f.calls) != 1 {
			t.Fatalf("calls = %d, want 1", len(f.calls))
		}
		msg := f.calls[0].Argv[5]
		if got := utf8.RuneCountInString(msg); got != notify.MaxChars {
			t.Errorf("message has %d chars, want %d", got, notify.MaxChars)
		}
		if !strings.HasPrefix(msg, prefix+" ") || !strings.HasSuffix(msg, " "+uiURL+"/#/task/t-42") {
			t.Errorf("prefix or link lost: %q", msg)
		}
	})

	t.Run("OA-7_event_is_logged_with_the_project_and_no_number", func(t *testing.T) {
		s, r, buf := signal(t)
		r.Expect([]string{"signal-cli", "-a", ownNumber, "send", "-m", prefix + " alpha: ship the cache now? " + uiURL + "/#/task/t-42", ownNumber},
			testutil.Reply{})
		if err := s.Notify(ctx, note); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		out := buf.String()
		for _, want := range []string{"project=alpha", "task_id=t-42"} {
			if !strings.Contains(out, want) {
				t.Errorf("log %q misses %q", out, want)
			}
		}
		assertNoNumber(t, out)
	})

	t.Run("OA-7_failed_send_is_an_error_without_the_number", func(t *testing.T) {
		var buf bytes.Buffer
		f := &fakeRunner{fail: true}
		s := notify.NewSignal(f, cfg(), slog.New(slog.NewTextHandler(&buf, nil)))
		err := s.Notify(ctx, note)
		if !errors.Is(err, notify.ErrSend) {
			t.Fatalf("err = %v, want ErrSend", err)
		}
		assertNoNumber(t, err.Error()+buf.String())
	})

	t.Run("OA-7_log_notifier_logs_project_and_text", func(t *testing.T) {
		var buf bytes.Buffer
		l := notify.NewLog(slog.New(slog.NewTextHandler(&buf, nil)))
		if err := l.Notify(ctx, note); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		if diff := cmp.Diff(true, strings.Contains(buf.String(), "project=alpha") && strings.Contains(buf.String(), "ship the cache")); diff != "" {
			t.Errorf("log = %q", buf.String())
		}
	})
}

func assertNoNumber(t *testing.T, s string) {
	t.Helper()
	for _, n := range []string{ownNumber, foreign} {
		if strings.Contains(s, n) {
			t.Errorf("number %q leaked into %q", n, s)
		}
	}
}
