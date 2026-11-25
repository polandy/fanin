package opencode_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/opencode"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	project = "proj"
	repo    = "/path/to/proj"
	base    = "main"
	clone   = opencode.DefaultSrv + "/" + project
	bundle  = "bundle-bytes"
)

var (
	sudo     = []string{"sudo", "-n", "-u", opencode.DefaultUser}
	fetch    = []string{"git", "-C", repo, "fetch", "-q", "origin", base}
	bundleCr = []string{"git", "-C", repo, "bundle", "create", "-q", "-", "refs/remotes/origin/" + base}
	status   = slices.Concat(sudo, []string{"git", "-C", clone, "status", "--porcelain"})
	syncArgv = slices.Concat(sudo, []string{"sh", "-c", opencode.SyncScript, "sh", clone, base})
)

// fakeInner records the specs it starts.
type fakeInner struct {
	runtime.WorkerRuntime
	started []runtime.RunSpec
}

func (f *fakeInner) Start(_ context.Context, spec runtime.RunSpec) (runtime.RunHandle, error) {
	f.started = append(f.started, spec)
	return runtime.RunHandle{Agent: spec.Agent, Pane: "p1"}, nil
}

func ocSpec() runtime.RunSpec {
	return runtime.RunSpec{Project: project, Worktree: repo, Base: base, Agent: "a1-1", Prompt: "do it",
		Kind: runtime.KindOpencode}
}

func newRig(t *testing.T) (*testutil.RecordingRunner, *fakeInner, *opencode.Runtime, string) {
	t.Helper()
	r := testutil.NewRecordingRunner(t)
	in := &fakeInner{}
	shim := filepath.Join(t.TempDir(), "oc-bin")
	cfg := opencode.DefaultConfig()
	cfg.ShimDir = shim
	return r, in, opencode.New(in, r, cfg), shim
}

func TestStart(t *testing.T) {
	t.Run("SP-9_syncs_the_clone_writes_the_shim_and_starts_opencode_with_it_on_PATH", func(t *testing.T) {
		r, in, rt, shim := newRig(t)
		r.Expect(status, testutil.Reply{})
		r.Expect(fetch, testutil.Reply{})
		r.Expect(bundleCr, testutil.Reply{Stdout: bundle})
		r.Expect(syncArgv, testutil.Reply{})

		if _, err := rt.Start(t.Context(), ocSpec()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		r.Done()
		if got := string(r.Calls()[3].Stdin); got != bundle {
			t.Errorf("sync stdin = %q, want the bundle", got)
		}
		want := ocSpec()
		want.PathDir = shim
		if diff := cmp.Diff([]runtime.RunSpec{want}, in.started); diff != "" {
			t.Errorf("inner specs (-want +got):\n%s", diff)
		}
		body, err := os.ReadFile(filepath.Join(shim, opencode.ShimName))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "#!/bin/sh\nexec "+opencode.DefaultLauncher+" \"$@\"\n" {
			t.Errorf("shim = %q", body)
		}
		if st, err := os.Stat(filepath.Join(shim, opencode.ShimName)); err != nil || st.Mode().Perm()&0o100 == 0 {
			t.Errorf("shim is not executable: %v %v", st, err)
		}
	})

	t.Run("SP-9_a_dirty_clone_stops_before_any_herdr_call", func(t *testing.T) {
		r, in, rt, _ := newRig(t)
		r.Expect(status, testutil.Reply{Stdout: " M main.go\n?? notes.txt\n"})

		_, err := rt.Start(t.Context(), ocSpec())
		if !errors.Is(err, opencode.ErrDirtyClone) {
			t.Fatalf("err = %v, want ErrDirtyClone", err)
		}
		r.Done()
		for _, part := range []string{clone + " is not clean", "M main.go", "never delete"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("err = %q, want %q in it", err, part)
			}
		}
		if len(in.started) != 0 {
			t.Errorf("inner started %d runs, want none", len(in.started))
		}
	})

	t.Run("SP-9_a_failed_sync_stops_before_any_herdr_call", func(t *testing.T) {
		r, in, rt, _ := newRig(t)
		r.Expect(status, testutil.Reply{})
		r.Expect(fetch, testutil.Reply{Stderr: "could not read from remote", ExitCode: 128})

		_, err := rt.Start(t.Context(), ocSpec())
		var exit *runtime.ExitError
		if !errors.As(err, &exit) || exit.Code != 128 {
			t.Fatalf("err = %v, want the fetch's exit 128", err)
		}
		r.Done()
		if len(in.started) != 0 {
			t.Errorf("inner started %d runs, want none", len(in.started))
		}
	})

	t.Run("SP-9_an_unreadable_clone_status_is_an_error_never_clean", func(t *testing.T) {
		r, in, rt, _ := newRig(t)
		r.Expect(status, testutil.Reply{Stderr: "sudo: a password is required", ExitCode: 1})

		_, err := rt.Start(t.Context(), ocSpec())
		if err == nil || errors.Is(err, opencode.ErrDirtyClone) {
			t.Fatalf("err = %v, want the status read error", err)
		}
		r.Done()
		if !strings.Contains(err.Error(), "sudoers") || len(in.started) != 0 {
			t.Errorf("err = %v, started %d; want a sudoers hint and no start", err, len(in.started))
		}
	})

	t.Run("SP-9_claude_specs_pass_through_untouched", func(t *testing.T) {
		r, in, rt, _ := newRig(t)
		s := ocSpec()
		s.Kind = ""
		if _, err := rt.Start(t.Context(), s); err != nil {
			t.Fatal(err)
		}
		r.Done()
		if diff := cmp.Diff([]runtime.RunSpec{s}, in.started); diff != "" {
			t.Errorf("inner specs (-want +got):\n%s", diff)
		}
	})

	t.Run("SP-9_unknown_kinds_and_a_missing_base_are_refused", func(t *testing.T) {
		unknown := ocSpec()
		unknown.Kind = "codex"
		noBase := ocSpec()
		noBase.Base = ""
		for _, s := range []runtime.RunSpec{unknown, noBase} {
			r, in, rt, _ := newRig(t)
			if _, err := rt.Start(t.Context(), s); !errors.Is(err, runtime.ErrInvalidSpec) {
				t.Errorf("Start(%+v) err = %v, want ErrInvalidSpec", s, err)
			}
			r.Done()
			if len(in.started) != 0 {
				t.Errorf("inner started %d runs, want none", len(in.started))
			}
		}
	})
}
