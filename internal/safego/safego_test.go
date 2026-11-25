package safego_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/polandy/fanin/internal/safego"
)

type recHandler struct {
	mu   sync.Mutex
	recs []slog.Record
	ch   chan struct{}
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.recs = append(h.recs, r)
	h.mu.Unlock()
	h.ch <- struct{}{}
	return nil
}
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recHandler) WithGroup(string) slog.Handler      { return h }

func TestGoLogsPanicAndProcessSurvives(t *testing.T) {
	h := &recHandler{ch: make(chan struct{}, 1)}
	safego.Go(context.Background(), slog.New(h), func(context.Context) { panic("boom") })
	<-h.ch

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(h.recs))
	}
	r := h.recs[0]
	if r.Level != slog.LevelError || !strings.Contains(r.Message, "panic") {
		t.Errorf("record = %v %q, want error mentioning panic", r.Level, r.Message)
	}
	var panicVal, stack string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "panic":
			panicVal = a.Value.String()
		case "stack":
			stack = a.Value.String()
		}
		return true
	})
	if panicVal != "boom" || stack == "" {
		t.Errorf("attrs panic=%q stack empty=%v", panicVal, stack == "")
	}
}

func TestGoRunsFnWithContext(t *testing.T) {
	type key struct{}
	done := make(chan any, 1)
	ctx := context.WithValue(context.Background(), key{}, "v")
	safego.Go(ctx, slog.Default(), func(ctx context.Context) { done <- ctx.Value(key{}) })
	if got := <-done; got != "v" {
		t.Errorf("ctx value = %v, want v", got)
	}
}
