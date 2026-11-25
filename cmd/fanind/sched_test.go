package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/sched"
	"github.com/polandy/fanin/internal/store"
)

// idleWorkers is a runtime that starts nothing and a picker that finds nothing.
type idleWorkers struct{}

func (idleWorkers) Start(context.Context, runtime.RunSpec) (runtime.RunHandle, error) {
	return runtime.RunHandle{}, runtime.ErrStartFailed
}

func (idleWorkers) Prompt(context.Context, runtime.RunHandle, string) error { return nil }

func (idleWorkers) Stop(context.Context, runtime.RunHandle) error { return nil }

func (idleWorkers) Observe(context.Context, runtime.RunHandle) (<-chan runtime.RunEvent, error) {
	return nil, runtime.ErrStatusUnknown
}

func (idleWorkers) Pick(context.Context, pick.Request) (pick.Result, error) {
	return pick.Result{Idle: true, Reason: "nothing"}, nil
}

// lockedBuffer is a log sink the scheduler goroutine and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// faninConfig writes a config whose project beta has loop: fanin.
func faninConfig(t *testing.T) string {
	t.Helper()
	dir := writeConfig(t, t.TempDir(), "127.0.0.1:0")
	file := filepath.Join(dir, "projects", projectBeta+".yaml")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(body, "loop: "+config.LoopFanin+"\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDaemonSched(t *testing.T) {
	t.Run("LN-2_loop_fanin_runs_the_scheduler_and_skips_the_shadow_import", func(t *testing.T) {
		dir := faninConfig(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		logs, ready, done := &lockedBuffer{}, make(chan string, 1), make(chan error, 1)
		var built []string
		d := deps{getenv: func(k string) string { return map[string]string{tokenEnv: token}[k] }, logOut: logs,
			clock: clock.NewManual(t0), probe: freeLocks{}, ready: func(addr string) { ready <- addr },
			workers: func(cfg config.Config, _ *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
				built = append(built, cfg.Listen)
				return idleWorkers{}, idleWorkers{}
			}}
		safego.Go(ctx, slog.New(slog.DiscardHandler), func(ctx context.Context) {
			done <- run(ctx, []string{flagConfig, dir, flagShadow, fixtures}, d)
		})
		var addr string
		select {
		case addr = <-ready:
		case err := <-done:
			t.Fatalf("fanind ended before it listened: %v", err)
		}
		snap, err := client.New("http://"+addr, token, nil).Snapshot(ctx)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		for _, h := range snap.Holds {
			if h.Project == projectBeta {
				t.Errorf("hold %s %s imported for a loop: fanin project", h.Project, h.Kind)
			}
		}
		for _, task := range snap.Tasks {
			if strings.HasPrefix(task.ID, "shadow-beta-") {
				t.Errorf("task %s imported for a loop: fanin project", task.ID)
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run after cancel: %v, want nil", err)
		}
		if len(built) != 1 || !strings.Contains(logs.String(), "scheduler running") {
			t.Errorf("workers built %d times, logs:\n%s", len(built), logs)
		}
	})

	t.Run("LN-5_no_loop_fanin_project_starts_no_scheduler", func(t *testing.T) {
		d := start(t, writeConfig(t, t.TempDir(), "127.0.0.1:0"))
		d.cancel()
		if err := <-d.done; err != nil {
			t.Errorf("run after cancel: %v, want nil", err)
		}
		if strings.Contains(d.logs.String(), "scheduler running") {
			t.Errorf("scheduler ran without a loop: fanin project:\n%s", d.logs)
		}
	})

	t.Run("SP-10_a_scheduler_that_cannot_resume_stops_fanind", func(t *testing.T) {
		dir := faninConfig(t)
		path := filepath.Join(dir, "fanin.db")
		seedLiveRun(t, path)
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(),
			`CREATE TRIGGER fault BEFORE INSERT ON timers BEGIN SELECT RAISE(ABORT, 'injected fault'); END`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		d := deps{getenv: func(k string) string { return map[string]string{tokenEnv: token}[k] },
			logOut: &lockedBuffer{}, clock: clock.NewManual(t0), probe: freeLocks{}, ready: func(string) {},
			workers: func(config.Config, *slog.Logger) (runtime.WorkerRuntime, sched.Picker) {
				return idleWorkers{}, idleWorkers{}
			}}
		if err := run(t.Context(), []string{flagConfig, dir}, d); !errors.Is(err, errSchedStopped) {
			t.Errorf("run = %v, want %v", err, errSchedStopped)
		}
	})
}

// seedLiveRun stores a working run of a beta task whose timers are missing, so a resume must arm them.
func seedLiveRun(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(t.Context(), path, clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	err = st.Transition(t.Context(), func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: "b1", Project: projectBeta, Title: "live", Shape: model.ShapePR,
			OwnerTask: true}); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "b1", model.TaskRunning, "slot 1"); err != nil {
			return err
		}
		if err := tx.CreateRun(ctx, model.Run{ID: "b1-run-1", TaskID: "b1", Number: 1, Slot: 1,
			Worker: model.Worker{Agent: "b1-1", Workspace: "ws", Tab: "tab", Pane: "pane"}}); err != nil {
			return err
		}
		return tx.SetRunState(ctx, "b1-run-1", model.RunWorking, "started")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}
