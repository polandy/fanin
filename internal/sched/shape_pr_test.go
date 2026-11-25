package sched

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/prompt"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const reviewNote = "rename the flag"

var donePR = model.Result{Kind: model.ResultDonePR, PR: prNumber}

func conflictPR() forge.PR {
	pr := greenPR()
	pr.Mergeable = forge.MergeableConflicted
	return pr
}

// reviewRedPR has green CI and a review that is not ready.
func reviewRedPR() forge.PR {
	pr := greenPR()
	pr.Checks[1].State, pr.Checks[1].Description = forge.CheckFailure, reviewNote
	return pr
}

// noChecksPR is a Gitea PR without any status.
func noChecksPR() forge.PR {
	pr := greenPR()
	pr.Checks = nil
	return pr
}

// prRig is a merge rig whose forge answers prs in turn.
func prRig(t *testing.T, cfg config.Config, prs ...forge.PR) *mergeRig {
	t.Helper()
	r := newMergeRig(t, cfg)
	if len(prs) > 0 {
		r.f.prs = prs
	}
	return r
}

// startPR adds a PR owner task of kind (the project's when empty) and waits until its first run works.
func (r *mergeRig) startPR(id model.TaskID, kind string) model.Run {
	r.t.Helper()
	task := ownerTask(id)
	task.WorkerKind = kind
	r.addTask(task)
	r.until(r.runIn(RunID(id, 1), model.RunWorking))
	return r.run(RunID(id, 1))
}

// nudgedWith waits until the run's n-th nudge is sent and returns its text.
func (r *mergeRig) nudgedWith(run model.Run, n int) string {
	r.t.Helper()
	r.until(func() bool { return len(r.rt.sent(run.Worker.Agent)) >= n })
	return r.rt.sent(run.Worker.Agent)[n-1]
}

// redRound reports the PR and waits for the n-th nudge, which sends the run back to work.
func (r *mergeRig) redRound(run model.Run, n int) string {
	r.t.Helper()
	r.report(run, donePR)
	text := r.nudgedWith(run, n)
	r.until(r.runIn(run.ID, model.RunWorking))
	r.until(r.taskIn(run.TaskID, model.TaskRunning))
	return text
}

func (r *mergeRig) noMerge() {
	r.t.Helper()
	r.quiet()
	if got := r.log.all(); len(got) != 0 {
		r.t.Errorf("merges = %v, want none", got)
	}
}

func TestPRGreen(t *testing.T) {
	t.Run("GH-13_a_green_GitHub_PR_merges_without_the_owner", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
		v := r.events("a1", model.EventGateVerdict)
		if len(v) != 1 || v[0] != GreenDetail+"#42: GREEN "+prTitle+" ("+AutoNote+")" {
			t.Errorf("verdicts = %q, want one auto green", v)
		}
		if got := r.run(run.ID).Branch; got != headBranch {
			t.Errorf("run branch = %q, want the PR head %q", got, headBranch)
		}
		r.until(r.stopped(run.Worker.Agent))
	})

	t.Run("GH-8_a_worker_that_ends_after_its_review_waits_for_CI_and_merges_without_a_nudge", func(t *testing.T) {
		cfg := mergeConfig()
		cfg.Projects[0].Gate = gateConfig().Projects[0].Gate
		r := prRig(t, cfg, pendingPR(), greenPR())
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(func() bool { _, ok := r.timers("a1")[gate.TimerWait]; return ok })
		r.send(run.Worker.Agent, runtime.RunEvent{Type: runtime.RunEventIdle, LastWords: "review posted"})
		r.quiet()
		r.advance(30 * time.Minute)
		r.until(r.taskIn("a1", model.TaskDone))
		if n := r.events("a1", model.EventWorkerNudged); len(n) != 0 {
			t.Errorf("nudges = %q, want none", n)
		}
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})

	ocMerge := []struct {
		name, merge string
		merged      bool
	}{
		{"OC-7_an_opencode_green_PR_merges_with_oc_merge_auto", config.OCMergeAuto, true},
		{"OC-7_an_opencode_green_PR_waits_for_the_go_with_oc_merge_manual", config.OCMergeManual, false},
	}
	for _, tc := range ocMerge {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mergeConfig()
			cfg.Projects[0].OCMerge = tc.merge
			r := prRig(t, cfg)
			r.start()
			run := r.startPR("a1", kindOC)
			r.report(run, donePR)
			r.until(r.hasEvent("a1", model.EventGateVerdict))
			v := r.events("a1", model.EventGateVerdict)[0]
			if !tc.merged {
				if v != ReadyDetail+"#42: GREEN "+prTitle+"; "+OCManualNote {
					t.Errorf("verdict = %q, want ready with %q", v, OCManualNote)
				}
				r.noMerge()
				r.goMerge("a1")
			} else if v != GreenDetail+"#42: GREEN "+prTitle+" ("+OCAutoNote+")" {
				t.Errorf("verdict = %q, want an opencode auto green", v)
			}
			r.until(r.taskIn("a1", model.TaskDone))
			if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
				t.Errorf("merges (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("GT-7_a_Gitea_green_docs_PR_merges_under_the_lock_and_hook_as_docs-only_auto", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		r.f.files = []string{docFile}
		r.gates = &fakeGates{forge: r.f, kind: forge.KindGitea}
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if v := r.events("a1", model.EventGateVerdict); len(v) != 1 ||
			v[0] != GreenDetail+"#42: GREEN docs "+prTitle+" ("+DocsAutoNote+")" {
			t.Errorf("verdicts = %q, want one docs-only auto green", v)
		}
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})

	ready := []struct {
		name string
		prs  []forge.PR
		want string
	}{
		{"GT-8_OA-6_a_Gitea_green_code_PR_is_ready_and_the_owner_go_merges_it", []forge.PR{greenPR()},
			ReadyDetail + "#42: GREEN code " + prTitle + "; " + GateCodeNote},
		{"GT-8_OA-6_a_Gitea_PR_without_CI_is_ready_and_the_owner_go_merges_it", []forge.PR{noChecksPR()},
			ReadyDetail + "#42: NOCI: no checks after 1 polls; " + NoCINote},
	}
	for _, tc := range ready {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mergeConfig()
			cfg.Projects[0].Gate.Gitea = config.GiteaGate{PollInterval: config.Duration(time.Minute),
				NociAfterPolls: 1, MaxPolls: 2}
			r := prRig(t, cfg, tc.prs...)
			r.f.files = []string{codeFile}
			r.gates = &fakeGates{forge: r.f, kind: forge.KindGitea}
			r.start()
			run := r.startPR("a1", "")
			r.report(run, donePR)
			for !r.hasEvent("a1", model.EventGateVerdict)() {
				r.until(func() bool {
					_, ok := r.timers("a1")[gate.TimerPoll]
					return ok || r.hasEvent("a1", model.EventGateVerdict)()
				})
				r.advance(time.Minute)
			}
			if v := r.events("a1", model.EventGateVerdict); v[0] != tc.want {
				t.Errorf("verdict = %q, want %q", v[0], tc.want)
			}
			r.noMerge()
			r.goMerge("a1")
			r.until(r.taskIn("a1", model.TaskDone))
			if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
				t.Errorf("merges (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPRHead(t *testing.T) {
	onBase := greenPR()
	onBase.HeadBranch = baseMain
	cases := []struct {
		name  string
		pr    forge.PR
		setup func(r *mergeRig, run model.Run)
		res   model.Result
		want  string
	}{
		{name: "GH-14_a_PR_that_heads_the_base_branch_is_blocked", pr: onBase, res: donePR,
			want: "#42 heads the base branch main"},
		{name: "GH-14_a_PR_another_run_claims_is_blocked", pr: greenPR(), res: donePR,
			setup: func(r *mergeRig, _ model.Run) {
				other := r.startPR("b1", "")
				r.tx(func(ctx context.Context, tx *store.Tx) error {
					return tx.SetRunBranch(ctx, other.ID, headBranch)
				})
			},
			want: "#42 is claimed by task b1"},
		{name: "GH-14_a_PR_whose_head_is_not_the_run_branch_is_blocked", pr: greenPR(), res: donePR,
			setup: func(r *mergeRig, run model.Run) {
				r.tx(func(ctx context.Context, tx *store.Tx) error { return tx.SetRunBranch(ctx, run.ID, "feat/other") })
			},
			want: "#42 heads feat/alpha-42, not the run's branch feat/other"},
		{name: "GH-14_a_result_without_a_PR_number_is_blocked", pr: greenPR(),
			res:  model.Result{Kind: model.ResultPushed, Branch: headBranch},
			want: "reported pushed without a PR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := prRig(t, mergeConfig(), tc.pr)
			r.start()
			run := r.startPR("a1", "")
			if tc.setup != nil {
				tc.setup(r, run)
			}
			r.report(run, tc.res)
			r.until(r.taskIn("a1", model.TaskNeedsOwner))
			got := r.lastDetail("a1")
			if !strings.HasPrefix(got, BlockedDetail+run.Worker.Agent+": ") || !strings.Contains(got, tc.want) {
				t.Errorf("task detail = %q, want blocked with %q", got, tc.want)
			}
			if v := r.events("a1", model.EventGateVerdict); len(v) != 0 {
				t.Errorf("verdicts = %q, want none: never gated", v)
			}
			r.noMerge()
		})
	}

	t.Run("GH-14_FP-6_an_unreadable_head_is_a_named_error_never_a_verdict", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		r.f.prs, r.f.errs = nil, []error{forge.ErrNetwork}
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.HasPrefix(got, GateErrorDetail+"#42: ") ||
			!strings.Contains(got, forge.ErrNetwork.Error()) {
			t.Errorf("task detail = %q, want %s#42 with the cause", got, GateErrorDetail)
		}
		if v := r.events("a1", model.EventGateVerdict); len(v) != 0 {
			t.Errorf("verdicts = %q, want none", v)
		}
		r.noMerge()
	})

	t.Run("FP-6_a_gate_without_a_reader_is_a_named_error", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		r.gates = &fakeGates{err: errNoForge}
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.HasPrefix(got, GateErrorDetail+"#42: ") ||
			!strings.Contains(got, errNoForge.Error()) {
			t.Errorf("task detail = %q, want %s#42 with the cause", got, GateErrorDetail)
		}
		r.noMerge()
	})
}

func TestPRRed(t *testing.T) {
	t.Run("GH-9_OC-8_red_CI_fix_nudges_then_a_fresh_run_then_paused", func(t *testing.T) {
		r := prRig(t, mergeConfig(), redPR())
		r.start()
		run := r.startPR("a1", "")
		for n := 1; n <= 3; n++ {
			for i := 1; i <= testBudgets.FixRoundsPerRun; i++ {
				text := r.redRound(run, i)
				if !strings.Contains(text, "#42 RED: failed: ci FAILURE") {
					t.Errorf("run %d fix nudge %d = %q, want the reasons", n, i, text)
				}
			}
			if got := r.run(run.ID).Budget.FixRounds; got != testBudgets.FixRoundsPerRun {
				t.Errorf("run %d fix rounds = %d, want %d", n, got, testBudgets.FixRoundsPerRun)
			}
			r.report(run, donePR)
			if n == 3 {
				break
			}
			next := RunID("a1", n+1)
			r.until(r.runIn(next, model.RunWorking))
			if got := r.startSpec(AgentName("a1", n+1)).Prompt; !strings.Contains(got, "PR #42 is open") {
				t.Errorf("run %d prompt lacks the open PR:\n%s", n+1, got)
			}
			run = r.run(next)
		}
		r.until(r.taskIn("a1", model.TaskPaused))
		want := PausedDetail + "a1-3: fix budget spent: fix round 3 of 2; 3 of 3 runs used"
		if got := r.lastDetail("a1"); got != want {
			t.Errorf("task detail = %q, want %q", got, want)
		}
		nudges := r.events("a1", model.EventWorkerNudged)
		if len(nudges) != 6 || slices.ContainsFunc(nudges, func(n string) bool { return n != string(prompt.NudgeFix) }) {
			t.Errorf("nudges = %q, want six %s", nudges, prompt.NudgeFix)
		}
		r.noMerge()
	})

	review := []struct{ name, kind string }{
		{"GH-10_a_review_red_does_not_use_the_fix_budget_up_to_3_then_paused", ""},
		{"OC-8_an_opencode_review_red_sends_the_reviewer_text", kindOC},
	}
	for _, tc := range review {
		t.Run(tc.name, func(t *testing.T) {
			r := prRig(t, mergeConfig(), reviewRedPR())
			r.start()
			run := r.startPR("a1", tc.kind)
			for i := 1; i <= ReviewNudges; i++ {
				if text := r.redRound(run, i); !strings.Contains(text, "review: not ready: "+reviewNote) {
					t.Errorf("review nudge %d = %q, want the reviewer's text", i, text)
				}
			}
			r.report(run, donePR)
			r.until(r.taskIn("a1", model.TaskPaused))
			b := r.run(run.ID).Budget
			if b.FixRounds != 0 || b.ReviewNudges != ReviewNudges {
				t.Errorf("budget = %+v, want no fix round and %d review nudges", b, ReviewNudges)
			}
			if got := r.lastDetail("a1"); !strings.HasPrefix(got, PausedDetail+"a1-1: #42 RED: review: not ready") {
				t.Errorf("task detail = %q, want paused with the review", got)
			}
			if runs := r.runs("a1"); len(runs) != 1 {
				t.Errorf("runs = %d, want the one run", len(runs))
			}
			r.noMerge()
		})
	}

	t.Run("CF-1_one_conflict_nudge_from_the_CI_budget_then_a_re-gate_and_a_merge", func(t *testing.T) {
		r := prRig(t, mergeConfig(), conflictPR(), greenPR())
		r.start()
		run := r.startPR("a1", "")
		text := r.redRound(run, 1)
		if !strings.Contains(text, "origin/main") || !strings.Contains(text, "CONFLICTING") {
			t.Errorf("conflict nudge = %q, want merge origin/main and the reason", text)
		}
		if got := r.run(run.ID).Budget.FixRounds; got != 1 {
			t.Errorf("fix rounds = %d, want the nudge taken from the CI budget", got)
		}
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{string(prompt.NudgeConflict)}, r.events("a1", model.EventWorkerNudged)); diff != "" {
			t.Errorf("nudges (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})

	t.Run("CF-2_a_conflict_that_stays_after_the_nudge_pauses_with_budget_left", func(t *testing.T) {
		r := prRig(t, mergeConfig(), conflictPR())
		r.start()
		run := r.startPR("a1", "")
		r.redRound(run, 1)
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskPaused))
		if got := r.lastDetail("a1"); !strings.HasPrefix(got, PausedDetail+"a1-1: #42 RED: CONFLICTING") {
			t.Errorf("task detail = %q, want paused CONFLICTING", got)
		}
		if got := r.run(run.ID).Budget.FixRounds; got >= testBudgets.FixRoundsPerRun {
			t.Errorf("fix rounds = %d, want budget left", got)
		}
		r.noMerge()
	})

	t.Run("CF-3_a_conflict_at_the_merge_re-gate_gets_the_conflict_nudge_then_merges", func(t *testing.T) {
		r := prRig(t, mergeConfig(), greenPR(), conflictPR(), greenPR())
		r.start()
		run := r.startPR("a1", "")
		r.ghostLock()
		r.report(run, donePR)
		r.await(r.waits)
		r.advance(lockLease)
		text := r.nudgedWith(run, 1)
		if !strings.Contains(text, "CONFLICTING") {
			t.Errorf("nudge = %q, want the conflict", text)
		}
		r.until(r.taskIn("a1", model.TaskRunning))
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{string(prompt.NudgeConflict)}, r.events("a1", model.EventWorkerNudged)); diff != "" {
			t.Errorf("nudges (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})

	t.Run("GH-9_a_closed_PR_is_blocked_without_a_nudge", func(t *testing.T) {
		closed := greenPR()
		closed.State = forge.StateClosed
		r := prRig(t, mergeConfig(), closed)
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.HasPrefix(got, BlockedDetail+run.Worker.Agent+": #42 RED: PR is") {
			t.Errorf("task detail = %q, want blocked with the state", got)
		}
		if n := r.events("a1", model.EventWorkerNudged); len(n) != 0 {
			t.Errorf("nudges = %q, want none", n)
		}
		r.noMerge()
	})
}
