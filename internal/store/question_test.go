package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

func question(task model.TaskID) model.OwnerQuestion {
	return model.OwnerQuestion{
		TaskID: task, RunID: "r1", Question: "proj-a: ship the cache now or after the audit?",
		Options:     []model.Option{{Label: "now", Description: "ships today"}, {Label: "later"}},
		Recommended: 0,
	}
}

func TestOwnerQuestions(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	createTasks(t, s, "t1")
	var id string
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		var err error
		id, err = tx.AskQuestion(ctx, question("t1"))
		return err
	})
	open, err := s.OpenQuestions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := question("t1")
	want.ID, want.Project, want.AskedAt = id, project, t0
	if d := cmp.Diff([]model.OwnerQuestion{want}, open); d != "" {
		t.Errorf("open questions (-want +got):\n%s", d)
	}

	clk.Advance(time.Minute)
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.AnswerQuestion(ctx, id, "now") })
	got, err := s.Question(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want.Answer, want.AnsweredAt = "now", t0.Add(time.Minute)
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("answered question (-want +got):\n%s", d)
	}
	if open, err := s.OpenQuestions(ctx); err != nil || len(open) != 0 {
		t.Errorf("open questions after the answer = %v %v, want none", open, err)
	}
	wantTypes := []model.EventType{model.EventTaskCreated, model.EventQuestionAsked, model.EventQuestionAnswered}
	if d := cmp.Diff(wantTypes, eventTypes(t, s, "t1")); d != "" {
		t.Errorf("events (-want +got):\n%s", d)
	}
}

func TestOwnerQuestionFailures(t *testing.T) {
	s, _ := newStore(t)
	createTasks(t, s, "t1")
	answered := question("t1")
	answered.ID = "q1"
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.AskQuestion(ctx, answered); err != nil {
			return err
		}
		return tx.AnswerQuestion(ctx, "q1", "later")
	})
	invalid := question("t1")
	invalid.Options = invalid.Options[:1]
	tests := []struct {
		name string
		fn   func(ctx context.Context, tx *Tx) error
		want error
	}{
		{"invalid question", func(ctx context.Context, tx *Tx) error { return second(tx.AskQuestion(ctx, invalid)) },
			model.ErrInvalidQuestion},
		{caseNoTask, func(ctx context.Context, tx *Tx) error {
			return second(tx.AskQuestion(ctx, question("nope")))
		}, ErrNotFound},
		{caseDup, func(ctx context.Context, tx *Tx) error { return second(tx.AskQuestion(ctx, answered)) }, nil},
		{"empty answer", func(ctx context.Context, tx *Tx) error { return tx.AnswerQuestion(ctx, "q1", " ") },
			ErrInvalid},
		{"answered twice", func(ctx context.Context, tx *Tx) error { return tx.AnswerQuestion(ctx, "q1", "now") },
			ErrNotFound},
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
	ctx := context.Background()
	if _, err := s.Question(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Question(nope): %v, want ErrNotFound", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE owner_questions SET options = '{'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Question(ctx, "q1"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Question on a corrupt row: %v, want ErrCorrupt", err)
	}
}
