package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
)

func TestRestartReloadsOpenTasksAndTimers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanin.db")
	clk := clock.NewManual(t0)
	s := openStore(t, path, clk)
	createTasks(t, s, "queued", "running", "done")
	var due, later string
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		if err := tx.SetTaskState(ctx, "running", model.TaskRunning, ""); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "done", model.TaskRunning, ""); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "done", model.TaskDone, ""); err != nil {
			return err
		}
		var err error
		if due, err = tx.ArmTimer(ctx, Timer{Project: project, TaskID: "running", Kind: "stall",
			DueAt: tx.Now().Add(time.Minute)}); err != nil {
			return err
		}
		if later, err = tx.ArmTimer(ctx, Timer{ID: "later", Project: project, Kind: "poll",
			DueAt: t0.Add(time.Hour)}); err != nil {
			return err
		}
		gone, err := tx.ArmTimer(ctx, Timer{Project: project, Kind: "poll", DueAt: t0})
		if err != nil {
			return err
		}
		return tx.DisarmTimer(ctx, gone)
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	clk.Advance(time.Minute)
	s = openStore(t, path, clk)
	ctx := context.Background()
	open, err := s.OpenTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []model.TaskID
	for _, task := range open {
		ids = append(ids, task.ID)
	}
	if d := cmp.Diff([]model.TaskID{"queued", "running"}, ids); d != "" {
		t.Errorf("open tasks after restart (-want +got):\n%s", d)
	}
	armed, err := s.ArmedTimers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantArmed := []Timer{
		{ID: due, Project: project, TaskID: "running", Kind: "stall", DueAt: t0.Add(time.Minute), CreatedAt: t0},
		{ID: later, Project: project, Kind: "poll", DueAt: t0.Add(time.Hour), CreatedAt: t0},
	}
	if d := cmp.Diff(wantArmed, armed); d != "" {
		t.Errorf("armed timers after restart (-want +got):\n%s", d)
	}
	dueNow, err := s.DueTimers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(wantArmed[:1], dueNow); d != "" {
		t.Errorf("due timers after restart (-want +got):\n%s", d)
	}
}

func TestTimerFailures(t *testing.T) {
	s, _ := newStore(t)
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx *Tx) error
		want error
	}{
		{"no project", func(ctx context.Context, tx *Tx) error {
			return second(tx.ArmTimer(ctx, Timer{Kind: "poll", DueAt: t0}))
		}, ErrInvalid},
		{"no kind", func(ctx context.Context, tx *Tx) error {
			return second(tx.ArmTimer(ctx, Timer{Project: project, DueAt: t0}))
		}, ErrInvalid},
		{"no due time", func(ctx context.Context, tx *Tx) error {
			return second(tx.ArmTimer(ctx, Timer{Project: project, Kind: "poll"}))
		}, ErrInvalid},
		{"duplicate id", func(ctx context.Context, tx *Tx) error {
			if _, err := tx.ArmTimer(ctx, Timer{ID: "x", Project: project, Kind: "poll", DueAt: t0}); err != nil {
				return err
			}
			return second(tx.ArmTimer(ctx, Timer{ID: "x", Project: project, Kind: "poll", DueAt: t0}))
		}, nil},
		{"disarm unknown", func(ctx context.Context, tx *Tx) error { return tx.DisarmTimer(ctx, "nope") }, ErrNotFound},
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
