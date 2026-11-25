package metrics_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/metrics"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

const (
	projAlpha = "alpha"
	ctPlain   = "text/plain; version=0.0.4; charset=utf-8"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"),
		clock.NewManual(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return st
}

func scrape(t *testing.T, src metrics.Source) (int, http.Header, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(src, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Code, rec.Header(), rec.Body.String()
}

func transition(t *testing.T, st *store.Store, fn func(ctx context.Context, tx *store.Tx) error) {
	t.Helper()
	if err := st.Transition(context.Background(), fn); err != nil {
		t.Fatalf("Transition: %v", err)
	}
}

func hasLine(body, line string) bool {
	for _, l := range strings.Split(body, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

// TestMetrics covers the Prometheus endpoint (docs/dev/concept.md, Observability).
func TestMetrics(t *testing.T) {
	t.Run("OB-1_endpoint_serves_the_text_format_with_zero_counters", func(t *testing.T) {
		code, hdr, body := scrape(t, openStore(t))
		if code != http.StatusOK || hdr.Get("Content-Type") != ctPlain {
			t.Fatalf("GET /metrics = %d %q, want 200 %q", code, hdr.Get("Content-Type"), ctPlain)
		}
		for _, want := range []string{
			"# TYPE fanin_tasks gauge",
			"# TYPE fanin_runs_started_total counter",
			"# TYPE fanin_nudges_total counter",
			"# TYPE fanin_merges_total counter",
			"# TYPE fanin_adapter_errors_total counter",
			`fanin_nudges_total{project=""} 0`,
			`fanin_adapter_errors_total{adapter="hook",project=""} 0`,
		} {
			if !hasLine(body, want) {
				t.Errorf("metrics lack %q:\n%s", want, body)
			}
		}
	})

	t.Run("OB-2_store_transitions_raise_their_counters", func(t *testing.T) {
		st := openStore(t)
		transition(t, st, func(ctx context.Context, tx *store.Tx) error {
			if err := tx.CreateTask(ctx, model.Task{ID: "a1", Project: projAlpha, Title: "t", Shape: model.ShapePR}); err != nil {
				return err
			}
			return tx.CreateTask(ctx, model.Task{ID: "a2", Project: projAlpha, Title: "t", Shape: model.ShapePR})
		})
		_, _, before := scrape(t, st)
		if !hasLine(before, `fanin_tasks{project="alpha",state="queued"} 2`) ||
			!hasLine(before, `fanin_tasks{project="alpha",state="running"} 0`) {
			t.Fatalf("queued tasks not counted:\n%s", before)
		}
		transition(t, st, func(ctx context.Context, tx *store.Tx) error {
			if err := tx.SetTaskState(ctx, "a1", model.TaskRunning, "dispatched"); err != nil {
				return err
			}
			if err := tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "a1", Number: 1, Branch: "feat/x",
				State: model.RunStarting}); err != nil {
				return err
			}
			for _, typ := range []model.EventType{model.EventRunStarted, model.EventWorkerNudged, model.EventWorkerNudged,
				model.EventMerged, model.EventHookFailed, model.EventNotifyFailed} {
				if _, err := tx.AppendEvent(ctx, model.Event{Type: typ, Project: projAlpha, TaskID: "a1", RunID: "r1"}); err != nil {
					return err
				}
			}
			return nil
		})
		_, _, body := scrape(t, st)
		for _, want := range []string{
			`fanin_tasks{project="alpha",state="queued"} 1`,
			`fanin_tasks{project="alpha",state="running"} 1`,
			`fanin_runs{project="alpha",state="starting"} 1`,
			`fanin_runs_started_total{project="alpha"} 1`,
			`fanin_nudges_total{project="alpha"} 2`,
			`fanin_merges_total{project="alpha"} 1`,
			`fanin_adapter_errors_total{adapter="hook",project="alpha"} 1`,
			`fanin_adapter_errors_total{adapter="notify",project="alpha"} 1`,
		} {
			if !hasLine(body, want) {
				t.Errorf("metrics lack %q:\n%s", want, body)
			}
		}
	})

	t.Run("OB-1_unreadable_store_is_a_500_never_partial_numbers", func(t *testing.T) {
		code, _, body := scrape(t, brokenSource{})
		if code != http.StatusInternalServerError {
			t.Fatalf("GET /metrics = %d, want 500", code)
		}
		if strings.Contains(body, "fanin_tasks") || !strings.Contains(body, errBroken.Error()) {
			t.Errorf("body = %q, want the error and no metrics", body)
		}
	})
}

var errBroken = errors.New("disk gone")

type brokenSource struct{}

func (brokenSource) TaskCounts(context.Context) ([]store.Count, error)  { return nil, errBroken }
func (brokenSource) RunCounts(context.Context) ([]store.Count, error)   { return nil, errBroken }
func (brokenSource) EventCounts(context.Context) ([]store.Count, error) { return nil, errBroken }
