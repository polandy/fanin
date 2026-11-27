package sched

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/pkg/faninmcp"
)

func TestResume(t *testing.T) {
	t.Run("SP-10_restart_observes_the_live_run_again_and_starts_no_second_worker", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventWorking})
		h.stop()
		all, err := h.st.ArmedTimers(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx *store.Tx) error { return disarm(ctx, tx, all[:1]) })
		h.restart()
		if n := len(h.rt.starts()); n != 0 {
			t.Errorf("starts after the restart = %d, want none", n)
		}
		want := runtime.RunHandle{TaskID: "a1", RunID: run.ID, Project: projAlpha,
			Agent: AgentName("a1", 1), Workspace: "ws-" + projAlpha, Tab: "tab-a1-1", Pane: "pane-a1-1"}
		if diff := cmp.Diff([]runtime.RunHandle{want}, h.rt.observes()); diff != "" {
			t.Errorf("observed (-want +got):\n%s", diff)
		}
		if got := h.timers("a1"); len(got) != 2 {
			t.Errorf("timers after the restart = %v, want stall and timeout armed again", got)
		}
		h.send(run.Worker.Agent, idle())
		h.advance(workerTimeout)
		if got := h.run(run.ID).State; got != model.RunTimedOut {
			t.Errorf("resumed run after %v = %s, want timed_out", workerTimeout, got)
		}
		if runs := h.runs("a1"); len(runs) != 1 {
			t.Errorf("runs = %d, want one", len(runs))
		}
	})

	t.Run("SP-11_run_without_a_pane_keeps_its_slot_and_starts_nothing", func(t *testing.T) {
		cfg := testConfig()
		cfg.Projects[0].MaxParallel = 1
		h := newHarness(t, cfg)
		h.addTask(ownerTask("a1"))
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			if err := tx.SetTaskState(ctx, "a1", model.TaskRunning, "slot 1"); err != nil {
				return err
			}
			return tx.CreateRun(ctx, model.Run{ID: RunID("a1", 1), TaskID: "a1", Number: 1, Slot: 1,
				Worker: model.Worker{Agent: AgentName("a1", 1)}})
		})
		h.addTask(ownerTask("a2"))
		h.start()
		h.quiet()
		if n, m := len(h.rt.starts()), len(h.rt.observes()); n+m != 0 {
			t.Errorf("starts %d observes %d, want none", n, m)
		}
		if runs := h.runs("a2"); len(runs) != 0 {
			t.Errorf("a2 runs = %v, want none while a1 holds the slot", runs)
		}
		if !strings.Contains(h.logs.String(), "status of a1-1 unknown, no second worker started") {
			t.Errorf("logs lack the unknown status line:\n%s", h.logs)
		}
	})

	t.Run("SP-11_unobservable_worker_is_never_ended", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.stop()
		h.restartWith(func(rt *fakeRuntime) { rt.observeErr = runtime.ErrStatusUnknown })
		h.advance(workerTimeout)
		h.alive(run)
		if !strings.Contains(h.logs.String(), "no second worker started") {
			t.Errorf("logs lack the unknown status line:\n%s", h.logs)
		}
	})

	t.Run("SP-10_ended_run_is_not_resumed", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.tx(func(ctx context.Context, tx *store.Tx) error {
			return tx.SetRunState(ctx, run.ID, model.RunCanceled, "owner")
		})
		h.quiet()
		h.restart()
		if n := len(h.rt.observes()); n != 0 {
			t.Errorf("observes = %d, want none for an ended run", n)
		}
	})
}

// restartWith reopens the store of a stopped scheduler and starts a fresh one with a runtime set prepares.
func (h *harness) restartWith(set func(*fakeRuntime)) {
	h.t.Helper()
	if err := h.st.Close(); err != nil {
		h.t.Fatal(err)
	}
	st, err := store.Open(h.t.Context(), h.path, h.clk)
	if err != nil {
		h.t.Fatal(err)
	}
	h.st, h.rt = st, newFakeRuntime()
	set(h.rt)
	h.start()
}

func TestRunFails(t *testing.T) {
	t.Run("remote_host_is_refused", func(t *testing.T) {
		cfg := testConfig()
		cfg.Projects[0].Host = hostFar
		cfg.Hosts = map[string]config.Host{hostFar: {Name: hostFar, Address: "far.example"}}
		if _, err := New(Deps{Config: cfg}); !errors.Is(err, ErrRemoteHost) {
			t.Errorf("New = %v, want %v", err, ErrRemoteHost)
		}
	})

	t.Run("unreadable_store_is_an_error", func(t *testing.T) {
		clk := clock.NewManual(epoch)
		st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "fanin.db"), clk)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		s, err := New(Deps{Config: testConfig(), Store: st, Runtime: newFakeRuntime(), Picker: &fakePicker{},
			Files: failingFiles{}, Clock: clk, Log: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Run(t.Context()); err == nil {
			t.Error("Run on a closed store = nil, want an error")
		}
	})
}

func readSettings(t *testing.T, path string) claudeSettings {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != filePerm {
		t.Errorf("settings mode = %v, want %v; it holds the run token", info.Mode().Perm(), os.FileMode(filePerm))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s claudeSettings
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFilesPrepare(t *testing.T) {
	dir := t.TempDir()
	f := Files{Dir: dir, DaemonURL: "http://127.0.0.1:7"}
	run := model.Run{ID: "a1-run-1", Token: "secret"}
	paths, err := f.Prepare(t.Context(), config.Project{}, run)
	if err != nil {
		t.Fatal(err)
	}
	want := RunPaths{MCPConfig: filepath.Join(dir, "a1-run-1", MCPConfigName),
		Settings: filepath.Join(dir, "a1-run-1", SettingsName)}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("paths (-want +got):\n%s", diff)
	}
	path := paths.MCPConfig
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != filePerm {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), os.FileMode(filePerm))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got mcpConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	wantMCP := mcpConfig{Servers: map[string]mcpServer{MCPServerName: {Command: DefaultCommand, Args: []string{WorkerMCPArg},
		Env: map[string]string{faninmcp.EnvURL: f.DaemonURL, faninmcp.EnvRunID: "a1-run-1", faninmcp.EnvRunToken: "secret"}}}}
	if diff := cmp.Diff(wantMCP, got); diff != "" {
		t.Errorf("MCP config (-want +got):\n%s", diff)
	}
	wantSettings := claudeSettings{StatusLine: statusLine{Type: StatusLineType,
		Command: "FANIN_URL='http://127.0.0.1:7' FANIN_RUN_ID='a1-run-1' FANIN_RUN_TOKEN='secret' 'fanin' " +
			WorkerUsageArg}}
	if diff := cmp.Diff(wantSettings, readSettings(t, paths.Settings)); diff != "" {
		t.Errorf("AW-14: settings (-want +got):\n%s", diff)
	}
	quoted, err := Files{Dir: t.TempDir(), DaemonURL: "http://h", Command: "/opt/it's/fanin"}.Prepare(t.Context(),
		config.Project{}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readSettings(t, quoted.Settings).StatusLine.Command,
		`FANIN_URL='http://h' FANIN_RUN_ID='a1-run-1' FANIN_RUN_TOKEN='secret' '/opt/it'\''s/fanin' `+
			WorkerUsageArg; got != want {
		t.Errorf("AW-14: command = %s, want %s", got, want)
	}

	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := (Files{Dir: blocked}).Prepare(t.Context(), config.Project{}, run); err == nil {
		t.Error("Prepare under a file = nil, want an error")
	}
}
