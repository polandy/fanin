package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/model"
)

var overviewT0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// overviewSnapshot is the ST-2 fixture: alpha has a batch of five done tasks, queued work, an owner question,
// both holds and a decision; gamma has nothing open.
func overviewSnapshot() wire.Snapshot {
	task := func(id, title, state string) wire.BoardTask {
		return wire.BoardTask{ID: id, Title: title, State: state, Shape: "pr", UpdatedAt: overviewT0}
	}
	blocked := task("a7", "after the next", string(model.TaskQueued))
	blocked.Blocked = true
	asking := task("a8", "pick a model", string(model.TaskNeedsOwner))
	asking.QuestionID = "q1"
	return wire.Snapshot{
		Projects: []string{projAlpha, projGamma},
		Board: []wire.BoardProject{
			{
				Project: projAlpha,
				Done: wire.BoardDone{Count: 5, Newest: []wire.BoardTask{
					task("a5", "fifth", string(model.TaskDone)), task("a4", "fourth", string(model.TaskDone)), task("a3", "third", string(model.TaskDone)),
					task("a2", "second", string(model.TaskDone)),
				}},
				Next:    []wire.BoardTask{task("a6", "the next one", string(model.TaskQueued)), blocked},
				Waiting: []wire.BoardTask{asking, task("a9", "paused for a while", string(model.TaskPaused))},
				Ready: []wire.BoardReady{{ID: "a12", Title: "ship the report", PR: 17,
					Verdict: "GREEN ship the report; changes code", UpdatedAt: overviewT0}},
				Holds: []wire.Hold{
					{ID: "h1", Project: projAlpha, Kind: "ci", Reason: "main is red", FixTask: "a10"},
					{ID: "h2", Project: projAlpha, Kind: "merge", Reason: "hook failed"},
				},
				Decisions: []wire.BoardTask{task("a11", "choose the store layout", string(model.TaskQueued))},
			},
			{Project: projGamma},
		},
	}
}

func serveSnapshot(t *testing.T, snap wire.Snapshot) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/snapshot" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(snap); err != nil {
			t.Errorf("encode snapshot: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestOverview(t *testing.T) {
	t.Run("ST-2_renders_every_section_from_the_snapshot", func(t *testing.T) {
		url := serveSnapshot(t, overviewSnapshot())
		code, out, errOut := runArgs(t, defaultRegistry, map[string]string{envURL: url}, "overview")
		if code != 0 || errOut != "" {
			t.Fatalf("overview = %d, stderr %q, want 0 and none", code, errOut)
		}
		golden := filepath.Join("testdata", "overview.golden")
		if *update {
			if err := os.WriteFile(golden, []byte(out), 0o600); err != nil {
				t.Fatalf("write golden: %v", err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("read golden (run go test ./cmd/fanin -update): %v", err)
		}
		if diff := cmp.Diff(string(want), out); diff != "" {
			t.Errorf("overview (-want +got):\n%s", diff)
		}
	})

	t.Run("ST-2_one_project_by_name_and_an_unknown_one_is_an_error", func(t *testing.T) {
		url := serveSnapshot(t, overviewSnapshot())
		code, out, _ := runArgs(t, defaultRegistry, map[string]string{envURL: url}, "overview", projGamma)
		if code != 0 || strings.Contains(out, projAlpha) || !strings.Contains(out, projGamma) {
			t.Errorf("overview gamma = %d %q, want only gamma", code, out)
		}
		code, _, errOut := runArgs(t, defaultRegistry, map[string]string{envURL: url}, "overview", "nope")
		if code != 1 || !strings.Contains(errOut, `"nope"`) || !strings.Contains(errOut, "alpha, gamma") {
			t.Errorf("overview nope = %d %q, want exit 1 naming the project and the known ones", code, errOut)
		}
	})

	t.Run("ST-2_unreachable_daemon_is_an_error_not_an_empty_board", func(t *testing.T) {
		code, out, errOut := runArgs(t, defaultRegistry, map[string]string{envURL: "http://127.0.0.1:1"}, "overview")
		if code != 1 || out != "" || !strings.Contains(errOut, "overview:") {
			t.Errorf("overview = %d %q %q, want exit 1, no output and the cause", code, out, errOut)
		}
	})

	t.Run("ST-2_without_the_daemon_url_is_a_usage_error", func(t *testing.T) {
		code, _, errOut := runArgs(t, defaultRegistry, map[string]string{}, "overview")
		if code != 2 || !strings.Contains(errOut, envURL) {
			t.Errorf("overview = %d %q, want usage error naming %s", code, errOut, envURL)
		}
	})
}
