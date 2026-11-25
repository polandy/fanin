package gitea_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/gitea"
)

const comparePath = "GET /api/v1/repos/acme/widget/compare/main...feat/widget"

func commit(sha, message string) map[string]any {
	return map[string]any{"sha": sha, "commit": map[string]any{"message": message}}
}

func squash(t *testing.T) forge.MergeRequest {
	t.Helper()
	req, err := forge.NewSquash(forge.PR{Repo: repo, Number: number, Title: title, HeadBranch: headBranch, HeadSHA: headSHA},
		[]forge.Trailer{{Key: "Task-Id", Value: "t-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestOpenBranch(t *testing.T) {
	t.Run("GT-1_opens_titled_by_the_oldest_commit_listing_every_commit", func(t *testing.T) {
		f := newFake(t)
		f.on(listPath, reply(http.StatusOK, []map[string]any{prWith("head", map[string]any{"ref": "other", "sha": "1"})}))
		f.on(comparePath, reply(http.StatusOK, map[string]any{"total_commits": 2, "commits": []map[string]any{
			commit("222", "fix: handle the edge\n\nThe edge case.\n"),
			commit("111", "feat: add the widget\n"),
		}}))
		f.on(openPath, reply(http.StatusCreated, prWith("number", 43)))
		n, reused, err := f.client(t).OpenBranch(context.Background(), repo, baseBranch, headBranch)
		if err != nil || n != 43 || reused {
			t.Fatalf("OpenBranch = %d, %t, %v; want 43 opened", n, reused, err)
		}
		want := map[string]any{"head": headBranch, "base": baseBranch, "title": "feat: add the widget",
			"body": "- feat: add the widget\n- fix: handle the edge\n\nThe edge case.\n\n" + gitea.Footer}
		if diff := cmp.Diff(want, f.body(openPath)); diff != "" {
			t.Errorf("open body (-want +got):\n%s", diff)
		}
	})

	t.Run("GT-1_an_open_pr_for_the_branch_is_reused", func(t *testing.T) {
		f := newFake(t)
		f.on(listPath, reply(http.StatusOK, []map[string]any{prDoc()}))
		n, reused, err := f.client(t).OpenBranch(context.Background(), repo, baseBranch, headBranch)
		if err != nil || n != number || !reused {
			t.Fatalf("OpenBranch = %d, %t, %v; want 42 reused", n, reused, err)
		}
		if diff := cmp.Diff([]string{listPath}, f.callList()); diff != "" {
			t.Errorf("calls (-want +got):\n%s", diff)
		}
	})

	for _, branch := range []string{baseBranch, "", "feat/a b", "feat;rm", "-x", "/feat", "feat/", "a..b", "feat/$(id)"} {
		t.Run("GT-1_odd_or_base_branch_is_refused_"+branch, func(t *testing.T) {
			f := newFake(t)
			_, _, err := f.client(t).OpenBranch(context.Background(), repo, baseBranch, branch)
			if !errors.Is(err, forge.ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
			if calls := f.callList(); len(calls) != 0 {
				t.Errorf("calls %v, want none", calls)
			}
		})
	}

	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"server_error", raw(http.StatusInternalServerError, "boom")},
		{"broken_json", raw(http.StatusOK, `[{"number":`)},
		{"unreadable_entry", reply(http.StatusOK, []map[string]any{prWith(keyState, "weird")})},
	} {
		t.Run("GT-1_unlistable_open_prs_are_an_error_never_a_second_pr_"+tc.name, func(t *testing.T) {
			f := newFake(t)
			f.on(listPath, tc.h)
			n, _, err := f.client(t).OpenBranch(context.Background(), repo, baseBranch, headBranch)
			if err == nil || n != 0 {
				t.Fatalf("OpenBranch = %d, %v; want an error", n, err)
			}
			if diff := cmp.Diff([]string{listPath}, f.callList()); diff != "" {
				t.Errorf("calls (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("GT-1_no_commits_beyond_base_opens_nothing", func(t *testing.T) {
		f := newFake(t)
		f.on(listPath, reply(http.StatusOK, []map[string]any{}))
		f.on(comparePath, reply(http.StatusOK, map[string]any{"total_commits": 0, "commits": []any{}}))
		if _, _, err := f.client(t).OpenBranch(context.Background(), repo, baseBranch, headBranch); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
		if diff := cmp.Diff([]string{listPath, comparePath}, f.callList()); diff != "" {
			t.Errorf("calls (-want +got):\n%s", diff)
		}
	})
}

func TestOpen(t *testing.T) {
	t.Run("answer_without_number_is_unreadable", func(t *testing.T) {
		f := newFake(t)
		f.on(openPath, reply(http.StatusCreated, map[string]any{}))
		_, err := f.client(t).Open(context.Background(), forge.OpenRequest{Repo: repo, Head: headBranch, Base: baseBranch, Title: title})
		if !errors.Is(err, forge.ErrUnreadable) {
			t.Fatalf("err = %v, want ErrUnreadable", err)
		}
	})

	t.Run("missing_field_fails_before_any_call", func(t *testing.T) {
		f := newFake(t)
		if _, err := f.client(t).Open(context.Background(), forge.OpenRequest{Repo: repo, Head: headBranch}); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
		if calls := f.callList(); len(calls) != 0 {
			t.Errorf("calls %v, want none", calls)
		}
	})
}

func TestMerge(t *testing.T) {
	wantBody := func(whenGreen bool) map[string]any {
		b := map[string]any{"do": "squash", "merge_title_field": "feat: add the widget (#42)",
			"merge_message_field": "Task-Id: t-1", "head_commit_id": headSHA, "delete_branch_after_merge": true}
		if whenGreen {
			b["merge_when_checks_succeed"] = true
		}
		return b
	}

	t.Run("merge_intent_is_merge_when_checks_succeed", func(t *testing.T) {
		f := newFake(t)
		f.on(mergePath, raw(http.StatusCreated, ""))
		if err := f.client(t).EnableAutoMerge(context.Background(), squash(t)); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(wantBody(true), f.body(mergePath)); diff != "" {
			t.Errorf("merge body (-want +got):\n%s", diff)
		}
	})

	t.Run("MG-6_squash_merge_deletes_the_branch", func(t *testing.T) {
		f := newFake(t)
		f.on(pullPath, reply(http.StatusOK, prDoc()))
		f.on(mergePath, raw(http.StatusOK, ""))
		if err := f.client(t).SquashMerge(context.Background(), squash(t)); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(wantBody(false), f.body(mergePath)); diff != "" {
			t.Errorf("merge body (-want +got):\n%s", diff)
		}
	})

	t.Run("MG-6_a_merged_pr_is_success_without_a_merge", func(t *testing.T) {
		f := newFake(t)
		d := prWith(keyState, "closed")
		d[keyMerged] = true
		f.on(pullPath, reply(http.StatusOK, d))
		if err := f.client(t).SquashMerge(context.Background(), squash(t)); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{pullPath}, f.callList()); diff != "" {
			t.Errorf("calls (-want +got):\n%s", diff)
		}
	})

	t.Run("a_closed_pr_is_not_open", func(t *testing.T) {
		f := newFake(t)
		f.on(pullPath, reply(http.StatusOK, prWith(keyState, "closed")))
		if err := f.client(t).SquashMerge(context.Background(), squash(t)); !errors.Is(err, forge.ErrNotOpen) {
			t.Fatalf("err = %v, want ErrNotOpen", err)
		}
	})

	t.Run("MG-6_a_view_without_head_branch_stops_before_the_merge", func(t *testing.T) {
		f := newFake(t)
		f.on(pullPath, reply(http.StatusOK, prWith("head", map[string]any{"sha": headSHA})))
		if err := f.client(t).SquashMerge(context.Background(), squash(t)); !errors.Is(err, forge.ErrUnreadable) {
			t.Fatalf("err = %v, want ErrUnreadable", err)
		}
		if diff := cmp.Diff([]string{pullPath}, f.callList()); diff != "" {
			t.Errorf("calls (-want +got):\n%s", diff)
		}
	})

	t.Run("a_refused_merge_is_an_error", func(t *testing.T) {
		f := newFake(t)
		f.on(pullPath, reply(http.StatusOK, prDoc()))
		f.on(mergePath, raw(http.StatusMethodNotAllowed, `{"message":"head moved"}`))
		if err := f.client(t).SquashMerge(context.Background(), squash(t)); err == nil {
			t.Fatal("err = nil, want the refusal")
		}
	})

	t.Run("request_without_head_fails_before_any_call", func(t *testing.T) {
		f := newFake(t)
		bad := squash(t)
		bad.HeadSHA = ""
		c := f.client(t)
		for _, err := range []error{c.SquashMerge(context.Background(), bad), c.EnableAutoMerge(context.Background(), bad)} {
			if !errors.Is(err, forge.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		}
		if calls := f.callList(); len(calls) != 0 {
			t.Errorf("calls %v, want none", calls)
		}
	})
}

func TestPostStatus(t *testing.T) {
	const path = "POST /api/v1/repos/acme/widget/statuses/abc123def456"
	t.Run("posts_the_status", func(t *testing.T) {
		f := newFake(t)
		f.on(path, raw(http.StatusCreated, "{}"))
		st := forge.Status{Repo: repo, SHA: headSHA, Context: review, State: forge.StatusSuccess, Description: "approved",
			TargetURL: "https://git.example.com/acme/widget/pulls/42#c1"}
		if err := f.client(t).PostStatus(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{keyState: "success", "context": review, "description": "approved",
			"target_url": "https://git.example.com/acme/widget/pulls/42#c1"}
		if diff := cmp.Diff(want, f.body(path)); diff != "" {
			t.Errorf("status body (-want +got):\n%s", diff)
		}
	})

	t.Run("unknown_state_fails_before_any_call", func(t *testing.T) {
		f := newFake(t)
		err := f.client(t).PostStatus(context.Background(), forge.Status{Repo: repo, SHA: headSHA, Context: review, State: "green"})
		if !errors.Is(err, forge.ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
		if calls := f.callList(); len(calls) != 0 {
			t.Errorf("calls %v, want none", calls)
		}
	})
}

func TestDeleteBranch(t *testing.T) {
	const path = "DELETE /api/v1/repos/acme/widget/branches/feat/widget"
	t.Run("deletes_a_branch_with_slashes", func(t *testing.T) {
		f := newFake(t)
		f.on(path, raw(http.StatusNoContent, ""))
		if err := f.client(t).DeleteBranch(context.Background(), repo, headBranch); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a_gone_branch_is_not_found", func(t *testing.T) {
		f := newFake(t)
		f.on(path, raw(http.StatusNotFound, `{"message":"branch not found"}`))
		if err := f.client(t).DeleteBranch(context.Background(), repo, headBranch); !errors.Is(err, forge.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}
