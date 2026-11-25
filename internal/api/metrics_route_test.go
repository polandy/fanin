package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsRoute(t *testing.T) {
	serve := func(t *testing.T, metrics http.Handler) *httptest.ResponseRecorder {
		t.Helper()
		reg := NewRegistry()
		RegisterBuiltins(reg)
		h, err := New(Config{Source: nil, OwnerToken: "tok", Registry: reg, Metrics: metrics})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec
	}

	t.Run("OB-1_metrics_route_serves_the_configured_handler_without_a_token", func(t *testing.T) {
		rec := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("fanin_tasks 1\n"))
		}))
		if rec.Code != http.StatusOK || rec.Body.String() != "fanin_tasks 1\n" {
			t.Errorf("GET /metrics = %d %q, want 200 and the handler's body", rec.Code, rec.Body.String())
		}
	})

	t.Run("OB-1_metrics_route_without_a_source_is_a_500", func(t *testing.T) {
		rec := serve(t, nil)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "no metrics source") {
			t.Errorf("GET /metrics = %d %q, want 500 naming the missing source", rec.Code, rec.Body.String())
		}
	})
}
