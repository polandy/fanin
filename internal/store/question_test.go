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
	want.ID, want.Project, want.Kind, want.AskedAt = id, project, model.QuestionTranscript, t0
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

func TestOpenTaskQuestion(t *testing.T) {
	s, clk := newStore(t)
	ctx := context.Background()
	createTasks(t, s, "t1", "t2")
	if _, err := s.OpenTaskQuestion(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenTaskQuestion without a question = %v, want ErrNotFound", err)
	}
	dialog := question("t1")
	dialog.Kind = model.QuestionDialog
	var first, second string
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		var err error
		first, err = tx.AskQuestion(ctx, question("t1"))
		return err
	})
	clk.Advance(time.Second)
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		var err error
		second, err = tx.AskQuestion(ctx, dialog)
		return err
	})
	got, err := s.OpenTaskQuestion(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != second || got.Kind != model.QuestionDialog || got.AllowText {
		t.Errorf("OpenTaskQuestion = %q %s text=%v, want the newest %q dialog without text", got.ID, got.Kind,
			got.AllowText, second)
	}
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.AnswerQuestion(ctx, second, "now") })
	if got, err := s.OpenTaskQuestion(ctx, "t1"); err != nil || got.ID != first {
		t.Errorf("OpenTaskQuestion after the answer = %q %v, want %q", got.ID, err, first)
	}
	if _, err := s.OpenTaskQuestion(ctx, "t2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenTaskQuestion of another task = %v, want ErrNotFound", err)
	}
}

func TestQuestionAllowText(t *testing.T) {
	s, _ := newStore(t)
	createTasks(t, s, "t1")
	q := question("t1")
	q.AllowText = true
	var id string
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		var err error
		id, err = tx.AskQuestion(ctx, q)
		return err
	})
	got, err := s.Question(context.Background(), id)
	if err != nil || !got.AllowText {
		t.Errorf("Question = %+v %v, want AllowText", got, err)
	}
	bad := question("t1")
	bad.Kind, bad.AllowText = model.QuestionDialog, true
	err = s.Transition(context.Background(), func(ctx context.Context, tx *Tx) error {
		_, err := tx.AskQuestion(ctx, bad)
		return err
	})
	if !errors.Is(err, model.ErrInvalidQuestion) {
		t.Errorf("AskQuestion of a dialog with free text = %v, want ErrInvalidQuestion", err)
	}
}

// OA-2: the answer is logged as "owner answer for <run>: <answer>".
func TestAnswerQuestionEvent(t *testing.T) {
	s, _ := newStore(t)
	createTasks(t, s, "t1")
	noRun := question("t1")
	noRun.RunID = ""
	var withRun, taskOnly string
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		var err error
		if withRun, err = tx.AskQuestion(ctx, question("t1")); err != nil {
			return err
		}
		taskOnly, err = tx.AskQuestion(ctx, noRun)
		return err
	})
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		if err := tx.AnswerQuestion(ctx, withRun, "now"); err != nil {
			return err
		}
		return tx.AnswerQuestion(ctx, taskOnly, "later")
	})
	evs, err := s.Events(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		if e.Type == model.EventQuestionAnswered {
			got = append(got, e.Detail)
		}
	}
	want := []string{"owner answer for r1: now", "owner answer for t1: later"}
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("answer events (-want +got):\n%s", d)
	}
}

func TestOpenRunQuestions(t *testing.T) {
	s, clk := newStore(t)
	createTasks(t, s, "t1")
	other := question("t1")
	other.RunID = "r2"
	ids := map[string]string{}
	for _, q := range []struct {
		name string
		q    model.OwnerQuestion
	}{{"first", question("t1")}, {"other", other}, {"answered", question("t1")}, {"second", question("t1")}} {
		mustTx(t, s, func(ctx context.Context, tx *Tx) error {
			id, err := tx.AskQuestion(ctx, q.q)
			ids[q.name] = id
			return err
		})
		clk.Advance(time.Second)
	}
	mustTx(t, s, func(ctx context.Context, tx *Tx) error { return tx.AnswerQuestion(ctx, ids["answered"], "now") })
	var got []string
	mustTx(t, s, func(ctx context.Context, tx *Tx) error {
		qs, err := tx.OpenRunQuestions(ctx, "r1")
		for _, q := range qs {
			got = append(got, q.ID)
		}
		return err
	})
	if d := cmp.Diff([]string{ids["first"], ids["second"]}, got); d != "" {
		t.Errorf("WA-3 open questions of r1, oldest first (-want +got):\n%s", d)
	}
}
