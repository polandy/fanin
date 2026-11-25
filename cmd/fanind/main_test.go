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
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// writeConfig writes a config that listens on a free loopback port and keeps the store in dir.
func writeConfig(t *testing.T, dir, listen string) string {
	t.Helper()
	body := "listen: " + listen + "\nstore: fanin.db\nlog:\n  format: text\nowner_token_env: " + tokenEnv +
		"\nprojects:\n  - name: beta\n  - name: alpha\n"
	path := filepath.Join(dir, "fanin.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type daemon struct {
	url    string
	done   chan error
	cancel context.CancelFunc
	logs   *bytes.Buffer
}

func start(t *testing.T, cfgPath string) *daemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &daemon{done: make(chan error, 1), cancel: cancel, logs: &bytes.Buffer{}}
	ready := make(chan string, 1)
	env := map[string]string{tokenEnv: token}
	deps := deps{getenv: func(k string) string { return env[k] }, logOut: d.logs, clock: clock.NewManual(t0),
		ready: func(addr string) { ready <- addr }}
	safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) {
		d.done <- run(ctx, []string{flagConfig, cfgPath}, deps)
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
	if strings.Join(snap.Projects, ",") != "beta,alpha" {
		t.Errorf("projects %v, want the config order beta, alpha", snap.Projects)
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
		if err := tx.CreateTask(ctx, model.Task{ID: "t1", Project: "alpha", Title: "work", Shape: model.ShapePR}); err != nil {
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
		{"missing config", func(dir string) []string { return []string{flagConfig, filepath.Join(dir, "none.yaml")} },
			map[string]string{tokenEnv: token}, config.ErrRead, "none.yaml"},
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
