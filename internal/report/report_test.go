package report

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/pkg/faninmcp"
)

const (
	oldToken = "token-of-run-1"
	curToken = "token-of-run-2"
	project  = "alpha"
)

var t0 = time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)

type env struct {
	st  *store.Store
	svc *Service
	log *bytes.Buffer
}

// newEnv stores task t1 with the rolled-over run r1 and the working run r2.
func newEnv(t *testing.T) *env {
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
	err = st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.CreateTask(ctx, model.Task{ID: "t1", Project: project, Title: "build", Shape: model.ShapePR}); err != nil {
			return err
		}
		if err := tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1, Token: oldToken,
			State: model.RunRolledOver}); err != nil {
			return err
		}
		return tx.CreateRun(ctx, model.Run{ID: "r2", TaskID: "t1", Number: 2, Token: curToken,
			State: model.RunWorking})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var buf bytes.Buffer
	return &env{st: st, svc: New(st, slog.New(slog.NewTextHandler(&buf, nil))), log: &buf}
}

func (e *env) events(t *testing.T, typ model.EventType) []model.Event {
	t.Helper()
	evs, err := e.st.Events(context.Background(), "t1")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var out []model.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func (e *env) result(t *testing.T, id model.RunID) *model.Result {
	t.Helper()
	r, err := e.st.Run(context.Background(), id)
	if err != nil {
		t.Fatalf("Run(%s): %v", id, err)
	}
	return r.Result
}

func TestReportToken(t *testing.T) {
	blocked := model.Result{Kind: model.ResultBlocked, Text: "which port?"}
	cases := []struct {
		name  string
		run   model.RunID
		token string
		want  error
		log   string
	}{
		{"AW-1_older_run_with_its_own_token_is_refused", "r1", oldToken, ErrStaleRun, "not the current run"},
		{"AW-1_older_run_token_on_the_current_run_is_refused", "r2", oldToken, ErrToken, "wrong run token"},
		{"AW-1_empty_token_is_refused", "r2", "", ErrToken, "wrong run token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			err := e.svc.Report(context.Background(), tc.run, tc.token, blocked)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Report = %v, want %v", err, tc.want)
			}
			logged := e.log.String()
			if !strings.Contains(logged, tc.log) || !strings.Contains(logged, "run_id="+string(tc.run)) ||
				!strings.Contains(logged, "task_id=t1") || !strings.Contains(logged, "project="+project) {
				t.Errorf("log = %q, want a line %q with task_id, run_id and project", logged, tc.log)
			}
			if strings.Contains(logged, oldToken) || strings.Contains(logged, curToken) {
				t.Errorf("log carries a token: %q", logged)
			}
			if got := e.events(t, model.EventResultReported); len(got) != 0 {
				t.Errorf("result_reported events = %v, want none", got)
			}
			if r := e.result(t, tc.run); r != nil {
				t.Errorf("result of %s = %+v, want none", tc.run, r)
			}
		})
	}
	t.Run("AW-1_current_run_with_its_token_is_taken", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.Report(context.Background(), "r2", curToken, blocked); err != nil {
			t.Fatalf("Report: %v", err)
		}
		if diff := cmp.Diff(&blocked, e.result(t, "r2")); diff != "" {
			t.Errorf("result (-want +got):\n%s", diff)
		}
		if got := e.events(t, model.EventResultReported); len(got) != 1 || got[0].RunID != "r2" {
			t.Errorf("result_reported events = %v, want one of r2", got)
		}
		if e.log.Len() != 0 {
			t.Errorf("log = %q, want nothing for an accepted report", e.log.String())
		}
	})
}

func TestReportEndedCurrentRunIsStale(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		return tx.SetRunState(ctx, "r2", model.RunFailed, "gone")
	}); err != nil {
		t.Fatal(err)
	}
	err := e.svc.Report(ctx, "r2", curToken, model.Result{Kind: model.ResultNothing, Text: "late"})
	if !errors.Is(err, ErrStaleRun) {
		t.Fatalf("Report = %v, want ErrStaleRun", err)
	}
	if got := e.events(t, model.EventResultReported); len(got) != 0 {
		t.Errorf("result_reported events = %v, want none", got)
	}
}

func TestReportUnknownRun(t *testing.T) {
	e := newEnv(t)
	err := e.svc.Report(context.Background(), "nope", curToken, model.Result{Kind: model.ResultNothing, Text: "x"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Report = %v, want store.ErrNotFound", err)
	}
}

func TestReportBlockedTextIsCapped(t *testing.T) {
	t.Run("AW-2_long_blocked_text_is_one_line_of_400", func(t *testing.T) {
		e := newEnv(t)
		long := "which\nport\tshall " + strings.Repeat("x", 1000)
		if err := e.svc.Report(context.Background(), "r2", curToken,
			model.Result{Kind: model.ResultBlocked, Text: long}); err != nil {
			t.Fatalf("Report: %v", err)
		}
		got := e.result(t, "r2").Text
		if n := len([]rune(got)); n != model.FieldCap || strings.ContainsAny(got, "\n\t") ||
			!strings.HasPrefix(got, "which port shall x") {
			t.Errorf("text = %q (%d chars), want one line of %d", got, n, model.FieldCap)
		}
	})
}

func TestReportPRNumber(t *testing.T) {
	t.Run("GH-14_done_pr_without_number_is_refused", func(t *testing.T) {
		e := newEnv(t)
		err := e.svc.Report(context.Background(), "r2", curToken, model.Result{Kind: model.ResultDonePR})
		if !errors.Is(err, model.ErrInvalidResult) {
			t.Fatalf("Report = %v, want ErrInvalidResult", err)
		}
		if r := e.result(t, "r2"); r != nil {
			t.Errorf("result = %+v, want none", r)
		}
	})
	t.Run("GH-14_done_pr_carries_its_number", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.Report(context.Background(), "r2", curToken,
			model.Result{Kind: model.ResultDonePR, PR: 42}); err != nil {
			t.Fatalf("Report: %v", err)
		}
		if got := e.result(t, "r2"); got.PR != 42 {
			t.Errorf("PR = %d, want 42", got.PR)
		}
	})
}

func TestReportAnalysisKinds(t *testing.T) {
	t.Run("FP-3_issue_nothing_and_blocked_arrive_as_own_events", func(t *testing.T) {
		e := newEnv(t)
		ctx := context.Background()
		reports := []model.Result{
			{Kind: model.ResultIssue, Issue: 7, IssueURL: "https://forge.example/o/r/issues/7"},
			{Kind: model.ResultNothing, Text: "no open task"},
			{Kind: model.ResultBlocked, Text: "which port?"},
		}
		for _, r := range reports {
			if err := e.svc.Report(ctx, "r2", curToken, r); err != nil {
				t.Fatalf("Report(%s): %v", r.Kind, err)
			}
		}
		var got []string
		for _, ev := range e.events(t, model.EventResultReported) {
			got = append(got, ev.Detail)
		}
		want := []string{string(model.ResultIssue), string(model.ResultNothing), string(model.ResultBlocked)}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("result_reported details (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(&reports[2], e.result(t, "r2")); diff != "" {
			t.Errorf("newest result (-want +got):\n%s", diff)
		}
	})
}

func TestProgress(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.svc.Progress(ctx, "r2", curToken, "running\ntests "+strings.Repeat("y", 500)); err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if err := e.svc.Progress(ctx, "r2", curToken, " "); err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if err := e.svc.Progress(ctx, "r1", oldToken, "old"); !errors.Is(err, ErrStaleRun) {
		t.Fatalf("Progress of r1 = %v, want ErrStaleRun", err)
	}
	if err := e.svc.Progress(ctx, "r2", oldToken, "old"); !errors.Is(err, ErrToken) {
		t.Fatalf("Progress with r1's token = %v, want ErrToken", err)
	}
	evs := e.events(t, model.EventProgress)
	if len(evs) != 2 {
		t.Fatalf("progress events = %v, want 2", evs)
	}
	if d := evs[0].Detail; len([]rune(d)) != model.FieldCap || !strings.HasPrefix(d, "running tests y") {
		t.Errorf("detail = %q, want one line of %d", d, model.FieldCap)
	}
	if evs[1].Detail != heartbeatDetail || evs[1].RunID != "r2" {
		t.Errorf("second event = %+v, want a heartbeat of r2", evs[1])
	}
}

func TestFromWire(t *testing.T) {
	got, err := FromWire(faninmcp.Report{Kind: faninmcp.KindIssue, Issue: 3, IssueURL: "https://x/3", Text: "n"})
	if err != nil {
		t.Fatal(err)
	}
	want := model.Result{Kind: model.ResultIssue, Issue: 3, IssueURL: "https://x/3", Text: "n"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FromWire (-want +got):\n%s", diff)
	}
	if _, err := FromWire(faninmcp.Report{Kind: "finished"}); !errors.Is(err, model.ErrUnknownValue) {
		t.Errorf("FromWire(finished) = %v, want ErrUnknownValue", err)
	}
}

// TestKindsMatchModel keeps the public contract and the model in step.
func TestKindsMatchModel(t *testing.T) {
	var got []string
	for _, k := range faninmcp.Kinds {
		got = append(got, string(k))
	}
	var want []string
	for _, k := range model.AllResultKinds {
		want = append(want, string(k))
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("faninmcp.Kinds differ from model.AllResultKinds (-want +got):\n%s", diff)
	}
	if faninmcp.TextCap != model.FieldCap {
		t.Errorf("faninmcp.TextCap = %d, want model.FieldCap %d", faninmcp.TextCap, model.FieldCap)
	}
}
