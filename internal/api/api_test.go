package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
	"github.com/polandy/fanin/internal/store"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	ownerToken = "owner-secret"
	projAlpha  = "alpha"
	projBeta   = "beta"
	projGamma  = "gamma"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

type env struct {
	st  *store.Store
	clk *clock.Manual
	srv *httptest.Server
	reg *Registry
}

func newEnv(t *testing.T, extra ...func(*Registry)) *env {
	t.Helper()
	clk := clock.NewManual(t0)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	reg := NewRegistry()
	RegisterBuiltins(reg)
	for _, f := range extra {
		f(reg)
	}
	h, err := New(Config{Source: st, OwnerToken: ownerToken, Clock: clk, Registry: reg, Projects: configured})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{st: st, clk: clk, srv: srv, reg: reg}
}

func (e *env) tx(t *testing.T, fn func(ctx context.Context, tx *store.Tx) error) {
	t.Helper()
	if err := e.st.Transition(context.Background(), fn); err != nil {
		t.Fatalf("Transition: %v", err)
	}
}

// reply is a finished answer: the body is read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (e *env) do(t *testing.T, method, path, token string, hdr map[string]string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}
}

func decodeError(t *testing.T, resp reply) wire.Error {
	t.Helper()
	var body struct {
		Error wire.Error `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	resp := e.do(t, http.MethodGet, "/api/v1/health", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var h wire.Health
	if err := json.Unmarshal(resp.Body, &h); err != nil || h.Status != wire.StatusOK {
		t.Errorf("health = %+v, %v; want status %q", h, err, wire.StatusOK)
	}
}

func TestUnknownRouteIsJSONNotFound(t *testing.T) {
	e := newEnv(t)
	resp := e.do(t, http.MethodGet, "/api/v1/nope", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if got := decodeError(t, resp); got.Code != wire.CodeNotFound || got.Message == "" {
		t.Errorf("error = %+v, want code %q and a message", got, wire.CodeNotFound)
	}
}

// echoRoute lives in its own file's register function, as a strand's endpoints do.
func registerCalls(calls *int) func(*Registry) {
	return func(r *Registry) {
		r.Add(Route{Pattern: "POST /api/v1/test/echo", Handler: func(*Server) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				*calls++
				WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
			})
		}})
		r.Add(Route{Pattern: "GET /api/v1/test/open", Handler: func(*Server) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
			})
		}})
	}
}

func TestPostNeedsOwnerToken(t *testing.T) {
	var calls int
	e := newEnv(t, registerCalls(&calls))
	tests := []struct {
		name, token string
		want        int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "guess", http.StatusUnauthorized},
		{"owner token", ownerToken, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := e.do(t, http.MethodPost, "/api/v1/test/echo", tc.token, nil)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				if got := decodeError(t, resp); got.Code != wire.CodeUnauthorized {
					t.Errorf("code = %q, want %q", got.Code, wire.CodeUnauthorized)
				}
			}
		})
	}
	if calls != 1 {
		t.Errorf("handler ran %d times, want once (only the owner-token call)", calls)
	}
}

func TestGetNeedsNoToken(t *testing.T) {
	e := newEnv(t, registerCalls(new(int)))
	if resp := e.do(t, http.MethodGet, "/api/v1/test/open", "", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestWebhookDisguisedCommandCallIsRefused(t *testing.T) {
	var calls int
	e := newEnv(t, registerCalls(&calls))
	hdr := map[string]string{
		"X-GitHub-Event":      "issues",
		"X-GitHub-Delivery":   "0000",
		"X-Hub-Signature-256": "sha256=deadbeef",
		"Content-Type":        "application/json",
	}
	for _, token := range []string{"", "sha256=deadbeef"} {
		resp := e.do(t, http.MethodPost, "/api/v1/test/echo", token, hdr)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("webhook-shaped POST (token %q) status = %d, want 401", token, resp.StatusCode)
		}
	}
	// With the owner token the webhook headers are still no way in: a signed delivery is not the owner.
	if resp := e.do(t, http.MethodPost, "/api/v1/test/echo", ownerToken, hdr); resp.StatusCode != http.StatusForbidden {
		t.Errorf("owner token with webhook headers status = %d, want 403", resp.StatusCode)
	}
	if calls != 0 {
		t.Errorf("command handler ran %d times for webhook-shaped calls, want 0", calls)
	}
}

func TestRouteFromOwnFileIsServed(t *testing.T) {
	e := newEnv(t, registerCalls(new(int)))
	got := e.reg.Patterns()
	for _, want := range []string{"GET /api/v1/test/open", "POST /api/v1/test/echo", "GET /api/v1/health"} {
		if !slices.Contains(got, want) {
			t.Errorf("pattern %q is not registered; have %v", want, got)
		}
	}
	if resp := e.do(t, http.MethodGet, "/api/v1/test/open", "", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/test/open status = %d, want 200", resp.StatusCode)
	}
}

func TestNewRefusesAnEmptyToken(t *testing.T) {
	_, err := New(Config{Source: nil, OwnerToken: "", Clock: clock.NewManual(t0), Registry: NewRegistry()})
	if err == nil {
		t.Fatal("New with an empty owner token: want an error, got nil")
	}
}

func fill(t *testing.T, e *env) {
	t.Helper()
	e.tx(t, func(ctx context.Context, tx *store.Tx) error {
		for _, task := range []model.Task{
			{ID: "t1", Project: projAlpha, Title: "first", Shape: model.ShapePR, After: []model.TaskID{"t0"}},
			{ID: "t2", Project: projBeta, Title: "second", Shape: model.ShapePR},
		} {
			if err := tx.CreateTask(ctx, task); err != nil {
				return err
			}
		}
		if err := tx.SetTaskState(ctx, "t1", model.TaskRunning, "dispatched"); err != nil {
			return err
		}
		if err := tx.CreateRun(ctx, model.Run{ID: "r1", TaskID: "t1", Number: 1, Branch: "feat/x", Token: "run-secret",
			State: model.RunStarting}); err != nil {
			return err
		}
		if _, err := tx.AskQuestion(ctx, model.OwnerQuestion{ID: "q1", TaskID: "t2", Project: projBeta,
			Question: "beta: which?", Options: []model.Option{{Label: "a"}, {Label: "b"}}, Recommended: 1}); err != nil {
			return err
		}
		_, err := tx.SetHold(ctx, model.Hold{ID: "h1", Project: projGamma, Kind: model.HoldCI, Reason: "main is red",
			FixTask: "t9"})
		return err
	})
	for _, l := range []store.LockRequest{
		{Project: projAlpha, Holder: "r1", Lease: time.Hour},
		{Project: projGamma, Holder: "r0", Lease: time.Hour},
	} {
		if err := e.st.AcquireLock(context.Background(), l); err != nil {
			t.Fatalf("AcquireLock: %v", err)
		}
	}
}

// TestSnapshotGolden covers the snapshot view of status (see SCENARIOS ST-1).
func TestSnapshotGolden(t *testing.T) {
	e := newEnv(t)
	fill(t, e)
	resp := e.do(t, http.MethodGet, "/api/v1/snapshot", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var snap wire.Snapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	if strings.Contains(string(got), "run-secret") {
		t.Fatal("snapshot leaks a run token")
	}
	golden := filepath.Join("testdata", "snapshot.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v; run go test ./internal/api -update", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("snapshot (-want +got):\n%s", diff)
	}
}

type sseEvent struct{ id, event, data string }

// readEvents parses the stream and sends each event; it closes the channel when the stream ends.
func readEvents(body *bufio.Scanner, out chan<- sseEvent) {
	defer close(out)
	var cur sseEvent
	for body.Scan() {
		line := body.Text()
		switch {
		case line == "":
			if cur.event != "" {
				out <- cur
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func openStream(t *testing.T, e *env, path string) <-chan sseEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	out := make(chan sseEvent)
	safego.Go(ctx, slog.Default(), func(context.Context) { readEvents(bufio.NewScanner(resp.Body), out) })
	return out
}

func TestSSEDeliversAnEventAfterAStoreWrite(t *testing.T) {
	e := newEnv(t)
	events := openStream(t, e, "/api/v1/events")
	// The handler has taken its change channel by the time the response headers arrive, so this write is seen.
	e.tx(t, func(ctx context.Context, tx *store.Tx) error {
		return tx.CreateTask(ctx, model.Task{ID: "t1", Project: projAlpha, Title: "first", Shape: model.ShapePR})
	})
	ev := <-events
	if ev.event != string(model.EventTaskCreated) || ev.id == "" {
		t.Fatalf("event = %+v, want a task_created with an id", ev)
	}
	var got wire.Event
	if err := json.Unmarshal([]byte(ev.data), &got); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if got.TaskID != "t1" || got.Project != projAlpha || got.Type != string(model.EventTaskCreated) {
		t.Errorf("data = %+v, want the t1 task_created of alpha", got)
	}
}

func TestSSEResumesAfterTheGivenSeq(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"t1", "t2"} {
		e.tx(t, func(ctx context.Context, tx *store.Tx) error {
			return tx.CreateTask(ctx, model.Task{ID: model.TaskID(id), Project: projAlpha, Title: id, Shape: model.ShapePR})
		})
	}
	evs, err := e.st.EventsAfter(context.Background(), 0, 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("EventsAfter = %v, %v; want two events", evs, err)
	}
	events := openStream(t, e, "/api/v1/events?after="+itoa(evs[0].Seq))
	if ev := <-events; ev.id != itoa(evs[1].Seq) {
		t.Errorf("first event id = %q, want %d (the one after the cursor)", ev.id, evs[1].Seq)
	}
}

func TestSSERefusesABadCursor(t *testing.T) {
	e := newEnv(t)
	resp := e.do(t, http.MethodGet, "/api/v1/events?after=abc", "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSSEKeepAliveFollowsTheClock(t *testing.T) {
	e := newEnv(t)
	body := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	safego.Go(ctx, slog.Default(), func(context.Context) {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), ":") {
				body <- sc.Text()
				return
			}
		}
	})
	e.clk.Advance(KeepAliveEvery)
	if got := <-body; got != ": keepalive" {
		t.Errorf("comment = %q, want %q", got, ": keepalive")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type failingSource struct{ Source }

var errStore = errors.New("store unreadable")

func (failingSource) OpenTasks(context.Context) ([]model.Task, error) { return nil, errStore }

func (failingSource) LastEventSeq(context.Context) (int64, error) { return 0, nil }

func TestSnapshotFailsClosedOnAnUnreadableStore(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg)
	h, err := New(Config{Source: failingSource{}, OwnerToken: ownerToken, Clock: clock.NewManual(t0), Registry: reg})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (never an empty snapshot)", rec.Code)
	}
	var body wire.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != wire.CodeInternal {
		t.Errorf("body = %s, %v; want code %q", rec.Body.String(), err, wire.CodeInternal)
	}
}
