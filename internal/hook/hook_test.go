package hook

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const (
	project  = "alpha"
	task     = model.TaskID("alpha-1")
	pr       = 42
	command  = "/srv/alpha/deploy.sh --prod"
	stateDir = "/var/lib/fanin"
	lastLine = "deploy: host unreachable"
)

var (
	epoch      = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	errNoShell = errors.New("exec: sh: not found")
)

// fakeRunner records each call and answers with res and err; during, when set, runs inside the call.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []runtime.Cmd
	res    runtime.Result
	err    error
	during func(ctx context.Context) error
}

func (f *fakeRunner) Run(ctx context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	during := f.during
	f.mu.Unlock()
	if during != nil {
		if err := during(ctx); err != nil {
			return runtime.Result{}, err
		}
	}
	return f.res, f.err
}

func (f *fakeRunner) cmds() []runtime.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtime.Cmd(nil), f.calls...)
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "fanin.db"), clock.NewManual(epoch))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return st
}

func request() Request {
	return Request{Project: project, Task: task, PR: pr, Command: command, Dir: stateDir}
}

func holds(t *testing.T, st *store.Store) []model.Hold {
	t.Helper()
	hs, err := st.Holds(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	return hs
}

// events returns the type of every logged event; hold events carry no task.
func events(t *testing.T, st *store.Store) []model.EventType {
	t.Helper()
	evs, err := st.EventsAfter(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.EventType
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

func exitErr(stderr string) error {
	return &runtime.ExitError{Argv: []string{Shell, ShellFlag, command}, Code: 1, StderrTail: stderr}
}

func TestRun(t *testing.T) {
	t.Run("MG-1_runs_once_in_the_state_dir_with_the_env_under_a_hold_it_lifts", func(t *testing.T) {
		st := newStore(t)
		r := &fakeRunner{}
		var during []model.Hold
		r.during = func(context.Context) error {
			during = holds(t, st)
			return nil
		}
		if err := New(st, r).Run(context.Background(), request()); err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		want := []runtime.Cmd{{Argv: []string{Shell, ShellFlag, command}, Dir: stateDir,
			Env: []string{EnvProject + "=" + project, EnvPR + "=42"}}}
		if diff := cmp.Diff(want, r.cmds()); diff != "" {
			t.Errorf("runner calls (-want +got):\n%s", diff)
		}
		for _, c := range r.cmds() {
			for _, e := range c.Env {
				if strings.Contains(e, "LANE") {
					t.Errorf("env %q carries a lane", e)
				}
			}
		}
		if len(during) != 1 || during[0].Kind != model.HoldMerge || during[0].Reason != RunningReason(pr) {
			t.Errorf("holds during the hook = %+v, want one merge hold %q", during, RunningReason(pr))
		}
		if hs := holds(t, st); len(hs) != 0 {
			t.Errorf("holds after the hook = %+v, want none", hs)
		}
		wantEv := []model.EventType{model.EventHoldSet, model.EventHookStarted, model.EventHoldLifted,
			model.EventHookFinished}
		if diff := cmp.Diff(wantEv, events(t, st)); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})

	t.Run("MG-1_no_command_runs_nothing_and_holds_nothing", func(t *testing.T) {
		st := newStore(t)
		r := &fakeRunner{}
		req := request()
		req.Command = ""
		if err := New(st, r).Run(context.Background(), req); err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if c := r.cmds(); len(c) != 0 {
			t.Errorf("runner calls = %v, want none", c)
		}
		if hs := holds(t, st); len(hs) != 0 {
			t.Errorf("holds = %+v, want none", hs)
		}
	})

	fails := []struct {
		name string
		res  runtime.Result
		err  error
		last string
	}{
		{"MG-4_failed_hook_holds_with_the_last_stderr_line",
			runtime.Result{Stdout: []byte("step 1\n")}, exitErr("step 2\n" + lastLine + "\n\n"), lastLine},
		{"MG-4_failed_hook_without_stderr_names_the_last_stdout_line",
			runtime.Result{Stdout: []byte("step 1\n" + lastLine + "\n")}, exitErr(""), lastLine},
		{"MG-4_hook_that_cannot_start_names_the_cause",
			runtime.Result{}, errNoShell, errNoShell.Error()},
	}
	for _, tc := range fails {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			r := &fakeRunner{res: tc.res, err: tc.err}
			err := New(st, r).Run(context.Background(), request())
			var failed *FailedError
			if !errors.Is(err, ErrFailed) || !errors.As(err, &failed) {
				t.Fatalf("Run = %v, want a *FailedError matching ErrFailed", err)
			}
			hs := holds(t, st)
			if len(hs) != 1 || hs[0].Kind != model.HoldMerge || hs[0].Reason != FailedPrefix+tc.last {
				t.Fatalf("holds = %+v, want one merge hold %q", hs, FailedPrefix+tc.last)
			}
			if failed.LastLine != tc.last || failed.HoldID != hs[0].ID || failed.PR != pr ||
				failed.Project != project {
				t.Errorf("FailedError = %+v, want last line %q and hold %s", failed, tc.last, hs[0].ID)
			}
			for _, part := range []string{project, "#42", tc.last, "fanin hold lift " + hs[0].ID} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q does not name %q", err, part)
				}
			}
			wantEv := []model.EventType{model.EventHoldSet, model.EventHookStarted, model.EventHookFailed}
			if diff := cmp.Diff(wantEv, events(t, st)); diff != "" {
				t.Errorf("events (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("MG-5_holder_stopped_inside_the_hook_keeps_the_hold", func(t *testing.T) {
		st := newStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		r := &fakeRunner{during: func(ctx context.Context) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}}
		err := New(st, r).Run(ctx, request())
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrFailed) {
			t.Fatalf("Run = %v, want context.Canceled, not ErrFailed", err)
		}
		hs := holds(t, st)
		if len(hs) != 1 || hs[0].Reason != RunningReason(pr) {
			t.Errorf("holds = %+v, want the running hold kept", hs)
		}
		wantEv := []model.EventType{model.EventHoldSet, model.EventHookStarted}
		if diff := cmp.Diff(wantEv, events(t, st)); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})

	invalid := []struct {
		name string
		edit func(*Request)
	}{
		{"no_project", func(r *Request) { r.Project = "" }},
		{"no_pr", func(r *Request) { r.PR = 0 }},
		{"no_dir", func(r *Request) { r.Dir = "" }},
	}
	for _, tc := range invalid {
		t.Run("MG-1_refuses_a_request_with_"+tc.name, func(t *testing.T) {
			st := newStore(t)
			r := &fakeRunner{}
			req := request()
			tc.edit(&req)
			if err := New(st, r).Run(context.Background(), req); !errors.Is(err, ErrInvalid) {
				t.Errorf("Run = %v, want ErrInvalid", err)
			}
			if c := r.cmds(); len(c) != 0 {
				t.Errorf("runner calls = %v, want none", c)
			}
		})
	}

	t.Run("MG-5_an_unwritable_hold_runs_no_hook", func(t *testing.T) {
		st := newStore(t)
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		r := &fakeRunner{}
		if err := New(st, r).Run(context.Background(), request()); err == nil || errors.Is(err, ErrFailed) {
			t.Errorf("Run = %v, want a store error", err)
		}
		if c := r.cmds(); len(c) != 0 {
			t.Errorf("runner calls = %v, want none: no hook without its hold", c)
		}
	})

	for _, tc := range []struct {
		name string
		err  error
	}{{"lift", nil}, {"failure", exitErr(lastLine)}} {
		t.Run("MG-4_an_unwritable_"+tc.name+"_after_the_hook_is_an_error", func(t *testing.T) {
			st := newStore(t)
			r := &fakeRunner{err: tc.err, during: func(context.Context) error { return st.Close() }}
			err := New(st, r).Run(context.Background(), request())
			if err == nil || errors.Is(err, ErrFailed) {
				t.Errorf("Run = %v, want a store error, not a verdict", err)
			}
		})
	}
}
