package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/report"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/pkg/faninmcp"
)

const (
	runToken    = "run-2-secret"
	oldRunToken = "run-1-secret"
	nothingBody = `{"kind":"nothing","text":"x"}`
)

type reportEnv struct {
	st  *store.Store
	url string
}

// newReportEnv serves the API with the report service; task t1 of alpha has the rolled-over r1 and the working r2.
func newReportEnv(t *testing.T, withReports bool) *reportEnv {
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
		if err := tx.CreateTask(ctx, model.Task{ID: "t1", Project: projAlpha, Title: "build", Shape: model.ShapePR}); err != nil {
			return err
		}
		if err := tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1, Token: oldRunToken,
			State: model.RunRolledOver}); err != nil {
			return err
		}
		return tx.CreateRun(ctx, model.Run{ID: "r2", TaskID: "t1", Number: 2, Token: runToken, State: model.RunWorking})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	reg := NewRegistry()
	RegisterBuiltins(reg)
	cfg := Config{Source: st, OwnerToken: ownerToken, Clock: clock.NewManual(t0), Registry: reg, Projects: configured}
	if withReports {
		cfg.Reports = report.New(st, nil)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &reportEnv{st: st, url: srv.URL}
}

func (e *reportEnv) client(t *testing.T, run, token string) *faninmcp.Client {
	t.Helper()
	c, err := faninmcp.NewClient(e.url, run, token, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func (e *reportEnv) reported(t *testing.T) []model.Event {
	t.Helper()
	evs, err := e.st.Events(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Event
	for _, ev := range evs {
		if ev.Type == model.EventResultReported || ev.Type == model.EventProgress {
			out = append(out, ev)
		}
	}
	return out
}

func wantWorkerError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *faninmcp.Error
	if !errors.As(err, &ae) || ae.Status != status || ae.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

func TestReportRoutes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		run    string
		token  string
		report faninmcp.Report
		status int
		code   string
	}{
		{"AW-1_token_of_an_older_run_is_refused", "r2", oldRunToken,
			faninmcp.Report{Kind: faninmcp.KindNothing, Text: "x"}, http.StatusUnauthorized, wire.CodeUnauthorized},
		{"AW-1_older_run_reporting_is_refused", "r1", oldRunToken,
			faninmcp.Report{Kind: faninmcp.KindNothing, Text: "x"}, http.StatusConflict, wire.CodeConflict},
		{"owner_token_is_no_run_token", "r2", ownerToken,
			faninmcp.Report{Kind: faninmcp.KindNothing, Text: "x"}, http.StatusUnauthorized, wire.CodeUnauthorized},
		{"GH-14_done_pr_without_number_is_refused", "r2", runToken,
			faninmcp.Report{Kind: faninmcp.KindDonePR}, http.StatusBadRequest, wire.CodeBadRequest},
		{"unknown_kind_is_refused", "r2", runToken,
			faninmcp.Report{Kind: "finished", Text: "x"}, http.StatusBadRequest, wire.CodeBadRequest},
		{"unknown_run", "r9", runToken,
			faninmcp.Report{Kind: faninmcp.KindNothing, Text: "x"}, http.StatusNotFound, wire.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReportEnv(t, true)
			wantWorkerError(t, e.client(t, tc.run, tc.token).Report(ctx, tc.report), tc.status, tc.code)
			if got := e.reported(t); len(got) != 0 {
				t.Errorf("events = %v, want none", got)
			}
		})
	}
	t.Run("GH-14_AW-1_current_run_reports_its_PR", func(t *testing.T) {
		e := newReportEnv(t, true)
		c := e.client(t, "r2", runToken)
		if err := c.Report(ctx, faninmcp.Report{Kind: faninmcp.KindDonePR, PR: 42}); err != nil {
			t.Fatalf("Report: %v", err)
		}
		if err := c.Progress(ctx, faninmcp.Progress{Text: "tests green"}); err != nil {
			t.Fatalf("Progress: %v", err)
		}
		got := e.reported(t)
		if len(got) != 2 || got[0].Detail != string(model.ResultDonePR) || got[1].Detail != "tests green" {
			t.Fatalf("events = %+v, want result_reported done_pr and progress", got)
		}
		run, err := e.st.Run(ctx, "r2")
		if err != nil || run.Result == nil || run.Result.PR != 42 {
			t.Errorf("run = %+v, %v; want PR 42", run, err)
		}
	})
}

func TestReportRoutesRawRequests(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		auth    string
		header  string
		body    string
		status  int
		code    string
		reports bool
	}{
		{"no_token", faninmcp.ReportPath("r2"), "", "", nothingBody,
			http.StatusUnauthorized, wire.CodeUnauthorized, true},
		{"empty_bearer", faninmcp.ReportPath("r2"), "Bearer ", "", nothingBody,
			http.StatusUnauthorized, wire.CodeUnauthorized, true},
		{"webhook_headers", faninmcp.ReportPath("r2"), "Bearer " + runToken, "X-GitHub-Event",
			nothingBody, http.StatusForbidden, wire.CodeForbidden, true},
		{"unknown_field", faninmcp.ReportPath("r2"), "Bearer " + runToken, "", `{"kind":"nothing","why":"x"}`,
			http.StatusBadRequest, wire.CodeBadRequest, true},
		{"trailing_data", faninmcp.ProgressPath("r2"), "Bearer " + runToken, "", `{"text":"a"} {}`,
			http.StatusBadRequest, wire.CodeBadRequest, true},
		{"too_large", faninmcp.ProgressPath("r2"), "Bearer " + runToken, "",
			`{"text":"` + strings.Repeat("a", MaxBodyBytes) + `"}`, http.StatusRequestEntityTooLarge, wire.CodeTooLarge, true},
		{"no_report_service", faninmcp.ReportPath("r2"), "Bearer " + runToken, "", nothingBody,
			http.StatusInternalServerError, wire.CodeInternal, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReportEnv(t, tc.reports)
			req, err := http.NewRequest(http.MethodPost, e.url+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.header != "" {
				req.Header.Set(tc.header, "push")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var body wire.ErrorBody
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode the answer: %v", err)
			}
			if resp.StatusCode != tc.status || body.Error.Code != tc.code {
				t.Errorf("answer = %d %s, want %d %s", resp.StatusCode, body.Error.Code, tc.status, tc.code)
			}
			if got := e.reported(t); len(got) != 0 {
				t.Errorf("events = %v, want none", got)
			}
		})
	}
}

func TestReportRouteStoreFailure(t *testing.T) {
	e := newReportEnv(t, true)
	c := e.client(t, "r2", runToken)
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	wantWorkerError(t, c.Report(context.Background(), faninmcp.Report{Kind: faninmcp.KindNothing, Text: "x"}),
		http.StatusInternalServerError, wire.CodeInternal)
}

func TestUsageRoute(t *testing.T) {
	ctx := context.Background()
	usage := faninmcp.Usage{InputTokens: 1000, OutputTokens: 100, ContextPct: 80}
	t.Run("AW-14_current_run_sends_its_usage", func(t *testing.T) {
		e := newReportEnv(t, true)
		if err := e.client(t, "r2", runToken).Usage(ctx, usage); err != nil {
			t.Fatalf("Usage: %v", err)
		}
		run, err := e.st.Run(ctx, "r2")
		if err != nil || run.Budget.TotalTokens != 1100 || run.Budget.ContextPct != 80 {
			t.Errorf("budget = %+v, %v; want 1100 tokens at 80%%", run.Budget, err)
		}
	})
	cases := []struct {
		name   string
		run    string
		token  string
		usage  faninmcp.Usage
		status int
		code   string
	}{
		{"AW-14_token_of_an_older_run_is_refused", "r2", oldRunToken, usage, http.StatusUnauthorized,
			wire.CodeUnauthorized},
		{"AW-14_older_run_is_refused", "r1", oldRunToken, usage, http.StatusConflict, wire.CodeConflict},
		{"AW-14_share_over_100_is_refused", "r2", runToken, faninmcp.Usage{ContextPct: 101},
			http.StatusBadRequest, wire.CodeBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReportEnv(t, true)
			wantWorkerError(t, e.client(t, tc.run, tc.token).Usage(ctx, tc.usage), tc.status, tc.code)
			run, err := e.st.Run(ctx, "r2")
			if err != nil || run.Budget != (model.Budget{}) {
				t.Errorf("budget = %+v, %v; want it untouched", run.Budget, err)
			}
		})
	}
}
