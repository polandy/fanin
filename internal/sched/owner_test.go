package sched

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const pushDialog = "Allow git push? 1. Yes / 2. No"

var errOwnerDown = errors.New("owner channel down")

func pushDialogEvent() runtime.RunEvent {
	return runtime.RunEvent{Type: runtime.RunEventDialog, Dialog: pushDialog, Options: []string{"Yes", "No"}}
}

// withOwner wires the real owner channel, delivering through the scheduler's runtime and noting through h.nt.
func (h *harness) withOwner() *owner.Service {
	svc := owner.New(h.st, OwnerDelivery{Store: h.st, Runtime: h.rt}, slog.New(slog.DiscardHandler))
	svc.SetNotifier(h.nt)
	h.questions = svc
	return svc
}

// sendHeld is send for an event whose step fails: the retry holds the next step, so it waits only for the apply.
func (h *harness) sendHeld(agent string, ev runtime.RunEvent) {
	h.t.Helper()
	n := h.s.applied.Load()
	h.rt.mu.Lock()
	feed := h.rt.feed(agent)
	h.rt.mu.Unlock()
	go func() { feed <- ev }()
	h.until(func() bool { return h.s.applied.Load() > n })
}

func (h *harness) openQuestions() []model.OwnerQuestion {
	h.t.Helper()
	qs, err := h.st.OpenQuestions(h.t.Context())
	if err != nil {
		h.t.Fatal(err)
	}
	return qs
}

// flakyQuestions fails its first calls, then hands on to the real channel.
type flakyQuestions struct {
	Questions
	fails int
	calls int
}

func (f *flakyQuestions) AskDialog(ctx context.Context, d owner.Dialog) (string, error) {
	f.calls++
	if f.fails > 0 {
		f.fails--
		return "", errOwnerDown
	}
	return f.Questions.AskDialog(ctx, d)
}

func (f *flakyQuestions) AnsweredInPane(ctx context.Context, run model.RunID) (int, error) {
	f.calls++
	if f.fails > 0 {
		f.fails--
		return 0, errOwnerDown
	}
	return f.Questions.AnsweredInPane(ctx, run)
}

func TestDialogToOwner(t *testing.T) {
	t.Run("AW-9_a_dialog_becomes_an_owner_question_and_a_note", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.withOwner()
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		qs := h.openQuestions()
		if len(qs) != 1 {
			t.Fatalf("open questions = %+v, want one", qs)
		}
		q := qs[0]
		want := []model.Option{{Label: "Yes"}, {Label: "No"}}
		if q.Kind != model.QuestionDialog || q.Question != pushDialog || q.RunID != run.ID ||
			!cmp.Equal(want, q.Options) {
			t.Errorf("question = %+v, want the dialog of %s with options %v", q, run.ID, want)
		}
		if got := h.task("a1").State; got != model.TaskNeedsOwner {
			t.Errorf("task = %s, want %s", got, model.TaskNeedsOwner)
		}
		if notes := h.nt.sent(); len(notes) != 1 || notes[0].Text != pushDialog {
			t.Errorf("notes = %+v, want one with the dialog", notes)
		}
	})

	t.Run("AW-9_the_same_dialog_after_a_restart_asks_once", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.withOwner()
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		h.stop()
		h.restartWith(func(*fakeRuntime) { h.withOwner() })
		h.until(func() bool { return len(h.rt.observes()) > 0 })
		h.send(run.Worker.Agent, pushDialogEvent())
		if qs := h.openQuestions(); len(qs) != 1 {
			t.Errorf("open questions = %d, want 1", len(qs))
		}
		if notes := h.nt.sent(); len(notes) != 1 {
			t.Errorf("notes = %d, want 1", len(notes))
		}
	})

	t.Run("AW-9_an_unreadable_dialog_only_notes_the_owner", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.withOwner()
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventDialog, Dialog: runtime.DialogUnreadable})
		if qs := h.openQuestions(); len(qs) != 0 {
			t.Errorf("open questions = %+v, want none", qs)
		}
		notes := h.nt.sent()
		if len(notes) != 1 || !strings.Contains(notes[0].Text, runtime.DialogUnreadable) ||
			!strings.Contains(notes[0].Text, run.Worker.Agent) {
			t.Errorf("notes = %+v, want one naming the agent and the unreadable dialog", notes)
		}
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want still %s", got, model.TaskRunning)
		}
	})

	t.Run("AW-9_a_failed_ask_is_retried", func(t *testing.T) {
		h := newHarness(t, testConfig())
		svc := h.withOwner()
		flaky := &flakyQuestions{Questions: svc, fails: 1}
		h.questions = flaky
		h.start()
		run := h.startOwner("a1")
		h.sendHeld(run.Worker.Agent, pushDialogEvent())
		if qs := h.openQuestions(); len(qs) != 0 || flaky.calls != 1 {
			t.Fatalf("after a failed ask: %d questions, %d calls; want none and one call", len(qs), flaky.calls)
		}
		if !strings.Contains(h.logs.String(), errOwnerDown.Error()) {
			t.Errorf("log does not name the failure:\n%s", h.logs.String())
		}
		h.clk.Advance(RetryAfter)
		h.until(func() bool { return len(h.openQuestions()) == 1 })
	})

	t.Run("AW-9_without_an_owner_channel_the_dialog_is_only_watched", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		if qs := h.openQuestions(); len(qs) != 0 {
			t.Errorf("open questions = %+v, want none", qs)
		}
	})
}

func TestAnsweredInPaneToOwner(t *testing.T) {
	t.Run("WA-3_an_answer_in_the_pane_closes_the_open_question", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.withOwner()
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		h.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventAnsweredInPane})
		if qs := h.openQuestions(); len(qs) != 0 {
			t.Errorf("open questions = %+v, want none", qs)
		}
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want %s", got, model.TaskRunning)
		}
	})

	t.Run("WA-3_FP-3_a_result_before_the_pane_answer_closes_the_question", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.withOwner()
		h.start()
		run := h.startAnalysis("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		h.report(run, model.Result{Kind: model.ResultNothing, Text: textNoTodo})
		h.until(h.stopped(run.Worker.Agent))
		if qs := h.openQuestions(); len(qs) != 0 {
			t.Errorf("open questions = %+v, want none", qs)
		}
		if got := h.task("a1").State; got != model.TaskDone {
			t.Errorf("task = %s, want %s", got, model.TaskDone)
		}
	})

	t.Run("WA-3_FP-3_a_failed_close_on_a_result_is_retried", func(t *testing.T) {
		h := newHarness(t, testConfig())
		svc := h.withOwner()
		h.start()
		run := h.startAnalysis("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		h.stop()
		h.questions = &flakyQuestions{Questions: svc, fails: 1}
		h.start()
		h.report(run, model.Result{Kind: model.ResultNothing, Text: textNoTodo})
		h.until(h.logged(errOwnerDown.Error()))
		if got := h.task("a1").State; got != model.TaskNeedsOwner {
			t.Fatalf("task = %s, want %s after the failed close", got, model.TaskNeedsOwner)
		}
		h.clk.Advance(RetryAfter)
		h.until(h.stopped(run.Worker.Agent))
		if got := h.task("a1").State; got != model.TaskDone {
			t.Errorf("task = %s, want %s", got, model.TaskDone)
		}
	})

	t.Run("WA-3_a_failed_close_is_retried", func(t *testing.T) {
		h := newHarness(t, testConfig())
		svc := h.withOwner()
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		h.stop()
		flaky := &flakyQuestions{Questions: svc, fails: 1}
		h.questions = flaky
		h.start()
		h.sendHeld(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventAnsweredInPane})
		if qs := h.openQuestions(); len(qs) != 1 || flaky.calls != 1 {
			t.Fatalf("after a failed close: %d open questions, %d calls; want 1 and one call", len(qs), flaky.calls)
		}
		h.clk.Advance(RetryAfter)
		h.until(func() bool { return len(h.openQuestions()) == 0 })
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want %s", got, model.TaskRunning)
		}
	})
}

func TestOwnerDelivery(t *testing.T) {
	t.Run("OA-1_an_option_number_is_pressed_in_the_worker_pane", func(t *testing.T) {
		h := newHarness(t, testConfig())
		svc := h.withOwner()
		h.start()
		run := h.startOwner("a1")
		h.send(run.Worker.Agent, pushDialogEvent())
		if _, err := svc.Answer(h.t.Context(), "a1", "2"); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"2"}, h.rt.pressed(run.Worker.Agent)); diff != "" {
			t.Errorf("keys (-want +got):\n%s", diff)
		}
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task = %s, want %s", got, model.TaskRunning)
		}
	})

	t.Run("OA-2_a_free_text_answer_goes_in_as_a_prompt", func(t *testing.T) {
		h := newHarness(t, testConfig())
		svc := h.withOwner()
		h.start()
		run := h.startOwner("a1")
		before := len(h.rt.sent(run.Worker.Agent))
		if _, err := svc.Ask(h.t.Context(), model.OwnerQuestion{TaskID: "a1", RunID: run.ID, AllowText: true,
			Question: "ship now?", Options: []model.Option{{Label: "now"}, {Label: "later"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Answer(h.t.Context(), "a1", "after the audit"); err != nil {
			t.Fatal(err)
		}
		if got := h.rt.sent(run.Worker.Agent)[before:]; !cmp.Equal([]string{"after the audit"}, got) {
			t.Errorf("prompts = %q, want the answer", got)
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(h *harness, run model.Run)
		want  error
	}{
		{"OA-3_a_refused_key_records_nothing", func(h *harness, _ model.Run) {
			h.rt.mu.Lock()
			h.rt.pressErr = runtime.ErrPromptRefused
			h.rt.mu.Unlock()
		}, runtime.ErrPromptRefused},
		{"OA-3_an_ended_run_records_nothing", func(h *harness, run model.Run) {
			h.tx(func(ctx context.Context, tx *store.Tx) error {
				return tx.SetRunState(ctx, run.ID, model.RunFailed, "worker gone")
			})
		}, ErrNoWorker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			svc := h.withOwner()
			h.start()
			run := h.startOwner("a1")
			h.send(run.Worker.Agent, pushDialogEvent())
			tc.setup(h, run)
			_, err := svc.Answer(h.t.Context(), "a1", "1")
			if !errors.Is(err, owner.ErrDelivery) || !errors.Is(err, tc.want) {
				t.Fatalf("Answer = %v, want ErrDelivery wrapping %v", err, tc.want)
			}
			if qs := h.openQuestions(); len(qs) != 1 {
				t.Errorf("open questions = %d, want the question still open", len(qs))
			}
			if got := h.rt.pressed(run.Worker.Agent); len(got) != 0 {
				t.Errorf("keys = %v, want none", got)
			}
		})
	}

	t.Run("OA-3_a_question_without_a_run_cannot_be_delivered", func(t *testing.T) {
		h := newHarness(t, testConfig())
		err := OwnerDelivery{Store: h.st, Runtime: h.rt}.Deliver(h.t.Context(),
			owner.Delivery{Question: model.OwnerQuestion{ID: "q1", TaskID: "a1"}, Mode: owner.DeliverPrompt, Text: "x"})
		if !errors.Is(err, ErrNoWorker) {
			t.Errorf("Deliver = %v, want ErrNoWorker", err)
		}
	})
}
