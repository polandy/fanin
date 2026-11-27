package github_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

func TestPR(t *testing.T) {
	t.Run("GH-14_view_reads_head_branch_sha_mergeable_and_checks", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, prDoc())})
		got, err := c.PR(context.Background(), repo, number)
		if err != nil {
			t.Fatal(err)
		}
		want := forge.PR{
			Repo: repo, Number: number, Title: title, URL: "https://github.com/acme/widget/pull/42",
			State: forge.StateOpen, Mergeable: forge.MergeableYes, HeadBranch: headBranch, HeadSHA: headSHA,
			BaseBranch: baseBranch,
			Checks: []forge.Check{
				{Name: "ci", State: forge.CheckSuccess, UpdatedAt: time.Date(2026, 11, 25, 9, 5, 0, 0, time.UTC)},
				{Name: "lint", State: forge.CheckPending, UpdatedAt: time.Date(2026, 11, 25, 9, 1, 0, 0, time.UTC)},
				{Name: reviewContext, State: forge.CheckSuccess, UpdatedAt: time.Date(2026, 11, 25, 9, 6, 0, 0, time.UTC),
					Description: reviewText},
			},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("PR (-want +got):\n%s", diff)
		}
	})

	t.Run("check_states_are_normalised", func(t *testing.T) {
		d := prDoc()
		d["statusCheckRollup"] = []any{
			checkRun("a", completed, "CANCELLED", "", doneAt),
			checkRun("b", completed, "TIMED_OUT", "", doneAt),
			checkRun("c", "QUEUED", "", "", ""),
			commitStatus("d", "ERROR", keyCreated, doneAt),
		}
		c, r := newClient(t)
		r.Expect(viewArgv, testutil.Reply{Stdout: marshal(t, d)})
		got, err := c.PR(context.Background(), repo, number)
		if err != nil {
			t.Fatal(err)
		}
		var states []forge.CheckState
		for _, ch := range got.Checks {
			states = append(states, ch.State)
		}
		want := []forge.CheckState{forge.CheckCancelled, forge.CheckFailure, forge.CheckPending, forge.CheckFailure}
		if diff := cmp.Diff(want, states); diff != "" {
			t.Errorf("states (-want +got):\n%s", diff)
		}
	})
}

func TestPRUnreadable(t *testing.T) {
	broken := func(mut func(map[string]any)) string {
		d := prDoc()
		mut(d)
		return marshal(t, d)
	}
	check := func(c map[string]any) func(map[string]any) {
		return func(d map[string]any) { d["statusCheckRollup"] = []any{c} }
	}
	cases := []struct {
		name  string
		reply testutil.Reply
		want  error
	}{
		{"GH-5_pr_not_found", testutil.Reply{ExitCode: 1, Stderr: "GraphQL: Could not resolve to a PullRequest"}, forge.ErrNotFound},
		{"GH-5_pr_network_failure", testutil.Reply{ExitCode: 1, Stderr: netStderr}, forge.ErrNetwork},
		{"GH-5_pr_view_fails", testutil.Reply{ExitCode: 4, Stderr: "gh: To get started with GitHub CLI, please run: gh auth login"}, nil},
		{"GH-5_empty_json", testutil.Reply{Stdout: ""}, forge.ErrUnreadable},
		{"GH-5_broken_json", testutil.Reply{Stdout: `{"number": 42,`}, forge.ErrUnreadable},
		{"GH-5_trailing_json", testutil.Reply{Stdout: marshal(t, prDoc()) + "{}"}, forge.ErrUnreadable},
		{"GH-5_wrong_pr_number", testutil.Reply{Stdout: broken(func(d map[string]any) { d["number"] = 7 })}, forge.ErrUnreadable},
		{"GH-5_unknown_state", testutil.Reply{Stdout: broken(func(d map[string]any) { d[keyState] = "DRAFT" })}, forge.ErrUnreadable},
		{"GH-5_missing_mergeable", testutil.Reply{Stdout: broken(func(d map[string]any) { delete(d, "mergeable") })}, forge.ErrUnreadable},
		{"GH-5_unknown_mergeable", testutil.Reply{Stdout: broken(func(d map[string]any) { d["mergeable"] = "MAYBE" })}, forge.ErrUnreadable},
		{"GH-5_GH-14_missing_head_branch", testutil.Reply{Stdout: broken(func(d map[string]any) { delete(d, "headRefName") })}, forge.ErrUnreadable},
		{"GH-5_missing_head_sha", testutil.Reply{Stdout: broken(func(d map[string]any) { d["headRefOid"] = "" })}, forge.ErrUnreadable},
		{"GH-5_unknown_check_type", testutil.Reply{Stdout: broken(check(map[string]any{keyType: "Robot"}))}, forge.ErrUnreadable},
		{"GH-5_unknown_conclusion", testutil.Reply{Stdout: broken(check(checkRun("ci", completed, "MAYBE", "", doneAt)))}, forge.ErrUnreadable},
		{"GH-5_unreadable_check_time", testutil.Reply{Stdout: broken(check(checkRun("ci", completed, success, "", "yesterday")))}, forge.ErrUnreadable},
		{"GH-5_unset_check_time", testutil.Reply{Stdout: broken(check(checkRun("ci", completed, success, "", "0001-01-01T00:00:00Z")))}, forge.ErrUnreadable},
		{"GH-5_unreadable_review_time", testutil.Reply{Stdout: broken(check(commitStatus(reviewContext, success, keyStarted, "soon")))}, forge.ErrUnreadable},
		{"GH-5_missing_review_time", testutil.Reply{Stdout: broken(check(commitStatus(reviewContext, success, "", "")))}, forge.ErrUnreadable},
		{"GH-5_unknown_status_state", testutil.Reply{Stdout: broken(check(commitStatus(reviewContext, "GREEN", keyStarted, doneAt)))}, forge.ErrUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := newClient(t)
			r.Expect(viewArgv, tc.reply)
			got, err := c.PR(context.Background(), repo, number)
			if err == nil {
				t.Fatalf("PR = %+v, want an error", got)
			}
			if diff := cmp.Diff(forge.PR{}, got); diff != "" {
				t.Errorf("an unreadable PR carries data (-want +got):\n%s", diff)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestListOpen(t *testing.T) {
	argv := []string{"gh", "pr", "list", flagRepo, repo, "--state", "open", flagLimit, "1000", "--json",
		"number,title,url,state,headRefName,headRefOid,baseRefName"}
	t.Run("lists_heads_without_checks", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: `[{"number":42,"title":"t","url":"u","state":"OPEN",` +
			`"headRefName":"feat/widget","headRefOid":"abc","baseRefName":"main"}]`})
		got, err := c.ListOpen(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		want := []forge.PR{{Repo: repo, Number: 42, Title: "t", URL: "u", State: forge.StateOpen,
			HeadBranch: headBranch, HeadSHA: "abc", BaseBranch: baseBranch}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("ListOpen (-want +got):\n%s", diff)
		}
	})
	t.Run("GH-14_entry_without_head_branch_is_unreadable", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: `[{"number":42,"state":"OPEN","headRefOid":"abc","baseRefName":"main"}]`})
		if got, err := c.ListOpen(context.Background(), repo); !errors.Is(err, forge.ErrUnreadable) || got != nil {
			t.Errorf("ListOpen = %v, %v; want nil, ErrUnreadable", got, err)
		}
	})
	t.Run("GH-5_object_instead_of_list_is_unreadable", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: `{}`})
		if _, err := c.ListOpen(context.Background(), repo); !errors.Is(err, forge.ErrUnreadable) {
			t.Errorf("err = %v, want ErrUnreadable", err)
		}
	})
}

func TestFiles(t *testing.T) {
	argv := []string{"gh", api, "--paginate", "repos/acme/widget/pulls/42/files", "--jq", ".[].filename"}
	t.Run("one_path_per_line", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: "go.mod\ninternal/forge/forge.go\n"})
		got, err := c.Files(context.Background(), repo, number)
		if diff := cmp.Diff([]string{"go.mod", "internal/forge/forge.go"}, got); err != nil || diff != "" {
			t.Errorf("Files err %v (-want +got):\n%s", err, diff)
		}
	})
	t.Run("no_files_is_an_empty_list", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{})
		if got, err := c.Files(context.Background(), repo, number); err != nil || got == nil || len(got) != 0 {
			t.Errorf("Files = %#v, %v; want an empty list", got, err)
		}
	})
	t.Run("GH-5_empty_line_is_unreadable", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: "go.mod\n\n"})
		if _, err := c.Files(context.Background(), repo, number); !errors.Is(err, forge.ErrUnreadable) {
			t.Errorf("err = %v, want ErrUnreadable", err)
		}
	})
	t.Run("not_found", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{ExitCode: 1, Stderr: "gh: Not Found (HTTP 404)"})
		if _, err := c.Files(context.Background(), repo, number); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestComments(t *testing.T) {
	argv := []string{"gh", api, "--paginate", "repos/acme/widget/issues/42/comments", "--jq",
		".[] | {id, body, html_url} | @json"}
	t.Run("OC-3_one_comment_per_line", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: `{"id":7,"body":"first","html_url":"https://github.com/c/7"}` + "\n" +
			`{"id":9,"body":"## PR quality review\nok","html_url":"https://github.com/c/9"}` + "\n"})
		got, err := c.Comments(context.Background(), repo, number)
		want := []forge.Comment{
			{ID: 7, Body: "first", URL: "https://github.com/c/7"},
			{ID: 9, Body: "## PR quality review\nok", URL: "https://github.com/c/9"},
		}
		if diff := cmp.Diff(want, got); err != nil || diff != "" {
			t.Errorf("Comments err %v (-want +got):\n%s", err, diff)
		}
	})
	t.Run("no_comments_is_an_empty_list", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{})
		if got, err := c.Comments(context.Background(), repo, number); err != nil || got == nil || len(got) != 0 {
			t.Errorf("Comments = %#v, %v; want an empty list", got, err)
		}
	})
	for name, line := range map[string]string{
		"GH-5_broken_json_is_unreadable": `{"id":7,`,
		"GH-5_missing_id_is_unreadable":  `{"body":"x","html_url":"u"}`,
		"GH-5_missing_url_is_unreadable": `{"id":7,"body":"x"}`,
		"GH-5_empty_line_is_unreadable":  "",
	} {
		t.Run(name, func(t *testing.T) {
			c, r := newClient(t)
			r.Expect(argv, testutil.Reply{Stdout: line + "\n"})
			if _, err := c.Comments(context.Background(), repo, number); !errors.Is(err, forge.ErrUnreadable) {
				t.Errorf("err = %v, want ErrUnreadable", err)
			}
		})
	}
	t.Run("not_found", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{ExitCode: 1, Stderr: "gh: Not Found (HTTP 404)"})
		if _, err := c.Comments(context.Background(), repo, number); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}
