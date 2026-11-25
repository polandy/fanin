package render

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const project = "proj-a"

var t0 = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

type env struct {
	t     *testing.T
	store *store.Store
	clock *clock.Manual
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewManual(t0)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &env{t: t, store: st, clock: clk}
}

func (e *env) tx(fn func(ctx context.Context, tx *store.Tx) error) {
	e.t.Helper()
	if err := e.store.Transition(context.Background(), fn); err != nil {
		e.t.Fatalf("Transition: %v", err)
	}
}

// task creates a task and walks it through states.
func (e *env) task(id string, states ...model.TaskState) {
	e.t.Helper()
	e.tx(func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: model.TaskID(id), Project: project, Title: "title " + id,
			Shape: model.ShapePR}); err != nil {
			return err
		}
		for _, s := range states {
			if err := tx.SetTaskState(ctx, model.TaskID(id), s, ""); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *env) ask(task, question string, answer string) {
	e.t.Helper()
	e.tx(func(ctx context.Context, tx *store.Tx) error {
		id, err := tx.AskQuestion(ctx, model.OwnerQuestion{TaskID: model.TaskID(task), Question: question,
			Options: []model.Option{{Label: "yes"}, {Label: "no"}}})
		if err != nil || answer == "" {
			return err
		}
		return tx.AnswerQuestion(ctx, id, answer)
	})
}

func (e *env) render() string {
	e.t.Helper()
	md, err := New(e.store, e.clock).Render(context.Background(), project)
	if err != nil {
		e.t.Fatalf("Render: %v", err)
	}
	return string(md)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v; run go test ./internal/render -update", err)
	}
	if string(want) != got {
		t.Errorf("render differs from %s:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func TestRender(t *testing.T) {
	t.Run("RS-1_window_keeps_header_readies_newest_blocked_queued_and_recent_decisions", func(t *testing.T) {
		e := newEnv(t)
		const (
			running = model.TaskRunning
			gating  = model.TaskGating
			done    = model.TaskDone
			needs   = model.TaskNeedsOwner
			paused  = model.TaskPaused
		)
		// Ten days ago: finished work, an old paused task and an old decision.
		e.task("old-done", running, done)
		e.task("old-paused", running, paused)
		e.task("old-blocked", running, needs)
		e.ask("old-blocked", "old question", "yes")
		e.clock.Advance(10 * 24 * time.Hour)
		// Three days ago: the newest blocked task and a recent decision.
		e.task("new-blocked", running, needs)
		e.ask("new-blocked", "recent question", "")
		e.clock.Advance(3 * 24 * time.Hour)
		e.task("ready-1", running, gating)
		e.task("queued-1")
		e.task("queued-2")
		e.task("live", running)
		e.tx(func(ctx context.Context, tx *store.Tx) error {
			return tx.CreateRun(ctx, model.Run{ID: "run-1", TaskID: "live", Number: 1, Slot: 2, Branch: "feat/live",
				State: model.RunWorking, Budget: model.Budget{FixRounds: 1, TotalTokens: 1200}})
		})
		e.clock.Advance(time.Hour)

		got := e.render()
		golden(t, "rs1.golden.md", got)

		// Older entries are missing from the Markdown and stay in the store.
		for _, id := range []model.TaskID{"old-done", "old-paused", "old-blocked"} {
			if bytes.Contains([]byte(got), []byte("`"+string(id)+"`")) {
				t.Errorf("render shows %s; want it outside the window", id)
			}
			if _, err := e.store.Task(context.Background(), id); err != nil {
				t.Errorf("store.Task(%s): %v; the store keeps everything", id, err)
			}
		}
		qs, err := e.store.ProjectQuestions(context.Background(), project)
		if err != nil || len(qs) != 2 {
			t.Errorf("ProjectQuestions = %d, %v; want both questions in the store", len(qs), err)
		}
	})

	t.Run("RS-2_failed_write_leaves_old_file_byte_for_byte", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		e := newEnv(t)
		e.task("a")
		dir := t.TempDir()
		path := filepath.Join(dir, "proj-a.md")
		old := []byte("old content\x00\n")
		if err := os.WriteFile(path, old, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Errorf("restore dir mode: %v", err)
			}
		})
		if err := New(e.store, e.clock).Write(context.Background(), project, path); err == nil {
			t.Fatal("Write into a read-only directory returned nil")
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, old) {
			t.Errorf("file = %q, %v; want the old bytes %q", got, err, old)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 {
			t.Errorf("dir = %v, %v; want no temp file left", entries, err)
		}
	})

	t.Run("RS-2_write_replaces_file", func(t *testing.T) {
		e := newEnv(t)
		e.task("a")
		path := filepath.Join(t.TempDir(), "proj-a.md")
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := New(e.store, e.clock).Write(context.Background(), project, path); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != e.render() {
			t.Errorf("file = %q, %v; want the rendered Markdown", got, err)
		}
	})
}
