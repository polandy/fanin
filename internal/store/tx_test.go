package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

func TestTaskRoundTripAndStateEvents(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	in := task("t1")
	in.Prompt, in.OwnerTask, in.After = "do it", true, []model.TaskID{"t0"}
	in.OwnerModel, in.ProjectModel, in.PickedModel = "o", "p", "k"
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.CreateTask(ctx, in) })
	clk.Advance(time.Minute)
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		return tx.SetTaskState(ctx, "t1", model.TaskRunning, "")
	})

	got, err := s.Task(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	want := in
	want.State, want.CreatedAt, want.UpdatedAt = model.TaskRunning, t0, t0.Add(time.Minute)
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("task (-want +got):\n%s", d)
	}
	evs, err := s.Events(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	wantEvs := []model.Event{
		{Seq: 1, Type: model.EventTaskCreated, Project: project, TaskID: "t1", At: t0, Detail: "title t1"},
		{Seq: 2, Type: model.EventTaskState, Project: project, TaskID: "t1", At: t0.Add(time.Minute),
			Detail: "queued -> running"},
	}
	if d := cmp.Diff(wantEvs, evs); d != "" {
		t.Errorf("events (-want +got):\n%s", d)
	}
}

func TestTransitionRollsBackEventAndTaskTogether(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	createTasks(t, s, "t1")
	boom := errors.New("boom")
	err := s.Transition(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.SetTaskState(ctx, "t1", model.TaskRunning, ""); err != nil {
			return err
		}
		if err := tx.CreateTask(ctx, task("t2")); err != nil {
			return err
		}
		if inTx, err := tx.Task(ctx, "t1"); err != nil || inTx.State != model.TaskRunning {
			t.Errorf("inside the transaction: %v %v, want running", inTx.State, err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Transition: %v, want boom", err)
	}
	got, err := s.Task(ctx, "t1")
	if err != nil || got.State != model.TaskQueued {
		t.Errorf("t1 after rollback: %v %v, want queued", got.State, err)
	}
	if _, err := s.Task(ctx, "t2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("t2 after rollback: %v, want ErrNotFound", err)
	}
	if d := cmp.Diff([]model.EventType{model.EventTaskCreated}, eventTypes(t, s, "t1")); d != "" {
		t.Errorf("events after rollback (-want +got):\n%s", d)
	}
}

func TestTransitionRejectsWhatTheModelForbids(t *testing.T) {
	s, _ := newStore(t)
	createTasks(t, s, "t1")
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx *Tx) error
		want error
	}{
		{"task transition", func(ctx context.Context, tx *Tx) error {
			return tx.SetTaskState(ctx, "t1", model.TaskDone, "")
		}, model.ErrTransition},
		{caseNoTask, func(ctx context.Context, tx *Tx) error {
			return tx.SetTaskState(ctx, "nope", model.TaskRunning, "")
		}, ErrNotFound},
		{"duplicate task", func(ctx context.Context, tx *Tx) error { return tx.CreateTask(ctx, task("t1")) }, nil},
		{"task without project", func(ctx context.Context, tx *Tx) error {
			return tx.CreateTask(ctx, model.Task{ID: "x", Shape: model.ShapePR})
		}, ErrInvalid},
		{"unknown shape", func(ctx context.Context, tx *Tx) error {
			return tx.CreateTask(ctx, model.Task{ID: "x", Project: project, Shape: "blob"})
		}, model.ErrUnknownValue},
		{"unknown state", func(ctx context.Context, tx *Tx) error {
			return tx.CreateTask(ctx, model.Task{ID: "x", Project: project, Shape: model.ShapePR, State: unknownValue})
		}, model.ErrUnknownValue},
		{"unknown event", func(ctx context.Context, tx *Tx) error {
			_, err := tx.AppendEvent(ctx, model.Event{Type: unknownValue, Project: project})
			return err
		}, model.ErrUnknownValue},
		{"event without project", func(ctx context.Context, tx *Tx) error {
			_, err := tx.AppendEvent(ctx, model.Event{Type: model.EventProgress})
			return err
		}, ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Transition(context.Background(), tt.fn)
			if err == nil {
				t.Fatal("Transition succeeded, want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("Transition: %v, want %v", err, tt.want)
			}
		})
	}
	if d := cmp.Diff([]model.EventType{model.EventTaskCreated}, eventTypes(t, s, "t1")); d != "" {
		t.Errorf("events after refused transitions (-want +got):\n%s", d)
	}
}

func TestRunLifecycle(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	createTasks(t, s, "t1")
	run := model.Run{ID: "r1", TaskID: "t1", Number: 1, Slot: 2, Branch: "feat/x", Token: "secret"}
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.CreateRun(ctx, run) })
	budget := model.Budget{FixRounds: 1, ReviewNudges: 2, Stalls: 3, InputTokens: 4, OutputTokens: 5, TotalTokens: 9}
	result := model.Result{Kind: model.ResultDonePR, PR: 7, Text: "  done\n now "}
	clk.Advance(time.Hour)
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		for _, to := range []model.RunState{model.RunWorking, model.RunReported} {
			if err := tx.SetRunState(ctx, "r1", to, ""); err != nil {
				return err
			}
		}
		if err := tx.SetRunBudget(ctx, "r1", budget); err != nil {
			return err
		}
		if err := tx.SetRunResult(ctx, "r1", result); err != nil {
			return err
		}
		return tx.SetRunState(ctx, "r1", model.RunSucceeded, "merged")
	})
	runs, err := s.Runs(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	norm := result.Normalize()
	want := []model.Run{{
		ID: "r1", TaskID: "t1", Number: 1, Slot: 2, Branch: "feat/x", Token: "secret", Budget: budget,
		State: model.RunSucceeded, Result: &norm, StartedAt: t0, EndedAt: t0.Add(time.Hour),
	}}
	if d := cmp.Diff(want, runs); d != "" {
		t.Errorf("runs (-want +got):\n%s", d)
	}
	wantTypes := []model.EventType{
		model.EventTaskCreated, model.EventRunStarted, model.EventRunState, model.EventRunState,
		model.EventTokenUsage, model.EventResultReported, model.EventRunState,
	}
	if d := cmp.Diff(wantTypes, eventTypes(t, s, "t1")); d != "" {
		t.Errorf("events (-want +got):\n%s", d)
	}
}

func TestRunLadderColumns(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	createTasks(t, s, "t1")
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		return tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1, Hint: "fix the parser"})
	})
	budget := model.Budget{Stalls: 1, ContextPct: 80, FooterWaits: 3, ContinueNudges: 1, SecureNudges: 1}
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		if err := tx.SetRunCounts(ctx, "r1", budget); err != nil {
			return err
		}
		if err := tx.SetRunTail(ctx, "r1", "line 1\nline 2"); err != nil {
			return err
		}
		return tx.SetRunBranch(ctx, "r1", "feat/parser")
	})
	run, err := s.Run(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	got := []any{run.Budget, run.Hint, run.Tail, run.Branch}
	want := []any{budget, "fix the parser", "line 1\nline 2", "feat/parser"}
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("run (-want +got):\n%s", d)
	}
	wantTypes := []model.EventType{model.EventTaskCreated, model.EventRunStarted}
	if d := cmp.Diff(wantTypes, eventTypes(t, s, "t1")); d != "" {
		t.Errorf("events (-want +got):\n%s; the scheduler's counts append none", d)
	}
}

func TestRunFailures(t *testing.T) {
	s, _ := newStore(t)
	createTasks(t, s, "t1")
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		return tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1})
	})
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx *Tx) error
		want error
	}{
		{"no number", func(ctx context.Context, tx *Tx) error {
			return tx.CreateRun(ctx, model.Run{ID: "r2", TaskID: "t1"})
		}, ErrInvalid},
		{"unknown state", func(ctx context.Context, tx *Tx) error {
			return tx.CreateRun(ctx, model.Run{ID: "r2", TaskID: "t1", Number: 2, State: unknownValue})
		}, model.ErrUnknownValue},
		{caseNoTask, func(ctx context.Context, tx *Tx) error {
			return tx.CreateRun(ctx, model.Run{ID: "r2", TaskID: "nope", Number: 1})
		}, ErrNotFound},
		{"duplicate number", func(ctx context.Context, tx *Tx) error {
			return tx.CreateRun(ctx, model.Run{ID: "r2", TaskID: "t1", Number: 1})
		}, nil},
		{"run transition", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunState(ctx, "r1", model.RunSucceeded, "")
		}, model.ErrTransition},
		{"missing run", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunState(ctx, "nope", model.RunWorking, "")
		}, ErrNotFound},
		{"invalid result", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunResult(ctx, "r1", model.Result{Kind: model.ResultDonePR})
		}, model.ErrInvalidResult},
		{"result of missing run", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunResult(ctx, "nope", model.Result{Kind: model.ResultDonePR, PR: 1})
		}, ErrNotFound},
		{"budget of missing run", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunBudget(ctx, "nope", model.Budget{})
		}, ErrNotFound},
		{"counts of missing run", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunCounts(ctx, "nope", model.Budget{})
		}, ErrNotFound},
		{"tail of missing run", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunTail(ctx, "nope", "tail")
		}, ErrNotFound},
		{"branch of missing run", func(ctx context.Context, tx *Tx) error {
			return tx.SetRunBranch(ctx, "nope", "feat/x")
		}, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Transition(context.Background(), tt.fn)
			if err == nil {
				t.Fatal("Transition succeeded, want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("Transition: %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCorruptRowsAreErrors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, sql string
		read      func(s *Store) error
	}{
		{"task state", `UPDATE tasks SET state = 'nap'`, func(s *Store) error { return second(s.OpenTasks(ctx)) }},
		{"task shape", `UPDATE tasks SET shape = 'blob'`, func(s *Store) error { return second(s.Task(ctx, "t1")) }},
		{"task after", `UPDATE tasks SET after_ids = '{'`, func(s *Store) error { return second(s.Task(ctx, "t1")) }},
		{"run state", `UPDATE runs SET state = 'nap'`, func(s *Store) error { return second(s.Run(ctx, "r1")) }},
		{"run result", `UPDATE runs SET result = '{'`, func(s *Store) error { return second(s.Runs(ctx, "t1")) }},
		{"event type", `UPDATE events SET type = 'nap'`, func(s *Store) error { return second(s.Events(ctx, "t1")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newStore(t)
			createTasks(t, s, "t1")
			mustTx(t, s, func(ctx context.Context, tx *Tx) error {
				return tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1})
			})
			if _, err := s.db.ExecContext(ctx, tt.sql); err != nil {
				t.Fatal(err)
			}
			if err := tt.read(s); !errors.Is(err, ErrCorrupt) {
				t.Errorf("read: %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestTxRunReads(t *testing.T) {
	s, _ := newStore(t)
	createTasks(t, s, "t1", "t2")
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		for _, r := range []model.Run{{ID: "r2", TaskID: "t1", Number: 2}, {ID: "r1", TaskID: "t1", Number: 1}} {
			if err := tx.CreateRun(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		r, err := tx.Run(ctx, "r1")
		if err != nil || r.Number != 1 {
			t.Errorf("Run(r1) = %+v, %v; want number 1", r, err)
		}
		latest, err := tx.LatestRun(ctx, "t1")
		if err != nil || latest.ID != "r2" {
			t.Errorf("LatestRun(t1) = %+v, %v; want r2", latest, err)
		}
		if _, err := tx.Run(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Run(nope) = %v, want ErrNotFound", err)
		}
		if _, err := tx.LatestRun(ctx, "t2"); !errors.Is(err, ErrNotFound) {
			t.Errorf("LatestRun(t2) = %v, want ErrNotFound", err)
		}
		return nil
	})
}

func TestRunWorker(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	createTasks(t, s, "t1")
	w := model.Worker{Host: "h1", Agent: "t1-1"}
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		return tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1, Worker: w})
	})
	got, err := s.Run(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(w, got.Worker); d != "" {
		t.Errorf("worker after CreateRun (-want +got):\n%s", d)
	}
	w.Workspace, w.Tab, w.Pane = "ws", "tab", "pane"
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.SetRunWorker(ctx, "r1", w) })
	if got, err = s.Run(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(w, got.Worker); d != "" {
		t.Errorf("worker after SetRunWorker (-want +got):\n%s", d)
	}
	err = s.Transition(ctx, func(ctx context.Context, tx *Tx) error { return tx.SetRunWorker(ctx, "nope", w) })
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRunWorker of a missing run: %v, want %v", err, ErrNotFound)
	}
}
