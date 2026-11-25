package owner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

const (
	proj    = "alpha"
	taskID  = model.TaskID("t1")
	runID   = model.RunID("r1")
	optShip = "ship"
	optWait = "wait"
)

var errSend = errors.New("send failed")

// fakeDeliverer records every delivery and fails with err when set.
type fakeDeliverer struct {
	got []Delivery
	err error
}

func (f *fakeDeliverer) Deliver(_ context.Context, d Delivery) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, d)
	return nil
}

type env struct {
	st  *store.Store
	del *fakeDeliverer
	svc *Service
}

// newEnv opens a real store with task t1 running in run r1.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fanin.db"),
		clock.NewManual(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: taskID, Project: proj, Title: "build", Shape: model.ShapePR}); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, taskID, model.TaskRunning, ""); err != nil {
			return err
		}
		return tx.CreateRun(ctx, model.Run{ID: runID, TaskID: taskID, Number: 1})
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	del := &fakeDeliverer{}
	return &env{st: st, del: del, svc: New(st, del, nil)}
}

func (e *env) ask(t *testing.T, kind model.QuestionKind, text bool) string {
	t.Helper()
	id, err := e.svc.Ask(context.Background(), model.OwnerQuestion{TaskID: taskID, RunID: runID, Kind: kind,
		AllowText: text, Question: "alpha: ship the cache now?",
		Options: []model.Option{{Label: optShip}, {Label: optWait}}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	return id
}

func (e *env) state(t *testing.T) model.TaskState {
	t.Helper()
	var s model.TaskState
	if err := e.st.Transition(context.Background(), func(ctx context.Context, tx *store.Tx) error {
		task, err := tx.Task(ctx, taskID)
		s = task.State
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) events(t *testing.T, typ model.EventType) []string {
	t.Helper()
	evs, err := e.st.EventsAfter(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev.Detail)
		}
	}
	return out
}

func TestAnswer(t *testing.T) {
	t.Run("OA-1_dialog_takes_only_an_option_number", func(t *testing.T) {
		for _, raw := range []string{"ship it", "0", "3", "12", ""} {
			e := newEnv(t)
			id := e.ask(t, model.QuestionDialog, false)
			_, err := e.svc.Answer(context.Background(), taskID, raw)
			if !errors.Is(err, ErrInvalidAnswer) {
				t.Fatalf("Answer(%q) = %v, want ErrInvalidAnswer", raw, err)
			}
			if want := "1) ship, 2) wait"; !strings.Contains(err.Error(), want) {
				t.Errorf("Answer(%q) error %q does not list the options %q", raw, err, want)
			}
			q, qerr := e.st.Question(context.Background(), id)
			if qerr != nil || q.Answer != "" || len(e.del.got) != 0 || len(e.events(t, model.EventQuestionAnswered)) != 0 {
				t.Errorf("Answer(%q): answer %q, sent %v; want nothing sent or recorded", raw, q.Answer, e.del.got)
			}
		}
	})
	t.Run("OA-1_option_number_is_pressed_as_a_key", func(t *testing.T) {
		e := newEnv(t)
		e.ask(t, model.QuestionDialog, false)
		if _, err := e.svc.Answer(context.Background(), taskID, " 2 "); err != nil {
			t.Fatal(err)
		}
		if len(e.del.got) != 1 || e.del.got[0].Mode != DeliverKey || e.del.got[0].Option != 2 || e.del.got[0].Text != optWait {
			t.Errorf("deliveries %+v, want key 2 (%s)", e.del.got, optWait)
		}
		if got := e.state(t); got != model.TaskRunning {
			t.Errorf("task state %s after the answer, want %s", got, model.TaskRunning)
		}
	})
	t.Run("OA-2_transcript_answer_goes_in_as_a_prompt_and_is_logged", func(t *testing.T) {
		e := newEnv(t)
		id := e.ask(t, model.QuestionTranscript, true)
		d, err := e.svc.Answer(context.Background(), taskID, "ship after the audit")
		if err != nil {
			t.Fatal(err)
		}
		want := Delivery{Mode: DeliverPrompt, Text: "ship after the audit"}
		if diff := cmp.Diff(want, d, cmpIgnoreQuestion); diff != "" || d.Question.ID != id {
			t.Errorf("delivery (-want +got):\n%s", diff)
		}
		if len(e.del.got) != 1 {
			t.Errorf("deliveries %d, want 1", len(e.del.got))
		}
		logged := []string{"owner answer for r1: ship after the audit"}
		if diff := cmp.Diff(logged, e.events(t, model.EventQuestionAnswered)); diff != "" {
			t.Errorf("answer events (-want +got):\n%s", diff)
		}
	})
	t.Run("OA-3_failed_delivery_records_nothing", func(t *testing.T) {
		e := newEnv(t)
		id := e.ask(t, model.QuestionTranscript, false)
		e.del.err = errSend
		_, err := e.svc.Answer(context.Background(), taskID, "1")
		if !errors.Is(err, ErrDelivery) || !errors.Is(err, errSend) {
			t.Fatalf("Answer = %v, want ErrDelivery wrapping the send error", err)
		}
		q, qerr := e.st.Question(context.Background(), id)
		if qerr != nil || q.Answer != "" || len(e.events(t, model.EventQuestionAnswered)) != 0 {
			t.Errorf("after a failed send: answer %q err %v; want nothing recorded", q.Answer, qerr)
		}
		if got := e.state(t); got != model.TaskNeedsOwner {
			t.Errorf("task state %s, want still %s", got, model.TaskNeedsOwner)
		}
	})
	t.Run("OA-3_no_deliverer_fails_closed", func(t *testing.T) {
		e := newEnv(t)
		e.ask(t, model.QuestionTranscript, false)
		svc := New(e.st, NoDeliverer{}, nil)
		if _, err := svc.Answer(context.Background(), taskID, "1"); !errors.Is(err, ErrNoDeliverer) {
			t.Errorf("Answer = %v, want ErrNoDeliverer", err)
		}
	})
	t.Run("no_open_question", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Answer(context.Background(), taskID, "1"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Answer = %v, want ErrNotFound", err)
		}
	})
	t.Run("answer_without_a_run_requeues", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Ask(context.Background(), model.OwnerQuestion{TaskID: taskID, Kind: model.QuestionTranscript,
			Question: "q?", Options: []model.Option{{Label: optShip}, {Label: optWait}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Answer(context.Background(), taskID, "1"); err != nil {
			t.Fatal(err)
		}
		if got := e.state(t); got != model.TaskQueued {
			t.Errorf("task state %s, want %s", got, model.TaskQueued)
		}
	})
}

func TestAskDialog(t *testing.T) {
	t.Run("AW-9_a_dialog_becomes_a_question_row", func(t *testing.T) {
		e := newEnv(t)
		id, err := e.svc.AskDialog(context.Background(), Dialog{TaskID: taskID, RunID: runID,
			Text: "Allow git push?", Options: []string{"Yes", "No"}})
		if err != nil {
			t.Fatal(err)
		}
		open, err := e.st.OpenQuestions(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(open) != 1 || open[0].ID != id || open[0].Kind != model.QuestionDialog || open[0].AllowText ||
			open[0].Question != "Allow git push?" || open[0].Project != proj {
			t.Errorf("open questions %+v, want one dialog row for %s", open, proj)
		}
		if got := e.state(t); got != model.TaskNeedsOwner {
			t.Errorf("task state %s, want %s", got, model.TaskNeedsOwner)
		}
	})
	t.Run("AW-9_unreadable_dialog_says_so", func(t *testing.T) {
		e := newEnv(t)
		id, err := e.svc.AskDialog(context.Background(), Dialog{TaskID: taskID, RunID: runID, Text: " ",
			Options: []string{"1", "2"}})
		if err != nil {
			t.Fatal(err)
		}
		q, err := e.st.Question(context.Background(), id)
		if err != nil || q.Question != unreadableDialog {
			t.Errorf("question %q %v, want %q", q.Question, err, unreadableDialog)
		}
	})
	t.Run("invalid_dialog_writes_nothing", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.svc.AskDialog(context.Background(), Dialog{TaskID: taskID, RunID: runID, Text: "x?"})
		if !errors.Is(err, model.ErrInvalidQuestion) {
			t.Fatalf("AskDialog without options = %v, want ErrInvalidQuestion", err)
		}
		if got := e.state(t); got != model.TaskRunning {
			t.Errorf("task state %s, want still %s", got, model.TaskRunning)
		}
	})
	t.Run("second_question_keeps_needs_owner", func(t *testing.T) {
		e := newEnv(t)
		e.ask(t, model.QuestionTranscript, false)
		e.ask(t, model.QuestionDialog, false)
		if got := e.state(t); got != model.TaskNeedsOwner {
			t.Errorf("task state %s, want %s", got, model.TaskNeedsOwner)
		}
	})
	t.Run("unknown_task", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.svc.AskDialog(context.Background(), Dialog{TaskID: "nope", Text: "x?", Options: []string{"a", "b"}})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("AskDialog = %v, want ErrNotFound", err)
		}
	})
}

var cmpIgnoreQuestion = cmp.FilterPath(func(p cmp.Path) bool { return p.String() == "Question" }, cmp.Ignore())
