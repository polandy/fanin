package sched

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
)

const (
	ocBranch = "oc/fix-alpha"
	ocRepo   = "owner/alpha"
	promptA1 = "do a1"
)

var committed = model.Result{Kind: model.ResultCommitted, Branch: ocBranch}

// fakeOC delivers and reviews opencode branches; log holds "deliver <branch>" and "review #<n>" in order.
type fakeOC struct {
	mu        sync.Mutex
	log       []string
	pushErr   error
	reviewErr error
}

func (f *fakeOC) Deliver(_ context.Context, _ config.Project, branch string) (OCPR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "deliver "+branch)
	return OCPR{Repo: ocRepo, Number: prNumber}, f.pushErr
}

func (f *fakeOC) Review(_ context.Context, _ config.Project, pr OCPR) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "review "+pr.Repo+"#"+strconv.Itoa(pr.Number))
	return f.reviewErr
}

func (f *fakeOC) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// ocRig is a PR rig with opencode delivery and review whose forge shows the oc branch as the PR head.
func ocRig(t *testing.T, oc *fakeOC) *mergeRig {
	t.Helper()
	pr := greenPR()
	pr.HeadBranch = ocBranch
	r := prRig(t, mergeConfig(), pr)
	r.ocPush, r.ocReview = oc, oc
	return r
}

func TestCommitted(t *testing.T) {
	t.Run("OC-1_a_committed_opencode_branch_is_pushed_reviewed_and_gated_as_its_PR", func(t *testing.T) {
		oc := &fakeOC{}
		r := ocRig(t, oc)
		r.start()
		run := r.startPR("a1", kindOC)
		r.report(run, committed)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{"deliver " + ocBranch, "review " + ocRepo + "#42"}, oc.calls()); diff != "" {
			t.Errorf("oc calls (-want +got):\n%s", diff)
		}
		got := r.run(run.ID)
		if got.Result == nil || got.Result.Kind != model.ResultDonePR || got.Result.PR != prNumber {
			t.Errorf("run result = %+v, want done_pr #42", got.Result)
		}
		if got.Branch != ocBranch {
			t.Errorf("run branch = %q, want %q", got.Branch, ocBranch)
		}
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})

	t.Run("OC-2_COMMITTED_from_a_Claude_worker_is_an_ERROR", func(t *testing.T) {
		oc := &fakeOC{}
		r := ocRig(t, oc)
		r.start()
		run := r.startPR("a1", "")
		r.report(run, committed)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.HasPrefix(got, GateErrorDetail) || !strings.Contains(got, kindCC) {
			t.Errorf("task detail = %q, want an ERROR naming the %s worker", got, kindCC)
		}
		r.noMerge()
		if got := oc.calls(); len(got) != 0 {
			t.Errorf("oc calls = %q, want none", got)
		}
	})

	stops := []struct {
		name      string
		oc        *fakeOC
		calls     []string
		wantInErr string
	}{
		{"OC-6_a_refused_push_stops_with_ERROR_before_the_review_and_the_gate",
			&fakeOC{pushErr: errors.New("push of oc/fix-alpha refused (it is never forced)")},
			[]string{"deliver " + ocBranch}, "it is never forced"},
		{"OC-6_a_failed_review_stops_with_ERROR_before_the_gate",
			&fakeOC{reviewErr: errors.New("the review changed the clone, not posted")},
			[]string{"deliver " + ocBranch, "review " + ocRepo + "#42"}, "not posted"},
	}
	for _, tc := range stops {
		t.Run(tc.name, func(t *testing.T) {
			r := ocRig(t, tc.oc)
			r.start()
			run := r.startPR("a1", kindOC)
			r.report(run, committed)
			r.until(r.taskIn("a1", model.TaskNeedsOwner))
			if got := r.lastDetail("a1"); !strings.HasPrefix(got, GateErrorDetail) || !strings.Contains(got, tc.wantInErr) {
				t.Errorf("task detail = %q, want an ERROR with %q", got, tc.wantInErr)
			}
			if diff := cmp.Diff(tc.calls, tc.oc.calls()); diff != "" {
				t.Errorf("oc calls (-want +got):\n%s", diff)
			}
			if v := r.events("a1", model.EventGateVerdict); len(v) != 0 {
				t.Errorf("verdicts = %q, want none", v)
			}
			r.noMerge()
		})
	}

	t.Run("OC-6_no_opencode_delivery_configured_is_an_ERROR", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		r.start()
		run := r.startPR("a1", kindOC)
		r.report(run, committed)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.HasPrefix(got, GateErrorDetail) {
			t.Errorf("task detail = %q, want an ERROR", got)
		}
		r.noMerge()
	})
}

func TestOpencodeSpec(t *testing.T) {
	t.Run("SP-9_an_opencode_spec_names_its_kind_and_base", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		h.addTask(model.Task{ID: "a1", Title: "oc", Prompt: promptA1, OwnerTask: true, WorkerKind: kindOC})
		h.until(h.runIn(RunID("a1", 1), model.RunWorking))
		specs := h.rt.starts()
		if len(specs) != 1 || specs[0].Kind != runtime.KindOpencode || specs[0].Base != baseMain {
			t.Errorf("specs = %+v, want one opencode spec on %s", specs, baseMain)
		}
	})

	refused := []struct {
		name string
		edit func(*config.Config, *model.Task)
	}{
		{"SP-9_an_opencode_task_on_a_Gitea_project_is_refused", func(c *config.Config, _ *model.Task) {
			c.Projects[0].Forge = "gt"
		}},
		{"SP-9_an_opencode_analysis_is_refused", func(_ *config.Config, task *model.Task) {
			task.Shape = model.ShapeAnalysis
		}},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			task := model.Task{ID: "a1", Title: "oc", Prompt: promptA1, OwnerTask: true, WorkerKind: kindOC}
			tc.edit(&cfg, &task)
			h := newHarness(t, cfg)
			h.start()
			h.addTask(task)
			h.until(h.taskIn("a1", model.TaskFailed))
			got := h.events("a1", model.EventTaskState)
			if !strings.Contains(got[len(got)-1], ErrWorkerKind.Error()) {
				t.Errorf("detail = %q, want %v", got[len(got)-1], ErrWorkerKind)
			}
			if specs := h.rt.starts(); len(specs) != 0 {
				t.Errorf("specs = %+v, want none", specs)
			}
		})
	}
}
