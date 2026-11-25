package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

var errSend = errors.New("pane gone")

type recordingDeliverer struct {
	got []owner.Delivery
	err error
}

func (d *recordingDeliverer) Deliver(_ context.Context, del owner.Delivery) error {
	if d.err != nil {
		return d.err
	}
	d.got = append(d.got, del)
	return nil
}

type ownerEnv struct {
	st  *store.Store
	del *recordingDeliverer
	url string
	c   *client.Client
}

// newOwnerEnv serves the API with the owner channel over a real store; task t1 of alpha runs in r1.
func newOwnerEnv(t *testing.T) *ownerEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fanin.db"), clock.NewManual(t0))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	del := &recordingDeliverer{}
	reg := NewRegistry()
	RegisterBuiltins(reg)
	h, err := New(Config{Source: st, OwnerToken: ownerToken, Clock: clock.NewManual(t0), Registry: reg,
		Projects: configured, Owner: owner.New(st, del, nil)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e := &ownerEnv{st: st, del: del, url: srv.URL, c: client.New(srv.URL, ownerToken, nil)}
	e.tx(t, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: "t1", Project: projAlpha, Title: "build", Shape: model.ShapePR}); err != nil {
			return err
		}
		if err := tx.SetTaskState(ctx, "t1", model.TaskRunning, ""); err != nil {
			return err
		}
		return tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1})
	})
	return e
}

func (e *ownerEnv) tx(t *testing.T, fn func(ctx context.Context, tx *store.Tx) error) {
	t.Helper()
	if err := e.st.Transition(context.Background(), fn); err != nil {
		t.Fatalf("Transition: %v", err)
	}
}

func (e *ownerEnv) ask(t *testing.T, kind model.QuestionKind, text bool) {
	t.Helper()
	e.tx(t, func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AskQuestion(ctx, model.OwnerQuestion{ID: "q1", TaskID: "t1", RunID: "r1", Kind: kind,
			AllowText: text, Question: "alpha: ship now?", Options: []model.Option{{Label: "ship"}, {Label: "wait"}}})
		return err
	})
}

func (e *ownerEnv) events(t *testing.T, typ model.EventType) []model.Event {
	t.Helper()
	evs, err := e.st.EventsAfter(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func wantAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *client.Error
	if !errors.As(err, &ae) || ae.Status != status || ae.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

func TestAnswerRoute(t *testing.T) {
	ctx := context.Background()
	t.Run("OA-1_free_text_on_a_dialog_is_refused_with_the_options", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.ask(t, model.QuestionDialog, false)
		_, err := e.c.Answer(ctx, "t1", "ship it")
		wantAPIError(t, err, http.StatusBadRequest, wire.CodeBadRequest)
		if !strings.Contains(err.Error(), "1) ship, 2) wait") {
			t.Errorf("error %q does not list the options", err)
		}
		if len(e.del.got) != 0 || len(e.events(t, model.EventQuestionAnswered)) != 0 {
			t.Errorf("sent %v; want nothing sent or recorded", e.del.got)
		}
	})
	t.Run("OA-1_option_number", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.ask(t, model.QuestionDialog, false)
		ack, err := e.c.Answer(ctx, "t1", "2")
		if err != nil || ack.Detail != "option 2: wait" {
			t.Fatalf("Answer = %+v %v, want option 2: wait", ack, err)
		}
		if len(e.del.got) != 1 || e.del.got[0].Mode != owner.DeliverKey {
			t.Errorf("deliveries %+v, want one key press", e.del.got)
		}
	})
	t.Run("OA-2_transcript_answer_is_a_prompt_and_an_event", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.ask(t, model.QuestionTranscript, true)
		if _, err := e.c.Answer(ctx, "t1", "ship after the audit"); err != nil {
			t.Fatal(err)
		}
		if len(e.del.got) != 1 || e.del.got[0].Mode != owner.DeliverPrompt {
			t.Errorf("deliveries %+v, want one prompt", e.del.got)
		}
		evs := e.events(t, model.EventQuestionAnswered)
		if len(evs) != 1 || evs[0].Detail != "owner answer for r1: ship after the audit" {
			t.Errorf("answer events %+v, want the owner answer for r1", evs)
		}
	})
	t.Run("OA-3_failed_delivery_is_an_error_and_records_nothing", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.ask(t, model.QuestionTranscript, false)
		e.del.err = errSend
		_, err := e.c.Answer(ctx, "t1", "1")
		wantAPIError(t, err, http.StatusBadGateway, wire.CodeDeliveryFailed)
		if len(e.events(t, model.EventQuestionAnswered)) != 0 {
			t.Error("a failed delivery recorded an answer")
		}
	})
	t.Run("no_open_question", func(t *testing.T) {
		e := newOwnerEnv(t)
		_, err := e.c.Answer(ctx, "t1", "1")
		wantAPIError(t, err, http.StatusNotFound, wire.CodeNotFound)
	})
	t.Run("bad_body", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.ask(t, model.QuestionDialog, false)
		err := e.c.Post(ctx, "/api/v1/tasks/t1/answer", []int{1}, nil)
		wantAPIError(t, err, http.StatusBadRequest, wire.CodeBadRequest)
	})
	t.Run("too_large_body", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.ask(t, model.QuestionTranscript, true)
		_, err := e.c.Answer(ctx, "t1", strings.Repeat("x", MaxBodyBytes))
		wantAPIError(t, err, http.StatusRequestEntityTooLarge, wire.CodeTooLarge)
	})
	t.Run("needs_the_owner_token", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.ask(t, model.QuestionDialog, false)
		c := client.New(e.url, "", nil)
		_, err := c.Answer(ctx, "t1", "1")
		wantAPIError(t, err, http.StatusUnauthorized, wire.CodeUnauthorized)
	})
}

func TestGoRoute(t *testing.T) {
	ctx := context.Background()
	t.Run("OA-6_go_on_a_ready_task_appends_owner_go", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.tx(t, func(ctx context.Context, tx *store.Tx) error {
			return tx.SetTaskState(ctx, "t1", model.TaskGating, "")
		})
		if _, err := e.c.Go(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
		if evs := e.events(t, model.EventOwnerGo); len(evs) != 1 || evs[0].TaskID != "t1" {
			t.Errorf("owner_go events %+v, want one for t1", evs)
		}
	})
	t.Run("go_on_a_running_task_is_a_conflict", func(t *testing.T) {
		e := newOwnerEnv(t)
		_, err := e.c.Go(ctx, "t1")
		wantAPIError(t, err, http.StatusConflict, wire.CodeConflict)
	})
	t.Run("go_on_an_unknown_task", func(t *testing.T) {
		e := newOwnerEnv(t)
		_, err := e.c.Go(ctx, "nope")
		wantAPIError(t, err, http.StatusNotFound, wire.CodeNotFound)
	})
}

func TestHoldLiftRoute(t *testing.T) {
	ctx := context.Background()
	t.Run("MG-4_hold_lift_lifts_the_hold", func(t *testing.T) {
		e := newOwnerEnv(t)
		e.tx(t, func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.SetHold(ctx, model.Hold{ID: "h1", Project: projAlpha, Kind: model.HoldMerge, Reason: "merge broke"})
			return err
		})
		if _, err := e.c.LiftHold(ctx, "h1"); err != nil {
			t.Fatal(err)
		}
		if holds, err := e.st.AllHolds(ctx); err != nil || len(holds) != 0 {
			t.Errorf("holds = %v %v, want none", holds, err)
		}
		_, err := e.c.LiftHold(ctx, "h1")
		wantAPIError(t, err, http.StatusNotFound, wire.CodeNotFound)
	})
}

func TestPauseRoutes(t *testing.T) {
	ctx := context.Background()
	// see SCENARIOS FP-2, CH-3
	t.Run("FP-2_CH-3_pause_sets_the_flag_the_next_worker_consumes", func(t *testing.T) {
		e := newOwnerEnv(t)
		ack, err := e.c.Pause(ctx, projAlpha)
		if err != nil || ack.Detail != detailPaused {
			t.Fatalf("Pause = %+v %v, want %q", ack, err, detailPaused)
		}
		if ack, err := e.c.Pause(ctx, projAlpha); err != nil || ack.Detail != detailAlreadyPaused {
			t.Errorf("second Pause = %+v %v, want %q", ack, err, detailAlreadyPaused)
		}
		var consumed bool
		e.tx(t, func(ctx context.Context, tx *store.Tx) error {
			var err error
			consumed, err = tx.ConsumePause(ctx, projAlpha)
			return err
		})
		if p, err := e.st.Paused(ctx, projAlpha); !consumed || err != nil || p {
			t.Errorf("consumed=%v paused=%v %v; want the flag consumed", consumed, p, err)
		}
	})
	t.Run("resume", func(t *testing.T) {
		e := newOwnerEnv(t)
		if _, err := e.c.Pause(ctx, projAlpha); err != nil {
			t.Fatal(err)
		}
		if ack, err := e.c.Resume(ctx, projAlpha); err != nil || ack.Detail != detailResumed {
			t.Fatalf("Resume = %+v %v", ack, err)
		}
		if p, err := e.st.Paused(ctx, projAlpha); err != nil || p {
			t.Errorf("paused after resume = %v %v, want false", p, err)
		}
	})
	t.Run("unknown_project", func(t *testing.T) {
		e := newOwnerEnv(t)
		_, err := e.c.Pause(ctx, "nope")
		wantAPIError(t, err, http.StatusNotFound, wire.CodeNotFound)
		_, err = e.c.Resume(ctx, "nope")
		wantAPIError(t, err, http.StatusNotFound, wire.CodeNotFound)
	})
}

func TestOwnerRoutesWithoutOwnerFailClosed(t *testing.T) {
	e := newEnv(t)
	_, err := client.New(e.srv.URL, ownerToken, nil).Go(context.Background(), "t1")
	wantAPIError(t, err, http.StatusInternalServerError, wire.CodeInternal)
}

func TestQuestionsRoute(t *testing.T) {
	e := newOwnerEnv(t)
	qs, err := e.c.Questions(context.Background())
	if err != nil || len(qs.Questions) != 0 {
		t.Fatalf("Questions = %+v %v, want an empty list", qs, err)
	}
	e.ask(t, model.QuestionDialog, false)
	qs, err = e.c.Questions(context.Background())
	if err != nil || len(qs.Questions) != 1 || qs.Questions[0].Kind != string(model.QuestionDialog) ||
		len(qs.Questions[0].Options) != 2 {
		t.Errorf("Questions = %+v %v, want the q1 dialog with 2 options", qs, err)
	}
}

func TestQuestionsRouteFailsClosed(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)
	h, err := New(Config{Source: unreadableEvents{}, OwnerToken: ownerToken, Clock: clock.NewManual(t0), Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, err = client.New(srv.URL, "", nil).Questions(context.Background())
	wantAPIError(t, err, http.StatusInternalServerError, wire.CodeInternal)
}

type unreadableEvents struct{ failingSource }

func (unreadableEvents) Changed() <-chan struct{} { return make(chan struct{}) }

func (unreadableEvents) OpenQuestions(context.Context) ([]model.OwnerQuestion, error) {
	return nil, errStore
}

func (unreadableEvents) EventsAfter(context.Context, int64, int) ([]model.Event, error) {
	return nil, errStore
}

type waitResult struct {
	ev  wire.Event
	err error
}

func startWait(ctx context.Context, c *client.Client, ownerOnly bool, after int64) <-chan waitResult {
	out := make(chan waitResult, 1)
	safego.Go(ctx, slog.Default(), func(ctx context.Context) {
		ev, err := c.Wait(ctx, ownerOnly, after)
		out <- waitResult{ev, err}
	})
	return out
}

func TestWaitRoute(t *testing.T) {
	t.Run("WA-2_wait_owner_returns_exactly_one_owner_event", func(t *testing.T) {
		e := newOwnerEnv(t)
		seq, err := e.st.LastEventSeq(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := startWait(context.Background(), e.c, true, seq)
		e.tx(t, func(ctx context.Context, tx *store.Tx) error {
			return tx.CreateTask(ctx, model.Task{ID: "t2", Project: projAlpha, Title: "noise", Shape: model.ShapePR})
		})
		e.ask(t, model.QuestionDialog, false)
		e.tx(t, func(ctx context.Context, tx *store.Tx) error {
			_, err := tx.SetHold(ctx, model.Hold{Project: projAlpha, Kind: model.HoldMerge, Reason: "later"})
			return err
		})
		r := <-got
		if r.err != nil || r.ev.Type != string(model.EventQuestionAsked) || r.ev.TaskID != "t1" {
			t.Errorf("Wait = %+v %v, want the one question_asked of t1", r.ev, r.err)
		}
	})
	t.Run("WA-2_wait_without_owner_returns_the_next_event", func(t *testing.T) {
		e := newOwnerEnv(t)
		r := <-startWait(context.Background(), e.c, false, 0)
		if r.err != nil || r.ev.Seq != 1 || r.ev.Type != string(model.EventTaskCreated) {
			t.Errorf("Wait = %+v %v, want event 1 task_created", r.ev, r.err)
		}
	})
	t.Run("WA-2_wait_ends_when_the_context_ends", func(t *testing.T) {
		e := newOwnerEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		got := startWait(ctx, e.c, true, client.FromNow)
		cancel()
		if r := <-got; !errors.Is(r.err, context.Canceled) {
			t.Errorf("Wait after cancel = %+v %v, want context.Canceled", r.ev, r.err)
		}
	})
	t.Run("bad_parameters", func(t *testing.T) {
		e := newEnv(t)
		for _, path := range []string{"/api/v1/wait?owner=maybe", "/api/v1/wait?after=-3"} {
			resp := e.do(t, http.MethodGet, path, "", nil)
			if resp.StatusCode != http.StatusBadRequest || decodeError(t, resp).Code != wire.CodeBadRequest {
				t.Errorf("GET %s = %d, want 400 bad_request", path, resp.StatusCode)
			}
		}
	})
	t.Run("unreadable_store", func(t *testing.T) {
		reg := NewRegistry()
		RegisterBuiltins(reg)
		h, err := New(Config{Source: unreadableEvents{}, OwnerToken: ownerToken, Clock: clock.NewManual(t0), Registry: reg})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		_, err = client.New(srv.URL, "", nil).Wait(context.Background(), true, 0)
		wantAPIError(t, err, http.StatusInternalServerError, wire.CodeInternal)
	})
}
