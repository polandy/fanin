package gitea_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/gitea"
)

const branchStatusPath = "GET /api/v1/repos/acme/widget/commits/main/status"

var _ forge.BranchChecks = (*gitea.Client)(nil)

func TestBranchChecks(t *testing.T) {
	t.Run("MG-8_reads_the_statuses_of_the_branch_head", func(t *testing.T) {
		f := newFake(t)
		f.on(branchStatusPath, reply(http.StatusOK, combined(status("ci", "failure"), status("lint", valSuccess))))
		got, err := f.client(t).BranchChecks(context.Background(), repo, baseBranch)
		at := time.Date(2026, 11, 25, 9, 5, 0, 0, time.UTC)
		want := []forge.Check{
			{Name: "ci", State: forge.CheckFailure, UpdatedAt: at},
			{Name: "lint", State: forge.CheckSuccess, UpdatedAt: at},
		}
		if diff := cmp.Diff(want, got); err != nil || diff != "" {
			t.Errorf("BranchChecks err %v (-want +got):\n%s", err, diff)
		}
	})
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		want error
	}{
		{"MG-8_missing_branch_is_not_found", raw(http.StatusNotFound, "not found"), forge.ErrNotFound},
		{"MG-8_" + caseBrokenJSON + "_is_unreadable", raw(http.StatusOK, `{"statuses":[`), forge.ErrUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.on(branchStatusPath, tc.h)
			if _, err := f.client(t).BranchChecks(context.Background(), repo, baseBranch); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
