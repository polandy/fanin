package testutil_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

type fakeTB struct{ errs []string }

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}

func TestRecordingRunnerScriptedAnswers(t *testing.T) {
	tb := &fakeTB{}
	r := testutil.NewRecordingRunner(tb)
	r.Expect([]string{"git", "status"}, testutil.Reply{Stdout: "clean"})
	r.Expect([]string{"gh", "pr", "list"}, testutil.Reply{Stderr: "denied", ExitCode: 4})

	res, err := r.Run(context.Background(), runtime.Cmd{Argv: []string{"git", "status"}, Dir: "/w"})
	if err != nil || string(res.Stdout) != "clean" {
		t.Fatalf("first call: %q %v", res.Stdout, err)
	}
	_, err = r.Run(context.Background(), runtime.Cmd{Argv: []string{"gh", "pr", "list"}})
	var ee *runtime.ExitError
	if !errors.As(err, &ee) || ee.Code != 4 || ee.StderrTail != "denied" {
		t.Fatalf("second call err = %v", err)
	}
	r.Done()
	if len(tb.errs) != 0 {
		t.Errorf("unexpected failures: %v", tb.errs)
	}
	if got := r.Calls(); len(got) != 2 || got[0].Dir != "/w" {
		t.Errorf("Calls = %+v", got)
	}
}

func TestRecordingRunnerFailsOnArgvMismatch(t *testing.T) {
	tb := &fakeTB{}
	r := testutil.NewRecordingRunner(tb)
	r.Expect([]string{"git", "status"}, testutil.Reply{})

	_, err := r.Run(context.Background(), runtime.Cmd{Argv: []string{"git", "stauts"}})
	if !errors.Is(err, testutil.ErrUnexpectedCall) {
		t.Errorf("err = %v, want ErrUnexpectedCall", err)
	}
	if len(tb.errs) != 1 {
		t.Fatalf("got %d failures, want 1: %v", len(tb.errs), tb.errs)
	}
}

func TestRecordingRunnerFailsOnUnscriptedAndUnconsumed(t *testing.T) {
	tb := &fakeTB{}
	r := testutil.NewRecordingRunner(tb)
	if _, err := r.Run(context.Background(), runtime.Cmd{Argv: []string{"x"}}); !errors.Is(err, testutil.ErrUnexpectedCall) {
		t.Errorf("err = %v", err)
	}
	r.Expect([]string{"y"}, testutil.Reply{})
	r.Done()
	if len(tb.errs) != 2 {
		t.Errorf("failures = %v, want unscripted call and unconsumed expectation", tb.errs)
	}
}
