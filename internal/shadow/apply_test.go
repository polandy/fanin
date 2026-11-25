package shadow

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

const lease = 90 * time.Second

func openStore(t *testing.T, clk clock.Clock) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return st
}

func lastSeq(t *testing.T, st *store.Store) int64 {
	t.Helper()
	seq, err := st.LastEventSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// dump lists every task of the projects with its state and its runs' states, one line each.
func dump(t *testing.T, st *store.Store, labels ...string) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	for _, label := range labels {
		tasks, err := st.ProjectTasks(ctx, label)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			runs, err := st.Runs(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s %s %s model=%s owner=%t", label, task.ID, task.State, task.PickedModel, task.OwnerTask)
			for _, r := range runs {
				fmt.Fprintf(&b, " run%d=%s", r.Number, r.State)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// snapshotDir reads every file under dir with its mode.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		body := ""
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			body = string(data)
		}
		out[path] = fmt.Sprintf("%s %s %s", info.Mode(), info.ModTime().Format(time.RFC3339Nano), body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func apply(t *testing.T, st *store.Store, projects []Project) {
	t.Helper()
	if err := Apply(context.Background(), st, slog.New(slog.DiscardHandler), projects, lease); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestApplyFixtures(t *testing.T) {
	before := snapshotDir(t, fixtures)
	st := openStore(t, clock.NewManual(t0))
	probe := fakeProbe{held: map[string]bool{".delta.merge.lock": true}}
	projects, err := Read(os.DirFS(fixtures), probe)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, st, projects)

	t.Run("SH-1_tasks_and_runs_match_the_golden_file", func(t *testing.T) {
		want, err := os.ReadFile("testdata/golden/store.txt")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(string(want), dump(t, st, "alpha", "beta", "delta", "gamma")); diff != "" {
			t.Errorf("store (-want +got):\n%s", diff)
		}
	})
	t.Run("SH-5_a_second_import_writes_nothing", func(t *testing.T) {
		seq := lastSeq(t, st)
		again, err := Read(os.DirFS(fixtures), probe)
		if err != nil {
			t.Fatal(err)
		}
		apply(t, st, again)
		if got := lastSeq(t, st); got != seq {
			t.Errorf("last event %d after an unchanged import, want %d", got, seq)
		}
	})
	t.Run("SH-5_the_state_directory_is_unchanged", func(t *testing.T) {
		if diff := cmp.Diff(before, snapshotDir(t, fixtures)); diff != "" {
			t.Errorf("state directory changed (-before +after):\n%s", diff)
		}
	})
}

func task(agent string, state model.TaskState, line string) Task {
	return Task{ID: model.TaskID("shadow-" + agent), Agent: agent, Lane: "a", Model: "sonnet", Prompt: "p",
		State: state, Line: line}
}

func TestApplyMovesTasks(t *testing.T) {
	ctx := context.Background()
	st := openStore(t, clock.NewManual(t0))
	apply(t, st, []Project{{Label: "alpha", Tasks: []Task{task("alpha-1", model.TaskNeedsOwner, "blocked")}}})
	apply(t, st, []Project{{Label: "alpha", Tasks: []Task{task("alpha-1", model.TaskGating, "pr #7")}}})

	t.Run("SH-1_shortest_path_with_the_line_as_detail", func(t *testing.T) {
		events, err := st.Events(ctx, "shadow-alpha-1")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, ev := range events {
			if ev.Type == model.EventTaskState {
				got = append(got, ev.Detail)
			}
		}
		for _, want := range []string{"blocked", "pr #7"} {
			if !strings.Contains(strings.Join(got, "|"), want) {
				t.Errorf("task_state details %q, want one naming %q", got, want)
			}
		}
		if diff := cmp.Diff("alpha shadow-alpha-1 gating model=sonnet owner=false run1=working\n",
			dump(t, st, "alpha")); diff != "" {
			t.Errorf("store (-want +got):\n%s", diff)
		}
	})
	t.Run("SH-1_done_ends_the_run", func(t *testing.T) {
		apply(t, st, []Project{{Label: "alpha", Tasks: []Task{task("alpha-1", model.TaskDone, "merged #7")}}})
		if diff := cmp.Diff("alpha shadow-alpha-1 done model=sonnet owner=false run1=succeeded\n",
			dump(t, st, "alpha")); diff != "" {
			t.Errorf("store (-want +got):\n%s", diff)
		}
	})
	t.Run("SH-1_a_store_ahead_of_the_file_is_skipped", func(t *testing.T) {
		seq := lastSeq(t, st)
		apply(t, st, []Project{{Label: "alpha", Tasks: []Task{task("alpha-1", model.TaskRunning, "spawned")}}})
		if got := lastSeq(t, st); got != seq {
			t.Errorf("last event %d, want %d: a done task stays done", got, seq)
		}
	})
	t.Run("SH-1_canceled_ends_the_run", func(t *testing.T) {
		apply(t, st, []Project{{Label: "alpha", Tasks: []Task{task("alpha-2", model.TaskRunning, "spawned")}}})
		apply(t, st, []Project{{Label: "alpha", Tasks: []Task{task("alpha-2", model.TaskCanceled, "spawned alpha-3")}}})
		r, err := st.Run(ctx, "shadow-alpha-2-run-1")
		if err != nil || r.State != model.RunCanceled {
			t.Errorf("run = %s, %v; want canceled", r.State, err)
		}
	})
}

func TestApplyHolds(t *testing.T) {
	ctx := context.Background()
	st := openStore(t, clock.NewManual(t0))
	merge := model.Hold{Project: "alpha", Kind: model.HoldMerge, Reason: "post-merge failed"}
	ci := model.Hold{Project: "alpha", Kind: model.HoldCI, Reason: "red CI", FixTask: PendingFix}
	active := func(t *testing.T) []string {
		t.Helper()
		holds, err := st.Holds(ctx, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range holds {
			out = append(out, fmt.Sprintf("%s %s %s", h.Kind, h.Reason, h.FixTask))
		}
		return out
	}

	apply(t, st, []Project{{Label: "alpha", Holds: []model.Hold{merge, ci}}})
	t.Run("SH-2_holds_are_set", func(t *testing.T) {
		want := []string{"merge post-merge failed ", "ci red CI shadow-pending-fix"}
		if diff := cmp.Diff(want, active(t)); diff != "" {
			t.Errorf("holds (-want +got):\n%s", diff)
		}
	})
	t.Run("SH-2_an_unchanged_hold_stays", func(t *testing.T) {
		seq := lastSeq(t, st)
		apply(t, st, []Project{{Label: "alpha", Holds: []model.Hold{merge, ci}}})
		if got := lastSeq(t, st); got != seq {
			t.Errorf("last event %d, want %d", got, seq)
		}
	})
	t.Run("SH-2_a_changed_hold_is_set_again", func(t *testing.T) {
		ci.FixTask = "shadow-alpha-4"
		apply(t, st, []Project{{Label: "alpha", Holds: []model.Hold{merge, ci}}})
		want := []string{"merge post-merge failed ", "ci red CI shadow-alpha-4"}
		if diff := cmp.Diff(want, active(t)); diff != "" {
			t.Errorf("holds (-want +got):\n%s", diff)
		}
	})
	t.Run("SH-2_a_removed_hold_file_lifts_the_hold", func(t *testing.T) {
		apply(t, st, []Project{{Label: "alpha"}})
		if got := active(t); len(got) != 0 {
			t.Errorf("holds %q, want none", got)
		}
	})
}

func TestApplyLock(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(t0)
	st := openStore(t, clk)
	locks := func(t *testing.T) []model.Lock {
		t.Helper()
		got, err := st.Locks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	held := &Lock{Lane: "a", PR: 7, Since: "2026-11-25 11:59", Holder: "lane a #7"}

	apply(t, st, []Project{{Label: "alpha", Lock: held}})
	t.Run("SH-3_a_held_flock_takes_the_lock", func(t *testing.T) {
		want := []model.Lock{{Project: "alpha", Holder: "lane a #7", AcquiredAt: t0, ExpiresAt: t0.Add(lease)}}
		if diff := cmp.Diff(want, locks(t)); diff != "" {
			t.Errorf("locks (-want +got):\n%s", diff)
		}
	})
	t.Run("SH-3_each_import_renews_the_lease", func(t *testing.T) {
		clk.Advance(time.Minute)
		apply(t, st, []Project{{Label: "alpha", Lock: held}})
		want := []model.Lock{{Project: "alpha", Holder: "lane a #7", AcquiredAt: t0,
			ExpiresAt: t0.Add(time.Minute + lease)}}
		if diff := cmp.Diff(want, locks(t)); diff != "" {
			t.Errorf("locks (-want +got):\n%s", diff)
		}
	})
	t.Run("SH-3_a_new_holder_replaces_the_row", func(t *testing.T) {
		next := &Lock{Lane: "b", PR: 8, Since: "2026-11-25 12:01", Holder: "lane b #8"}
		apply(t, st, []Project{{Label: "alpha", Lock: next}})
		if got := locks(t); len(got) != 1 || got[0].Holder != "lane b #8" {
			t.Errorf("locks %+v, want one held by lane b #8", got)
		}
	})
	t.Run("SH-3_a_free_flock_releases_the_lock", func(t *testing.T) {
		apply(t, st, []Project{{Label: "alpha"}})
		if got := locks(t); len(got) != 0 {
			t.Errorf("locks %+v, want none", got)
		}
	})
}

// logBuffer is a slog text log the test can read.
func logBuffer() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}
