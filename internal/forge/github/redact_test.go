package github_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

func TestTokenNeverLeaks(t *testing.T) {
	t.Run("GT-10_token_in_gh_stderr_is_in_no_log_line_and_no_error", func(t *testing.T) {
		var logs bytes.Buffer
		r := testutil.NewRecordingRunner(t)
		t.Cleanup(r.Done)
		clk := newSignalClock()
		c := github.New(r, clk, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		leak := testutil.Reply{ExitCode: 1, Stderr: netStderr + " (Authorization: token " + token + ")"}
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		for range len(github.MergeRetryDelays) {
			r.Expect(mergeArgv, leak)
			r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		}
		r.Expect(mergeArgv, leak)
		_, err := drive(clk, runMerge(context.Background(), t, c))
		if !errors.Is(err, forge.ErrNetwork) {
			t.Fatalf("err = %v, want ErrNetwork", err)
		}
		lines := strings.Count(logs.String(), "\n")
		if lines != len(github.MergeRetryDelays) || !strings.Contains(logs.String(), forge.Redacted) {
			t.Fatalf("want %d redacted retry log lines, got:\n%s", len(github.MergeRetryDelays), logs.String())
		}
		for _, text := range []string{err.Error(), logs.String()} {
			if strings.Contains(text, token) {
				t.Errorf("token leaked: %s", text)
			}
		}
	})
	t.Run("GT-10_token_in_a_runner_error_is_redacted_and_the_chain_kept", func(t *testing.T) {
		c, r := newClient(t)
		cause := errors.New("exec: GH_TOKEN=" + token + " gh: permission denied")
		r.Expect(viewArgv, testutil.Reply{Err: fmt.Errorf("start: %w", cause)})
		_, err := c.PR(context.Background(), repo, number)
		if err == nil || strings.Contains(err.Error(), token) || !errors.Is(err, cause) {
			t.Errorf("err = %v; want it redacted and wrapping the cause", err)
		}
	})
}
