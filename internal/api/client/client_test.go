package client_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/api"
	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

const token = "owner-secret"

func start(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	clk := clock.NewManual(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	reg := api.NewRegistry()
	api.RegisterBuiltins(reg)
	reg.Add(api.Route{Pattern: "POST /api/v1/test/echo", Handler: func(*api.Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			api.WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
		})
	}})
	h, err := api.New(api.Config{Source: st, OwnerToken: token, Clock: clk, Registry: reg})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return st, srv
}

func TestClientHealthAndSnapshot(t *testing.T) {
	st, srv := start(t)
	ctx := context.Background()
	err := st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		return tx.CreateTask(ctx, model.Task{ID: "t1", Project: "alpha", Title: "first", Shape: model.ShapePR})
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	c := client.New(srv.URL, token, srv.Client())
	h, err := c.Health(ctx)
	if err != nil || h.Status != wire.StatusOK {
		t.Fatalf("Health = %+v, %v; want ok", h, err)
	}
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Tasks) != 1 || snap.Tasks[0].ID != "t1" {
		t.Errorf("Snapshot tasks = %+v, want t1", snap.Tasks)
	}
}

func TestClientPostSendsTheToken(t *testing.T) {
	_, srv := start(t)
	var out map[string]string
	if err := client.New(srv.URL, token, srv.Client()).Post(context.Background(), "/api/v1/test/echo", nil, &out); err != nil {
		t.Fatalf("Post with the token: %v", err)
	}
	err := client.New(srv.URL, "wrong", srv.Client()).Post(context.Background(), "/api/v1/test/echo", nil, &out)
	var apiErr *client.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Code != wire.CodeUnauthorized {
		t.Errorf("Post with a wrong token = %v, want *client.Error 401 %s", err, wire.CodeUnauthorized)
	}
}

func TestClientEventsStreamsStoreWrites(t *testing.T) {
	st, srv := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan wire.Event)
	ready := make(chan struct{})
	errc := make(chan error, 1)
	c := client.New(srv.URL, token, srv.Client())
	safego.Go(ctx, slog.Default(), func(ctx context.Context) {
		errc <- c.Events(ctx, 0, func() { close(ready) }, func(ev wire.Event) error {
			got <- ev
			return nil
		})
	})
	<-ready
	err := st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		return tx.CreateTask(ctx, model.Task{ID: "t1", Project: "alpha", Title: "first", Shape: model.ShapePR})
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if ev := <-got; ev.TaskID != "t1" || ev.Type != string(model.EventTaskCreated) {
		t.Errorf("event = %+v, want t1 task_created", ev)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("Events after cancel = %v, want context.Canceled", err)
	}
}
