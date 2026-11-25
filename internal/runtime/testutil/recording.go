// Package testutil holds the hand-written fakes for the runtime seam.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/polandy/fanin/internal/runtime"
)

// ErrUnexpectedCall is returned by a RecordingRunner for a call that is unscripted or has the wrong argv.
var ErrUnexpectedCall = errors.New("testutil: unexpected runner call")

// TB is the part of testing.TB the runner needs.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

// Reply is a scripted answer; a non-zero ExitCode becomes a *runtime.ExitError.
type Reply struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Err, when set, is returned as is instead of a result.
	Err error
}

type expectation struct {
	argv  []string
	reply Reply
}

// RecordingRunner answers calls from a script in order and fails the test on any deviation.
type RecordingRunner struct {
	tb    TB
	mu    sync.Mutex
	queue []expectation
	calls []runtime.Cmd
}

// NewRecordingRunner returns an empty runner reporting failures to tb.
func NewRecordingRunner(tb TB) *RecordingRunner { return &RecordingRunner{tb: tb} }

// Expect queues the exact argv of the next call and its answer.
func (r *RecordingRunner) Expect(argv []string, reply Reply) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = append(r.queue, expectation{argv: argv, reply: reply})
}

// Run records cmd and answers it from the script.
func (r *RecordingRunner) Run(_ context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	r.tb.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, cmd)
	if len(r.queue) == 0 {
		r.tb.Errorf("unscripted call %q", cmd.Argv)
		return runtime.Result{}, fmt.Errorf("%w: %q", ErrUnexpectedCall, cmd.Argv)
	}
	next := r.queue[0]
	r.queue = r.queue[1:]
	if !slices.Equal(next.argv, cmd.Argv) {
		r.tb.Errorf("argv = %q, want %q", cmd.Argv, next.argv)
		return runtime.Result{}, fmt.Errorf("%w: got %q, want %q", ErrUnexpectedCall, cmd.Argv, next.argv)
	}
	if next.reply.Err != nil {
		return runtime.Result{}, next.reply.Err
	}
	res := runtime.Result{Stdout: []byte(next.reply.Stdout), Stderr: []byte(next.reply.Stderr)}
	if next.reply.ExitCode != 0 {
		return res, &runtime.ExitError{Argv: cmd.Argv, Code: next.reply.ExitCode, StderrTail: runtime.Tail(res.Stderr)}
	}
	return res, nil
}

// Calls returns every recorded call in order.
func (r *RecordingRunner) Calls() []runtime.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// Done fails the test for every scripted call that never happened.
func (r *RecordingRunner) Done() {
	r.tb.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.queue {
		r.tb.Errorf("expected call never made: %q", e.argv)
	}
}
