package headless_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/headless"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	model   = "haiku"
	tools   = "Read,Grep"
	repoDir = "/repo"
	prompt  = "pick a task"
	timeout = 600 * time.Second
	okJSON  = `{"type":"result","is_error":false,"result":"PICK sonnet: fix it","usage":` +
		`{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":3,"cache_read_input_tokens":2}}`
)

var wantArgv = []string{"claude", "-p", "--output-format", "json", "--model", model, "--allowedTools", tools}

func spec() headless.Spec {
	return headless.Spec{Model: model, Prompt: prompt, Dir: repoDir, AllowedTools: []string{"Read", "Grep"}, Timeout: timeout}
}

func newRuntime(r runtime.Runner, c clock.Clock) *headless.Runtime {
	return headless.New(r, c, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRunArgvOutputAndUsage(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	rr.Expect(wantArgv, testutil.Reply{Stdout: okJSON})
	got, err := newRuntime(rr, clock.NewManual(time.Unix(0, 0))).Run(context.Background(), spec())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rr.Done()
	want := headless.Result{Output: "PICK sonnet: fix it", Usage: headless.Usage{Input: 10, Output: 5, CacheCreation: 3, CacheRead: 2}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("result (-want +got):\n%s", diff)
	}
	if got.Usage.Total() != 20 {
		t.Errorf("Total = %d, want 20", got.Usage.Total())
	}
	calls := rr.Calls()
	if calls[0].Dir != repoDir || string(calls[0].Stdin) != prompt {
		t.Errorf("dir %q stdin %q, want %q and %q", calls[0].Dir, calls[0].Stdin, repoDir, prompt)
	}
}

func TestRunPassesEnv(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	rr.Expect(wantArgv, testutil.Reply{Stdout: okJSON})
	s := spec()
	s.Env = []string{"GIT_LFS_SKIP_SMUDGE=1"}
	if _, err := newRuntime(rr, clock.NewManual(time.Unix(0, 0))).Run(context.Background(), s); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if diff := cmp.Diff(s.Env, rr.Calls()[0].Env); diff != "" {
		t.Errorf("env (-want +got):\n%s", diff)
	}
}

func TestRunWithoutToolsOmitsAllowedTools(t *testing.T) {
	rr := testutil.NewRecordingRunner(t)
	rr.Expect([]string{"claude", "-p", "--output-format", "json", "--model", model}, testutil.Reply{Stdout: okJSON})
	s := spec()
	s.AllowedTools = nil
	if _, err := newRuntime(rr, clock.NewManual(time.Unix(0, 0))).Run(context.Background(), s); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rr.Done()
}

func TestRunInvalidSpec(t *testing.T) {
	for name, mut := range map[string]func(*headless.Spec){
		"no model":   func(s *headless.Spec) { s.Model = "" },
		"no prompt":  func(s *headless.Spec) { s.Prompt = "" },
		"no timeout": func(s *headless.Spec) { s.Timeout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			s := spec()
			mut(&s)
			_, err := newRuntime(rr, clock.NewManual(time.Unix(0, 0))).Run(context.Background(), s)
			if !errors.Is(err, runtime.ErrInvalidSpec) {
				t.Errorf("err = %v, want ErrInvalidSpec", err)
			}
			if len(rr.Calls()) != 0 {
				t.Errorf("runner was called for an invalid spec")
			}
		})
	}
}

func TestRunFailurePaths(t *testing.T) {
	tests := []struct {
		name  string
		reply testutil.Reply
		want  error
	}{
		{"exit code", testutil.Reply{Stderr: "boom", ExitCode: 1}, nil},
		{"not json", testutil.Reply{Stdout: "hello"}, runtime.ErrOddAnswer},
		{"is_error", testutil.Reply{Stdout: `{"is_error":true,"result":"rate limited"}`}, runtime.ErrOddAnswer},
		{"empty result", testutil.Reply{Stdout: `{"is_error":false,"result":""}`}, runtime.ErrOddAnswer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := testutil.NewRecordingRunner(t)
			rr.Expect(wantArgv, tc.reply)
			_, err := newRuntime(rr, clock.NewManual(time.Unix(0, 0))).Run(context.Background(), spec())
			if err == nil {
				t.Fatal("Run succeeded, want an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			var ee *runtime.ExitError
			if tc.want == nil && !errors.As(err, &ee) {
				t.Errorf("err = %v, want *runtime.ExitError", err)
			}
		})
	}
}

// blockingRunner holds the process until its context ends, like a hung claude.
type blockingRunner struct {
	started chan struct{}
	stdout  string
}

func (b blockingRunner) Run(ctx context.Context, _ runtime.Cmd) (runtime.Result, error) {
	close(b.started)
	<-ctx.Done()
	return runtime.Result{Stdout: []byte(b.stdout)}, ctx.Err()
}

func TestRunTimeoutKillsTheProcess(t *testing.T) {
	clk := clock.NewManual(time.Unix(0, 0))
	br := blockingRunner{started: make(chan struct{}), stdout: "half an answer"}
	errc := make(chan error, 1)
	go func() {
		_, err := newRuntime(br, clk).Run(context.Background(), spec())
		errc <- err
	}()
	<-br.started
	clk.Advance(timeout - time.Second)
	select {
	case err := <-errc:
		t.Fatalf("Run returned before the timeout: %v", err)
	default:
	}
	clk.Advance(time.Second)
	err := <-errc
	if !errors.Is(err, headless.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if !strings.Contains(err.Error(), "half an answer") {
		t.Errorf("err %q lacks the output tail", err)
	}
}

func TestRunParentCancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	br := blockingRunner{started: make(chan struct{})}
	errc := make(chan error, 1)
	go func() {
		_, err := newRuntime(br, clock.NewManual(time.Unix(0, 0))).Run(ctx, spec())
		errc <- err
	}()
	<-br.started
	cancel()
	err := <-errc
	if !errors.Is(err, context.Canceled) || errors.Is(err, headless.ErrTimeout) {
		t.Errorf("err = %v, want context.Canceled and not ErrTimeout", err)
	}
}
