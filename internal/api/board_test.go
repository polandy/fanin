package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

type fakeFleet struct {
	hosts []wire.Host
	err   error
}

func (f fakeFleet) Hosts(context.Context) ([]wire.Host, error) { return f.hosts, f.err }

func newFleetEnv(t *testing.T, fleet Fleet) *env {
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
	h, err := New(Config{Source: st, OwnerToken: ownerToken, Clock: clk, Registry: reg, Projects: configured, Fleet: fleet})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{st: st, clk: clk, srv: srv, reg: reg}
}

// fillBoard builds the ST-2 fixture in alpha: one done task before the batch, five inside it, and one task of
// every other board section.
func fillBoard(t *testing.T, e *env) {
	t.Helper()
	step := func(fn func(ctx context.Context, tx *store.Tx) error) {
		e.tx(t, fn)
		e.clk.Advance(time.Minute)
	}
	create := func(id string, shape model.Shape, after ...model.TaskID) func(context.Context, *store.Tx) error {
		return func(ctx context.Context, tx *store.Tx) error {
			return tx.CreateTask(ctx, model.Task{ID: model.TaskID(id), Project: projAlpha, Title: "title " + id,
				Shape: shape, After: after})
		}
	}
	move := func(id string, states ...model.TaskState) func(context.Context, *store.Tx) error {
		return func(ctx context.Context, tx *store.Tx) error {
			for _, st := range states {
				if err := tx.SetTaskState(ctx, model.TaskID(id), st, ""); err != nil {
					return err
				}
			}
			return nil
		}
	}
	finish := func(id string) {
		step(create(id, model.ShapePR))
		step(move(id, model.TaskRunning, model.TaskGating, model.TaskDone))
	}
	finish("old0")
	e.clk.Advance(time.Hour)
	step(create("anchor", model.ShapePR))
	for _, id := range []string{"d1", "d2", "d3", "d4", "d5"} {
		finish(id)
	}
	step(create("n1", model.ShapePR, "anchor"))
	step(create("w1", model.ShapePR))
	step(move("w1", model.TaskRunning, model.TaskNeedsOwner))
	step(func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AskQuestion(ctx, model.OwnerQuestion{ID: "q1", TaskID: "w1", Question: "ship?",
			Options: []model.Option{{Label: "ship"}, {Label: "wait"}}})
		return err
	})
	step(create("p1", model.ShapePR))
	step(move("p1", model.TaskRunning, model.TaskPaused))
	step(create("dec1", model.ShapeDecision))
	step(func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.SetHold(ctx, model.Hold{ID: "h1", Project: projAlpha, Kind: model.HoldCI, Reason: "main is red",
			FixTask: "d5"})
		return err
	})
}

// TestSnapshot covers the board data and the hosts view (see SCENARIOS ST-2, ST-3).
func TestSnapshot(t *testing.T) {
	t.Run("ST-2_board_sections_match_their_golden_files", func(t *testing.T) {
		e := newFleetEnv(t, nil)
		fillBoard(t, e)
		snap := snapshot(t, e)
		if len(snap.Board) != len(snap.Projects) {
			t.Fatalf("board has %d projects, want one per project %v", len(snap.Board), snap.Projects)
		}
		sections := map[string]func(wire.BoardProject) any{
			"completed": func(b wire.BoardProject) any { return b.Done },
			"next":      func(b wire.BoardProject) any { return b.Next },
			"waiting":   func(b wire.BoardProject) any { return b.Waiting },
			"holds":     func(b wire.BoardProject) any { return b.Holds },
			"decisions": func(b wire.BoardProject) any { return b.Decisions },
		}
		for name, pick := range sections {
			t.Run(name, func(t *testing.T) {
				data := map[string]any{}
				for _, b := range snap.Board {
					data[b.Project] = pick(b)
				}
				got, err := json.MarshalIndent(data, "", "  ")
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				got = append(got, '\n')
				golden := filepath.Join("testdata", "board."+name+".golden.json")
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
					t.Errorf("board %s (-want +got):\n%s", name, diff)
				}
			})
		}
	})
	t.Run("ST-2_board_done_is_empty_without_open_work", func(t *testing.T) {
		e := newFleetEnv(t, nil)
		e.tx(t, func(ctx context.Context, tx *store.Tx) error {
			if err := tx.CreateTask(ctx, model.Task{ID: "x", Project: projAlpha, Shape: model.ShapePR}); err != nil {
				return err
			}
			for _, st := range []model.TaskState{model.TaskRunning, model.TaskGating, model.TaskDone} {
				if err := tx.SetTaskState(ctx, "x", st, ""); err != nil {
					return err
				}
			}
			return nil
		})
		for _, b := range snapshot(t, e).Board {
			if b.Done.Count != 0 || len(b.Done.Newest) != 0 {
				t.Errorf("project %s done = %+v, want empty: no open task starts a batch", b.Project, b.Done)
			}
		}
	})
	t.Run("ST-3_unreachable_host_warns_and_the_other_host_lists_its_workers", func(t *testing.T) {
		fleet := fakeFleet{hosts: []wire.Host{
			{Name: "nuc", Warning: "herdr unreachable: ssh timed out"},
			{Name: "box", Workers: []wire.Worker{
				{Agent: "o1", Project: projAlpha, Kind: wire.WorkerOwner, Status: "idle"},
				{Agent: "w2", Project: projAlpha, Kind: wire.WorkerManaged, Status: string(model.RunWorking)},
				{Agent: "w1", Project: projAlpha, Kind: wire.WorkerManaged, Status: string(model.RunStalled), WaitingOwner: true},
			}},
		}}
		got := snapshot(t, newFleetEnv(t, fleet)).Hosts
		want := []wire.Host{
			{Name: "nuc", Warning: "herdr unreachable: ssh timed out", Workers: []wire.Worker{}},
			{Name: "box", Workers: []wire.Worker{
				{Agent: "w1", Project: projAlpha, Kind: wire.WorkerManaged, Status: string(model.RunStalled), WaitingOwner: true},
				{Agent: "w2", Project: projAlpha, Kind: wire.WorkerManaged, Status: string(model.RunWorking)},
				{Agent: "o1", Project: projAlpha, Kind: wire.WorkerOwner, Status: "idle"},
			}},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("hosts (-want +got):\n%s", diff)
		}
	})
	t.Run("ST-3_without_a_fleet_the_hosts_are_empty", func(t *testing.T) {
		if got := snapshot(t, newFleetEnv(t, nil)).Hosts; got == nil || len(got) != 0 {
			t.Errorf("hosts = %#v, want an empty list", got)
		}
	})
	t.Run("ST-3_an_unreadable_fleet_fails_the_snapshot", func(t *testing.T) {
		e := newFleetEnv(t, fakeFleet{err: errors.New("fleet unreadable")})
		resp := e.do(t, http.MethodGet, "/api/v1/snapshot", "", nil)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500: an unreadable fleet is never an empty one", resp.StatusCode)
		}
	})
}
