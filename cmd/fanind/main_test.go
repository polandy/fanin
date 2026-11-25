package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

const (
	tokenEnv   = "TEST_FANIN_OWNER_TOKEN"
	token      = "owner-secret"
	flagConfig = "--config"
	project    = "alpha"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// writeConfig writes a config directory that listens on listen, keeps the store in dir and holds two projects.
func writeConfig(t *testing.T, dir, listen string) string {
	t.Helper()
	files := map[string]string{
		"fanin.yaml":        "listen: " + listen + "\nstore: fanin.db\nlog:\n  format: text\nowner_token_env: " + tokenEnv + "\n",
		"forges.yaml":       "forges:\n  github:\n    kind: github\n    token_env: GH_TOKEN\n",
		"worker-kinds.yaml": "kinds:\n  claude:\n    runtime: herdr\n",
	}
	for _, p := range []string{"beta", project} {
		files["projects/"+p+".yaml"] = "path: /path/to/" + p + "\nhost: local\nforge: github\nmax_parallel: 1\n"
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type daemon struct {
	url    string
	done   chan error
	cancel context.CancelFunc
	logs   *bytes.Buffer
}

func start(t *testing.T, cfgPath string, extra ...string) *daemon {
	t.Helper()
	return startEnv(t, cfgPath, map[string]string{tokenEnv: token})
}

// startEnv starts fanind with exactly env as its environment.
func startEnv(t *testing.T, cfgPath string, env map[string]string) *daemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &daemon{done: make(chan error, 1), cancel: cancel, logs: &bytes.Buffer{}}
	ready := make(chan string, 1)
	deps := deps{getenv: func(k string) string { return env[k] }, logOut: d.logs, clock: clock.NewManual(t0),
		ready: func(addr string) { ready <- addr }, probe: freeLocks{}}
	safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) {
		d.done <- run(ctx, append([]string{flagConfig, cfgPath}, extra...), deps)
	})
	select {
	case addr := <-ready:
		d.url = "http://" + addr
	case err := <-d.done:
		t.Fatalf("fanind ended before it listened: %v", err)
	}
	return d
}

func TestDaemonStartsAgainstAnEmptyStore(t *testing.T) {
	dir := t.TempDir()
	d := start(t, writeConfig(t, dir, "127.0.0.1:0"))
	c := client.New(d.url, token, nil)
	ctx := context.Background()
	if h, err := c.Health(ctx); err != nil || h.Status != wire.StatusOK {
		t.Fatalf("Health = %+v, %v; want ok", h, err)
	}
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if strings.Join(snap.Projects, ",") != "alpha,beta" {
		t.Errorf("projects %v, want the config order alpha, beta", snap.Projects)
	}
	d.cancel()
	if err := <-d.done; err != nil {
		t.Errorf("run after cancel: %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fanin.db")); err != nil {
		t.Errorf("store file: %v", err)
	}
	if strings.Contains(d.logs.String(), token) {
		t.Error("the log carries the owner token")
	}
}

// TestDaemonCancelLeavesTasksAlone: a shutdown is never a task failure (guidelines, Context and goroutines).
func TestDaemonCancelLeavesTasksAlone(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "127.0.0.1:0")
	dbPath := filepath.Join(dir, "fanin.db")
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath, clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: "t1", Project: project, Title: "work", Shape: model.ShapePR}); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "t1", model.TaskRunning, ""); err != nil {
			return err
		}
		return tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1, State: model.RunWorking})
	}); err != nil {
		t.Fatal(err)
	}
	before, err := st.LastEventSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	d := start(t, cfgPath)
	// An open event stream must not hold the shutdown up.
	streamReady, streamDone := make(chan struct{}), make(chan error, 1)
	safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) {
		streamDone <- client.New(d.url, token, nil).Events(ctx, before, func() { close(streamReady) },
			func(wire.Event) error { return nil })
	})
	<-streamReady
	d.cancel()
	if err := <-d.done; err != nil {
		t.Fatalf("run after cancel: %v, want nil", err)
	}
	if err := <-streamDone; err != nil {
		t.Errorf("event stream after shutdown: %v, want nil", err)
	}

	st, err = store.Open(ctx, dbPath, clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	task, err := st.Task(ctx, "t1")
	if err != nil || task.State != model.TaskRunning {
		t.Errorf("task t1 = %s, %v; want still running", task.State, err)
	}
	r, err := st.Run(ctx, "r1")
	if err != nil || r.State != model.RunWorking {
		t.Errorf("run r1 = %s, %v; want still working", r.State, err)
	}
	if after, err := st.LastEventSeq(ctx); err != nil || after != before {
		t.Errorf("last event %d, %v; want %d: the shutdown wrote no event", after, err, before)
	}
}

func TestDaemonRefusesToStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(dir string) []string
		env  map[string]string
		want error
		in   string
	}{
		{"missing config", func(dir string) []string { return []string{flagConfig, filepath.Join(dir, "none")} },
			map[string]string{tokenEnv: token}, config.ErrRead, "none"},
		{"no config flag", func(string) []string { return nil }, map[string]string{tokenEnv: token},
			errNoConfig, envConfig},
		{"empty owner token", func(dir string) []string {
			return []string{flagConfig, writeConfig(t, dir, "127.0.0.1:0")}
		}, nil, errNoOwnerToken, tokenEnv},
		{"busy or bad listen address", func(dir string) []string {
			return []string{flagConfig, writeConfig(t, dir, "256.0.0.1:1")}
		}, map[string]string{tokenEnv: token}, errListen, "256.0.0.1:1"},
		{"bad flag", func(string) []string { return []string{"--nope"} }, nil, errUsage, "nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := deps{getenv: func(k string) string { return tc.env[k] }, logOut: &bytes.Buffer{},
				clock: clock.NewManual(t0), ready: func(string) { t.Error("ready called for a refused start") }}
			err := run(context.Background(), tc.args(t.TempDir()), deps)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.in) {
				t.Errorf("run: %v, want %v naming %q", err, tc.want, tc.in)
			}
		})
	}
}
