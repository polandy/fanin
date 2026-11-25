package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

var errRollback = errors.New("rollback on purpose")

func TestChangedClosesAfterCommitOnly(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	ch := s.Changed()
	if err := s.Transition(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.CreateTask(ctx, task("t1")); err != nil {
			return err
		}
		return errRollback
	}); err == nil {
		t.Fatal("Transition: want the rollback error, got nil")
	}
	select {
	case <-ch:
		t.Fatal("Changed fired for a rolled-back transaction")
	default:
	}
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.CreateTask(ctx, task("t1")) })
	<-ch
}

func TestEventsAfterReturnsOnlyNewerInOrder(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.CreateTask(ctx, task("t1")) })
	first, err := s.EventsAfter(ctx, 0, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("EventsAfter(0) = %v, %v; want one event", first, err)
	}
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.CreateTask(ctx, task("t2")) })
	got, err := s.EventsAfter(ctx, first[0].Seq, 10)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != "t2" || got[0].Seq <= first[0].Seq {
		t.Errorf("EventsAfter(%d) = %+v; want only the t2 event", first[0].Seq, got)
	}
	limited, err := s.EventsAfter(ctx, 0, 1)
	if err != nil || len(limited) != 1 {
		t.Errorf("EventsAfter(0, 1) = %v, %v; want one event", limited, err)
	}
}

func TestAllHoldsSpansProjectsAndSkipsLifted(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	a := setHold(t, s, model.Hold{Project: "a", Kind: model.HoldMerge, Reason: "r"})
	setHold(t, s, model.Hold{Project: "b", Kind: model.HoldMerge, Reason: "r"})
	liftHold(t, s, a)
	got, err := s.AllHolds(ctx)
	if err != nil {
		t.Fatalf("AllHolds: %v", err)
	}
	var projects []string
	for _, h := range got {
		projects = append(projects, h.Project)
	}
	if diff := cmp.Diff([]string{"b"}, projects); diff != "" {
		t.Errorf("AllHolds projects (-want +got):\n%s", diff)
	}
}
