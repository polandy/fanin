package github_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/github"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

var _ forge.BranchChecks = (*github.Client)(nil)

// branchDoc is the GraphQL answer for the head of main with the given rollup nodes; nil nodes is no rollup.
func branchDoc(more bool, nodes ...any) map[string]any {
	target := map[string]any{"statusCheckRollup": nil}
	if nodes != nil {
		target["statusCheckRollup"] = map[string]any{"contexts": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": more}, "nodes": nodes}}
	}
	return map[string]any{"data": map[string]any{"repository": map[string]any{"ref": map[string]any{
		"target": target}}}}
}

func TestBranchChecks(t *testing.T) {
	argv := []string{"gh", api, "graphql", "-f", "query=" + github.BranchChecksQuery, "-f", "owner=acme",
		"-f", "name=widget", "-f", "ref=refs/heads/main"}
	t.Run("MG-8_reads_check_runs_and_statuses_of_the_branch_head", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: marshal(t, branchDoc(false,
			checkRun("ci", completed, "FAILURE", "2026-11-25T09:00:00Z", doneAt),
			commitStatus("deploy", success, keyCreated, doneAt)))})
		got, err := c.BranchChecks(context.Background(), repo, baseBranch)
		at := time.Date(2026, 11, 25, 9, 5, 0, 0, time.UTC)
		want := []forge.Check{
			{Name: "ci", State: forge.CheckFailure, UpdatedAt: at},
			{Name: "deploy", State: forge.CheckSuccess, UpdatedAt: at},
		}
		if diff := cmp.Diff(want, got); err != nil || diff != "" {
			t.Errorf("BranchChecks err %v (-want +got):\n%s", err, diff)
		}
	})
	t.Run("MG-8_no_rollup_is_no_checks", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: marshal(t, branchDoc(false))})
		if got, err := c.BranchChecks(context.Background(), repo, baseBranch); err != nil || got == nil || len(got) != 0 {
			t.Errorf("BranchChecks = %#v, %v; want an empty list", got, err)
		}
	})
	t.Run("MG-8_missing_branch_is_not_found", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{Stdout: `{"data":{"repository":{"ref":null}}}`})
		if _, err := c.BranchChecks(context.Background(), repo, baseBranch); !errors.Is(err, forge.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
	for name, out := range map[string]string{
		"MG-8_broken_json_is_unreadable":    `{"data":`,
		"MG-8_no_repository_is_unreadable":  `{"data":{}}`,
		"MG-8_unknown_check_is_unreadable":  marshal(t, branchDoc(false, map[string]any{keyType: "Other"})),
		"MG-8_more_than_a_page_is_unreadable": marshal(t, branchDoc(true, checkRun("ci", completed, success, doneAt, doneAt))),
	} {
		t.Run(name, func(t *testing.T) {
			c, r := newClient(t)
			r.Expect(argv, testutil.Reply{Stdout: out})
			if _, err := c.BranchChecks(context.Background(), repo, baseBranch); !errors.Is(err, forge.ErrUnreadable) {
				t.Errorf("err = %v, want ErrUnreadable", err)
			}
		})
	}
	t.Run("MG-8_network_failure", func(t *testing.T) {
		c, r := newClient(t)
		r.Expect(argv, testutil.Reply{ExitCode: 1, Stderr: netStderr})
		if _, err := c.BranchChecks(context.Background(), repo, baseBranch); !errors.Is(err, forge.ErrNetwork) {
			t.Errorf("err = %v, want ErrNetwork", err)
		}
	})
	t.Run("repo_without_owner_is_refused_before_gh", func(t *testing.T) {
		c, _ := newClient(t)
		if _, err := c.BranchChecks(context.Background(), "widget", baseBranch); !errors.Is(err, forge.ErrInvalidRequest) {
			t.Errorf("err = %v, want ErrInvalidRequest", err)
		}
	})
}
