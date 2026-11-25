package herdr_test

import (
	"context"
	"errors"
	"testing"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const nudge = "your PR has a red check"

func TestPrompt(t *testing.T) {
	t.Run("AW-13_undeliverable_nudge_fails_at_once", func(t *testing.T) {
		f := newFixture(t)
		f.runner.Expect(hd("agent", "prompt", agent, nudge), testutil.Reply{Stderr: "agent not found", ExitCode: 1})

		err := f.rt.Prompt(context.Background(), want, nudge)
		if !errors.Is(err, runtime.ErrPromptRefused) {
			t.Fatalf("err = %v, want ErrPromptRefused", err)
		}
		var ee *runtime.ExitError
		if !errors.As(err, &ee) {
			t.Errorf("err = %v does not carry herdr's exit", err)
		}
		mustContain(t, err, "prompt to "+agent, "agent not found", pane)
		f.runner.Done()
		if n := len(f.runner.Calls()); n != 1 {
			t.Errorf("herdr called %d times, want 1 (no retry)", n)
		}
	})

	t.Run("AW-13_delivered_nudge", func(t *testing.T) {
		f := newFixture(t)
		f.runner.Expect(hd("agent", "prompt", agent, nudge), testutil.Reply{})
		if err := f.rt.Prompt(context.Background(), want, nudge); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		f.runner.Done()
	})
}

func TestStop(t *testing.T) {
	t.Run("FP-7_MG-7_closes_only_the_run_pane", func(t *testing.T) {
		f := newFixture(t)
		f.runner.Expect(closeArgv, testutil.Reply{})
		if err := f.rt.Stop(context.Background(), want); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		f.runner.Done()
		if n := len(f.runner.Calls()); n != 1 {
			t.Errorf("herdr called %d times, want only the pane close", n)
		}
	})

	t.Run("FP-7_MG-7_on_the_run_host", func(t *testing.T) {
		f := newFixture(t)
		h := want
		h.Host = "host-a"
		f.runner.Expect([]string{"herdr", "--machine", "host-a", "pane", "close", pane}, testutil.Reply{})
		if err := f.rt.Stop(context.Background(), h); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		f.runner.Done()
	})

	t.Run("FP-7_failed_close_is_an_error", func(t *testing.T) {
		f := newFixture(t)
		f.runner.Expect(closeArgv, testutil.Reply{Stderr: "no such pane", ExitCode: 1})
		err := f.rt.Stop(context.Background(), want)
		mustContain(t, err, pane, "no such pane")
		f.runner.Done()
	})

	t.Run("MG-7_without_a_pane_closes_nothing", func(t *testing.T) {
		f := newFixture(t)
		h := want
		h.Pane = ""
		err := f.rt.Stop(context.Background(), h)
		if !errors.Is(err, runtime.ErrInvalidSpec) {
			t.Fatalf("err = %v, want ErrInvalidSpec", err)
		}
		if n := len(f.runner.Calls()); n != 0 {
			t.Errorf("herdr called %d times, want 0", n)
		}
	})
}

func TestObserveStubClosesWithTheContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := f.rt.Observe(ctx, want)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	cancel()
	if _, open := <-ch; open {
		t.Error("stub sent an event")
	}
}
