// Package github is the forge.Forge for GitHub. It calls gh and gh api through runtime.Runner; gh owns the
// authentication, so fanin never holds a GitHub token.
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime"
)

// Program is the gh binary, resolved on PATH.
const Program = "gh"

// MergeRetryDelays are the waits before each retry of a squash merge that failed on the network (see SCENARIOS
// MG-6).
var MergeRetryDelays = []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}

// gh exits 1 for every failure, so its stderr is the only signal of the cause.
var (
	networkMarkers = []string{
		"error connecting to", "dial tcp", "i/o timeout", "connection reset", "connection refused",
		"TLS handshake timeout", "no such host", "HTTP 502", "HTTP 503", "HTTP 504",
	}
	notFoundMarkers = []string{
		"HTTP 404", "Could not resolve to a", "no pull requests found", "Reference does not exist",
	}
)

// Client talks to GitHub through gh.
type Client struct {
	runner runtime.Runner
	clock  clock.Clock
	log    *slog.Logger
}

var _ forge.Forge = (*Client)(nil)

// New returns a Client; a nil logger logs nowhere.
func New(runner runtime.Runner, clk clock.Clock, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{runner: runner, clock: clk, log: log}
}

// gh runs one gh call and turns a failure into a redacted error wrapping a forge sentinel where one fits.
func (c *Client) gh(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	argv := append([]string{Program}, args...)
	op := opName(argv)
	res, err := c.runner.Run(ctx, runtime.Cmd{Argv: argv, Stdin: stdin})
	if err == nil {
		return res.Stdout, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("forge: %s: %w", op, ctxErr)
	}
	var exit *runtime.ExitError
	if !errors.As(err, &exit) {
		return nil, forge.RedactError(fmt.Errorf("forge: %s: %w", op, err))
	}
	// The ExitError is not wrapped: its argv and stderr would reach callers unredacted.
	tail := forge.Redact(exit.StderrTail)
	switch {
	case containsAny(tail, networkMarkers):
		return nil, fmt.Errorf("%w: %s: exit %d: %s", forge.ErrNetwork, op, exit.Code, tail)
	case containsAny(tail, notFoundMarkers):
		return nil, fmt.Errorf("%w: %s: exit %d: %s", forge.ErrNotFound, op, exit.Code, tail)
	default:
		return nil, fmt.Errorf("forge: %s: exit %d: %s; check that gh is authenticated (gh auth status)", op, exit.Code, tail)
	}
}

// opName is the program and its subcommand words, without flags or free text such as a PR body.
func opName(argv []string) string {
	const words = 3
	n := min(len(argv), words)
	for i := 1; i < n; i++ {
		if strings.HasPrefix(argv[i], "-") {
			n = i
			break
		}
	}
	return strings.Join(argv[:n], " ")
}

func containsAny(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// wait blocks for d on the injected clock, or until ctx ends.
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.clock.After(d):
		return nil
	}
}
