package gitea_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
)

func TestPR(t *testing.T) {
	t.Run("reads_head_mergeable_and_statuses_with_the_token", func(t *testing.T) {
		f := newFake(t)
		f.on(pullPath, reply(http.StatusOK, prDoc()))
		f.on(statusPath, reply(http.StatusOK, combined(status("ci", "success"), status(review, "pending"))))
		got, err := f.client(t).PR(context.Background(), repo, number)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 11, 25, 9, 5, 0, 0, time.UTC)
		want := forge.PR{
			Repo: repo, Number: number, Title: title, URL: "https://git.example.com/acme/widget/pulls/42",
			State: forge.StateOpen, Mergeable: forge.MergeableYes, HeadBranch: headBranch, HeadSHA: headSHA,
			BaseBranch: baseBranch,
			Checks: []forge.Check{
				{Name: "ci", State: forge.CheckSuccess, UpdatedAt: at},
				{Name: review, State: forge.CheckPending, UpdatedAt: at},
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("PR (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{"token " + token, "token " + token}, f.auth); diff != "" {
			t.Errorf("Authorization (-want +got):\n%s", diff)
		}
	})

	for _, tc := range []struct {
		name      string
		state     string
		merged    bool
		mergeable bool
		want      forge.PRState
		wantM     forge.Mergeable
	}{
		{"open_conflicting", "open", false, false, forge.StateOpen, forge.MergeableConflicted},
		{"merged", "closed", true, false, forge.StateMerged, forge.MergeableConflicted},
		{"closed", "closed", false, true, forge.StateClosed, forge.MergeableYes},
	} {
		t.Run("state_"+tc.name, func(t *testing.T) {
			d := prWith(keyState, tc.state)
			d[keyMerged], d["mergeable"] = tc.merged, tc.mergeable
			f := newFake(t)
			f.on(pullPath, reply(http.StatusOK, d))
			f.on(statusPath, reply(http.StatusOK, combined()))
			got, err := f.client(t).PR(context.Background(), repo, number)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.want || got.Mergeable != tc.wantM || got.Checks == nil {
				t.Errorf("state %s mergeable %s checks %v; want %s %s and empty checks", got.State, got.Mergeable,
					got.Checks, tc.want, tc.wantM)
			}
		})
	}

	t.Run("status_states_are_normalised", func(t *testing.T) {
		f := newFake(t)
		f.on(pullPath, reply(http.StatusOK, prDoc()))
		f.on(statusPath, reply(http.StatusOK, combined(status("a", "running"), status("b", "warning"),
			status("c", "error"), status("d", "skipped"), status("e", "failure"))))
		got, err := f.client(t).PR(context.Background(), repo, number)
		if err != nil {
			t.Fatal(err)
		}
		var states []forge.CheckState
		for _, ch := range got.Checks {
			states = append(states, ch.State)
		}
		want := []forge.CheckState{forge.CheckPending, forge.CheckFailure, forge.CheckFailure, forge.CheckSkipped, forge.CheckFailure}
		if diff := cmp.Diff(want, states); diff != "" {
			t.Errorf("states (-want +got):\n%s", diff)
		}
	})

	t.Run("statuses_are_read_page_by_page", func(t *testing.T) {
		var first []map[string]any
		for i := range 50 {
			first = append(first, status(fmt.Sprintf("c%d", i), "success"))
		}
		f := newFake(t)
		f.on(pullPath, reply(http.StatusOK, prDoc()))
		f.on(statusPath, func(w http.ResponseWriter, r *http.Request) {
			page := map[string]any{"total_count": 51, "statuses": first}
			if r.URL.Query().Get("page") == "2" {
				page["statuses"] = []map[string]any{status("last", "failure")}
			}
			reply(http.StatusOK, page)(w, r)
		})
		got, err := f.client(t).PR(context.Background(), repo, number)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Checks) != 51 || got.Checks[50].State != forge.CheckFailure {
			t.Errorf("got %d checks, want 51 ending in the failure of page 2", len(got.Checks))
		}
	})
}

func TestPRUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"server_error", raw(http.StatusInternalServerError, "boom")},
		{"broken_json", raw(http.StatusOK, `{"number":`)},
		{"trailing_data", raw(http.StatusOK, `{"number":42} {}`)},
		{"other_number", reply(http.StatusOK, prWith("number", 7))},
		{"unknown_state", reply(http.StatusOK, prWith(keyState, "draft"))},
		{"no_head_branch", reply(http.StatusOK, prWith("head", map[string]any{"sha": headSHA}))},
		{"no_base", reply(http.StatusOK, prWith("base", map[string]any{}))},
		{"no_mergeable", reply(http.StatusOK, prWith("mergeable", nil))},
	} {
		t.Run("GT-6_pr_unreadable_is_an_error_"+tc.name, func(t *testing.T) {
			f := newFake(t)
			f.on(pullPath, tc.h)
			got, err := f.client(t).PR(context.Background(), repo, number)
			if err == nil || errors.Is(err, forge.ErrChecksUnreadable) {
				t.Fatalf("PR = %+v, %v; want an error that is not ErrChecksUnreadable", got, err)
			}
			if diff := cmp.Diff([]string{pullPath}, f.callList()); diff != "" {
				t.Errorf("calls (-want +got):\n%s", diff)
			}
		})
	}

	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"server_error", raw(http.StatusBadGateway, "bad gateway")},
		{"broken_json", raw(http.StatusOK, `{"statuses":[`)},
		{"unknown_state", reply(http.StatusOK, combined(status("ci", "green")))},
		{"no_context", reply(http.StatusOK, combined(status("", "success")))},
		{"bad_time", reply(http.StatusOK, combined(map[string]any{"context": "ci", keyStatus: "success", "updated_at": "soon"}))},
	} {
		t.Run("GT-6_status_unreadable_is_checks_unreadable_never_green_"+tc.name, func(t *testing.T) {
			f := newFake(t)
			f.on(pullPath, reply(http.StatusOK, prDoc()))
			f.on(statusPath, tc.h)
			got, err := f.client(t).PR(context.Background(), repo, number)
			if !errors.Is(err, forge.ErrChecksUnreadable) {
				t.Fatalf("err = %v, want ErrChecksUnreadable", err)
			}
			if got.Checks != nil || got.State != "" {
				t.Errorf("PR = %+v, want the zero PR", got)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		repo   string
		number int
	}{{"zero", repo, 0}, {"negative", repo, -1}, {"repo_without_owner", "widget", number}, {"nested_repo", "a/b/c", number}} {
		t.Run("GT-6_bad_number_or_repo_fails_before_any_call_"+tc.name, func(t *testing.T) {
			f := newFake(t)
			c := f.client(t)
			if _, err := c.PR(context.Background(), tc.repo, tc.number); !errors.Is(err, forge.ErrInvalidRequest) {
				t.Errorf("PR err = %v, want ErrInvalidRequest", err)
			}
			if _, err := c.Files(context.Background(), tc.repo, tc.number); !errors.Is(err, forge.ErrInvalidRequest) {
				t.Errorf("Files err = %v, want ErrInvalidRequest", err)
			}
			if calls := f.callList(); len(calls) != 0 {
				t.Errorf("calls %v, want none", calls)
			}
		})
	}
}

func TestFiles(t *testing.T) {
	t.Run("reads_every_page", func(t *testing.T) {
		var first []map[string]any
		for i := range 50 {
			first = append(first, map[string]any{"filename": fmt.Sprintf("docs/f%d.md", i)})
		}
		f := newFake(t)
		f.on(filesPath, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "2" {
				reply(http.StatusOK, []map[string]any{{"filename": "main.go"}})(w, r)
				return
			}
			reply(http.StatusOK, first)(w, r)
		})
		got, err := f.client(t).Files(context.Background(), repo, number)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 51 || got[50] != "main.go" {
			t.Errorf("got %d files ending %q, want 51 ending main.go", len(got), got[len(got)-1])
		}
	})

	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		want error
	}{
		{"server_unavailable", raw(http.StatusServiceUnavailable, "down"), forge.ErrNetwork},
		{"broken_json", raw(http.StatusOK, `[{"filename":`), forge.ErrUnreadable},
		{"empty_filename", reply(http.StatusOK, []map[string]any{{"filename": ""}}), forge.ErrUnreadable},
	} {
		t.Run("GT-6_files_unreadable_is_an_error_"+tc.name, func(t *testing.T) {
			f := newFake(t)
			f.on(filesPath, tc.h)
			got, err := f.client(t).Files(context.Background(), repo, number)
			if !errors.Is(err, tc.want) || got != nil {
				t.Fatalf("Files = %v, %v; want %v", got, err, tc.want)
			}
		})
	}

	t.Run("endless_pages_are_an_error_not_a_truncated_list", func(t *testing.T) {
		var page []map[string]any
		for i := range 50 {
			page = append(page, map[string]any{"filename": fmt.Sprintf("f%d", i)})
		}
		f := newFake(t)
		f.on(filesPath, reply(http.StatusOK, page))
		if _, err := f.client(t).Files(context.Background(), repo, number); !errors.Is(err, forge.ErrUnreadable) {
			t.Fatalf("err = %v, want ErrUnreadable", err)
		}
	})
}

func TestListOpen(t *testing.T) {
	t.Run("lists_open_prs_without_statuses", func(t *testing.T) {
		f := newFake(t)
		f.on(listPath, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get(keyState) != "open" {
				t.Errorf("state query %q, want open", r.URL.Query().Get(keyState))
			}
			reply(http.StatusOK, []map[string]any{prDoc()})(w, r)
		})
		got, err := f.client(t).ListOpen(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].HeadBranch != headBranch || got[0].Checks != nil {
			t.Errorf("ListOpen = %+v, want PR 42 without checks", got)
		}
	})

	t.Run("an_unreadable_entry_fails_the_list", func(t *testing.T) {
		f := newFake(t)
		f.on(listPath, reply(http.StatusOK, []map[string]any{prDoc(), prWith("head", map[string]any{})}))
		if _, err := f.client(t).ListOpen(context.Background(), repo); !errors.Is(err, forge.ErrUnreadable) {
			t.Fatalf("err = %v, want ErrUnreadable", err)
		}
	})
}

func TestTransportErrors(t *testing.T) {
	t.Run("not_found_is_err_not_found", func(t *testing.T) {
		f := newFake(t)
		f.on(pullPath, raw(http.StatusNotFound, `{"message":"not found"}`))
		if _, err := f.client(t).PR(context.Background(), repo, number); !errors.Is(err, forge.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("closed_server_is_err_network", func(t *testing.T) {
		f := newFake(t)
		c := f.client(t)
		f.srv.Close()
		if _, err := c.PR(context.Background(), repo, number); !errors.Is(err, forge.ErrNetwork) {
			t.Fatalf("err = %v, want ErrNetwork", err)
		}
	})

	t.Run("cancelled_context_is_the_context_error", func(t *testing.T) {
		f := newFake(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := f.client(t).PR(ctx, repo, number); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}
