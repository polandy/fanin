//go:build integration

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/render"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/exec"
	"github.com/polandy/fanin/internal/runtime/herdr"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/sched"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/internal/testutil/fakebin"
	"github.com/polandy/fanin/pkg/faninmcp"
)

const (
	herdrWorkspaces = `{"result":{"workspaces":[{"label":"beta","workspace_id":"w1"}]}}`
	herdrTab        = `{"result":{"tab":{"tab_id":"w1:t5"},"root_pane":{"pane_id":"w1:p9"}}}`
	herdrWorking    = `{"result":{"agent":{"agent_status":"working","pane_id":"w1:p9"}}}`
	herdrPane       = "w1:p9"
	analysisPrompt  = "find why the nightly job is slow"
	analysisIssue   = 12
	analysisURL     = "https://forge.example/acme/beta/issues/12"
)

// signalRuntime is a WorkerRuntime that reports each watched and each stopped run.
type signalRuntime struct {
	runtime.WorkerRuntime
	observed chan runtime.RunHandle
	stopped  chan runtime.RunHandle
}

func (r signalRuntime) Observe(ctx context.Context, h runtime.RunHandle) (<-chan runtime.RunEvent, error) {
	ch, err := r.WorkerRuntime.Observe(ctx, h)
	notifyOnce(r.observed, h)
	return ch, err
}

func (r signalRuntime) Stop(ctx context.Context, h runtime.RunHandle) error {
	err := r.WorkerRuntime.Stop(ctx, h)
	notifyOnce(r.stopped, h)
	return err
}

// notifyOnce hands h on without blocking the caller; the test reads only the first.
func notifyOnce(ch chan runtime.RunHandle, h runtime.RunHandle) {
	select {
	case ch <- h:
	default:
	}
}

// await returns the first handle on ch, or fails once fanind ended.
func await(t *testing.T, ch chan runtime.RunHandle, done chan error) runtime.RunHandle {
	t.Helper()
	select {
	case h := <-ch:
		return h
	case err := <-done:
		t.Fatalf("fanind ended early: %v", err)
	}
	return runtime.RunHandle{}
}

// fastHerdr waits for nothing but the next poll, which never comes on its own manual clock.
func fastHerdr() herdr.Config {
	cfg := herdr.DefaultConfig()
	cfg.ShellPause, cfg.WorkCheckInterval, cfg.StartInterval, cfg.ReadRetryInterval = 0, 0, 0, 0
	cfg.PollInterval = 24 * time.Hour
	return cfg
}

func TestAnalysisEndToEnd(t *testing.T) {
	t.Run("FP-3_analysis_issue_from_task_add_to_the_closed_pane", func(t *testing.T) {
		bin := fakebin.New(t)
		bin.Fake(herdr.Program).
			On([]string{"workspace", "list"}, fakebin.Answer{Stdout: herdrWorkspaces}).
			OnPrefix([]string{"tab", "create"}, fakebin.Answer{Stdout: herdrTab}).
			OnPrefix([]string{"agent", "start"}).
			OnPrefix([]string{"agent", "prompt"}).
			OnPrefix([]string{"agent", "get"}, fakebin.Answer{Stdout: herdrWorking}).
			OnPrefix([]string{"pane", "read", herdrPane}).
			On([]string{"pane", "close", herdrPane})
		bin.Install()

		dir := faninConfig(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rt := signalRuntime{observed: make(chan runtime.RunHandle, 1), stopped: make(chan runtime.RunHandle, 1)}
		ready, done := make(chan string, 1), make(chan error, 1)
		d := deps{getenv: func(k string) string { return map[string]string{tokenEnv: token}[k] },
			logOut: &lockedBuffer{}, clock: clock.NewManual(t0), probe: freeLocks{},
			ready: func(addr string) { ready <- addr },
			workers: func(_ config.Config, log *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
				rt.WorkerRuntime = herdr.New(exec.Runner{}, clock.NewManual(t0), log, fastHerdr())
				return rt, idleWorkers{}
			}}
		safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) {
			done <- run(ctx, []string{flagConfig, dir}, d)
		})
		var addr string
		select {
		case addr = <-ready:
		case err := <-done:
			t.Fatalf("fanind ended before it listened: %v", err)
		}
		task, err := client.New("http://"+addr, token, nil).AddTask(ctx,
			wire.TaskRequest{Project: projectBeta, Analyze: analysisPrompt})
		if err != nil {
			t.Fatalf("AddTask: %v", err)
		}
		if h := await(t, rt.observed, done); h.Pane != herdrPane {
			t.Fatalf("watched pane %q, want %q", h.Pane, herdrPane)
		}

		runID := sched.RunID(model.TaskID(task.ID), 1)
		mc, err := faninmcp.NewClient("http://"+addr, string(runID), runToken(t, dir, runID), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := mc.Report(ctx, faninmcp.Report{Kind: faninmcp.KindIssue, Issue: analysisIssue,
			IssueURL: analysisURL}); err != nil {
			t.Fatalf("Report: %v", err)
		}
		if h := await(t, rt.stopped, done); h.Pane != herdrPane {
			t.Errorf("closed pane %q, want %q", h.Pane, herdrPane)
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run after cancel: %v, want nil", err)
		}

		var calls [][]string
		for _, c := range bin.Calls(herdr.Program) {
			calls = append(calls, c.Argv)
		}
		if !slices.ContainsFunc(calls, func(a []string) bool { return slices.Equal(a, []string{"pane", "close", herdrPane}) }) {
			t.Errorf("herdr calls %q, want pane close %s", calls, herdrPane)
		}
		st, err := store.Open(t.Context(), filepath.Join(dir, "fanin.db"), clock.NewManual(t0))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := st.Close(); err != nil {
				t.Error(err)
			}
		}()
		got, err := st.Task(t.Context(), model.TaskID(task.ID))
		if err != nil {
			t.Fatal(err)
		}
		if got.State != model.TaskDone || got.Shape != model.ShapeAnalysis {
			t.Errorf("task = %s %s, want %s %s", got.Shape, got.State, model.ShapeAnalysis, model.TaskDone)
		}
		evs, err := st.Events(t.Context(), got.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := sched.AnalysisIssueDetail + analysisURL
		if !slices.ContainsFunc(evs, func(e model.Event) bool { return e.Type == model.EventTaskState && e.Detail == want }) {
			t.Errorf("no task_state %q in %v", want, evs)
		}
		md, err := render.New(st, clock.NewManual(t0)).Render(t.Context(), projectBeta)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if strings.Contains(string(md), task.ID) {
			t.Errorf("rendered Markdown still lists the done task %s:\n%s", task.ID, md)
		}
	})
}

// runToken reads the run token from the run's MCP config, as the worker's MCP server gets it.
func runToken(t *testing.T, dir string, run model.RunID) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, RunsDir, string(run), sched.MCPConfigName))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	tok := cfg.Servers[sched.MCPServerName].Env[faninmcp.EnvRunToken]
	if tok == "" {
		t.Fatalf("no run token in %s", data)
	}
	return tok
}
