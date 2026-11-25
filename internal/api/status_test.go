package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

// configured is the project order of the test config; beta is only in the store.
var configured = []string{"gamma", projAlpha}

func snapshot(t *testing.T, e *env) wire.Snapshot {
	t.Helper()
	resp := e.do(t, http.MethodGet, "/api/v1/snapshot", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var snap wire.Snapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return snap
}

// TestSnapshotProjects covers the project order of status (see SCENARIOS ST-1).
func TestSnapshotProjects(t *testing.T) {
	e := newEnv(t)
	if got := snapshot(t, e).Projects; !cmp.Equal(got, configured) {
		t.Errorf("empty store: projects %v, want the configured %v", got, configured)
	}
	fill(t, e)
	want := []string{"gamma", projAlpha, "beta"}
	if got := snapshot(t, e).Projects; !cmp.Equal(got, want) {
		t.Errorf("projects %v, want config order, then the rest sorted: %v", got, want)
	}
}

// TestSnapshotLockStale covers the merge field of status (see SCENARIOS ST-1).
func TestSnapshotLockStale(t *testing.T) {
	e := newEnv(t)
	fill(t, e)
	stale := func() map[string]bool {
		out := map[string]bool{}
		for _, l := range snapshot(t, e).Locks {
			out[l.Project] = l.Stale
		}
		return out
	}
	if diff := cmp.Diff(map[string]bool{projAlpha: false, "gamma": true}, stale()); diff != "" {
		t.Errorf("a live run holds alpha, gamma's holder r0 is gone (-want +got):\n%s", diff)
	}
	e.clk.Advance(time.Hour)
	if diff := cmp.Diff(map[string]bool{projAlpha: true, "gamma": true}, stale()); diff != "" {
		t.Errorf("after the lease ran out (-want +got):\n%s", diff)
	}
	if err := e.st.AcquireLock(context.Background(),
		store.LockRequest{Project: "gamma", Holder: "t2", Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if got := stale()["gamma"]; got {
		t.Error("gamma held by the open task t2: stale, want live")
	}
}

type statusFailSource struct {
	*store.Store
	locks, latest bool
}

func (s statusFailSource) Locks(ctx context.Context) ([]model.Lock, error) {
	if s.locks {
		return nil, errStore
	}
	return s.Store.Locks(ctx)
}

func (s statusFailSource) LatestEvents(ctx context.Context) ([]model.Event, error) {
	if s.latest {
		return nil, errStore
	}
	return s.Store.LatestEvents(ctx)
}

func TestSnapshotFailsClosedOnUnreadableLocksOrEvents(t *testing.T) {
	for name, src := range map[string]statusFailSource{"locks": {locks: true}, "latest events": {latest: true}} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			src.Store = e.st
			reg := NewRegistry()
			RegisterBuiltins(reg)
			h, err := New(Config{Source: src, OwnerToken: ownerToken, Clock: e.clk, Registry: reg})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 (never an empty snapshot)", rec.Code)
			}
		})
	}
}
