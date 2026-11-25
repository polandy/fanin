package sched

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/notify"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/store"
)

const (
	repoAlpha  = "acme/alpha"
	prNumber   = 42
	prTitle    = "docs: write up the finding"
	issueNum   = 7
	issueURL   = "https://forge.example/acme/alpha/issues/7"
	question   = "which base do you want?"
	codeFile   = "internal/x/x.go"
	docFile    = "docs/dev/finding.md"
	agentFile  = "CLAUDE.md"
	closeByHnd = "close it by hand"
	headBranch = "feat/alpha-42"
)

var (
	errNoteDown = errors.New("signal down")
	errPaneGone = errors.New("pane gone")
	errNoForge  = errors.New("no forge for alpha")
)

// fakeNotifier records every note, or fails each with err.
type fakeNotifier struct {
	mu    sync.Mutex
	notes []notify.Note
	err   error
}

func (f *fakeNotifier) Notify(_ context.Context, n notify.Note) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, n)
	return f.err
}

func (f *fakeNotifier) sent() []notify.Note {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notify.Note(nil), f.notes...)
}

// fakeForge answers PR from a queue whose last answer repeats, and Files from fixed values.
type fakeForge struct {
	mu       sync.Mutex
	prs      []forge.PR
	errs     []error
	files    []string
	filesErr error
	reads    int
}

func (f *fakeForge) PR(context.Context, string, int) (forge.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(f.reads, max(len(f.prs), len(f.errs))-1)
	f.reads++
	var pr forge.PR
	var err error
	if i < len(f.prs) {
		pr = f.prs[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return pr, err
}

func (f *fakeForge) Files(context.Context, string, int) ([]string, error) {
	return f.files, f.filesErr
}

func (f *fakeForge) prReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// fakeGates hands out one forge of kind (GitHub when unset) for every PR of alpha, or fails with err.
type fakeGates struct {
	forge *fakeForge
	kind  forge.Kind
	err   error
}

func (f *fakeGates) For(_ config.Project, pr int) (gate.Reader, gate.Target, error) {
	if f.err != nil {
		return nil, gate.Target{}, f.err
	}
	kind := f.kind
	if kind == "" {
		kind = forge.KindGitHub
	}
	return f.forge, gate.Target{Kind: kind, Repo: repoAlpha, Number: pr, Base: baseMain}, nil
}

// greenPR is an open, mergeable PR whose CI and review passed.
func greenPR() forge.PR {
	return forge.PR{Repo: repoAlpha, Number: prNumber, Title: prTitle, State: forge.StateOpen,
		Mergeable: forge.MergeableYes, HeadSHA: "abc123", HeadBranch: headBranch, BaseBranch: baseMain, Checks: []forge.Check{
			{Name: "ci", State: forge.CheckSuccess, UpdatedAt: epoch},
			{Name: gate.ReviewContext, State: forge.CheckSuccess, UpdatedAt: epoch.Add(time.Minute)}}}
}

func pendingPR() forge.PR {
	pr := greenPR()
	pr.Checks[0].State = forge.CheckPending
	return pr
}

func redPR() forge.PR {
	pr := greenPR()
	pr.Checks[0].State = forge.CheckFailure
	return pr
}

// startAnalysis adds an analysis owner task and waits until its first run works.
func (h *harness) startAnalysis(id model.TaskID) model.Run {
	h.t.Helper()
	task := ownerTask(id)
	task.Shape = model.ShapeAnalysis
	h.addTask(task)
	h.until(h.runIn(RunID(id, 1), model.RunWorking))
	return h.run(RunID(id, 1))
}

// report records res as the run's result, as fanin_report does.
func (h *harness) report(run model.Run, res model.Result) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx *store.Tx) error { return tx.SetRunResult(ctx, run.ID, res) })
}

// lastDetail is the detail of the task's latest task_state event.
func (h *harness) lastDetail(id model.TaskID) string {
	h.t.Helper()
	d := h.events(id, model.EventTaskState)
	if len(d) == 0 {
		return ""
	}
	return d[len(d)-1]
}

func (h *harness) stopped(agent string) func() bool {
	return func() bool { return slices.Contains(h.rt.stops(), agent) }
}

func (h *harness) hasEvent(id model.TaskID, typ model.EventType) func() bool {
	return func() bool { return len(h.events(id, typ)) > 0 }
}

func TestAnalysisResults(t *testing.T) {
	ends := []struct {
		name   string
		res    model.Result
		detail string
	}{
		{"FP-3_issue_closes_the_pane_and_ends_done_analysis_needs_owner_with_url",
			model.Result{Kind: model.ResultIssue, Issue: issueNum, IssueURL: issueURL},
			AnalysisIssueDetail + issueURL},
		{"FP-3_issue_without_url_ends_done_with_the_issue_number",
			model.Result{Kind: model.ResultIssue, Issue: issueNum}, AnalysisIssueDetail + "#7"},
		{"FP-3_nothing_closes_the_pane_and_ends_idle",
			model.Result{Kind: model.ResultNothing, Text: textNoTodo}, IdleDetail + textNoTodo},
	}
	for _, tc := range ends {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.start()
			run := h.startAnalysis("a1")
			h.report(run, tc.res)
			h.until(h.stopped(run.Worker.Agent))
			if got := h.task("a1").State; got != model.TaskDone {
				t.Errorf("task state = %s, want %s", got, model.TaskDone)
			}
			if got := h.lastDetail("a1"); got != tc.detail {
				t.Errorf("task detail = %q, want %q", got, tc.detail)
			}
			if got := h.run(run.ID).State; got != model.RunSucceeded {
				t.Errorf("run state = %s, want %s", got, model.RunSucceeded)
			}
			if n := h.nt.sent(); len(n) != 0 {
				t.Errorf("notes = %v, want none", n)
			}
		})
	}

	t.Run("FP-3_blocked_notifies_ends_blocked_and_keeps_the_pane", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultBlocked, Text: question})
		h.until(h.hasEvent("a1", model.EventNotified))
		want := BlockedDetail + run.Worker.Agent + ": " + question
		if got := h.task("a1").State; got != model.TaskNeedsOwner {
			t.Errorf("task state = %s, want %s", got, model.TaskNeedsOwner)
		}
		if got := h.lastDetail("a1"); got != want {
			t.Errorf("task detail = %q, want %q", got, want)
		}
		if n := h.nt.sent(); len(n) != 1 || n[0].TaskID != "a1" || n[0].Project != projAlpha || n[0].Text != want {
			t.Errorf("notes = %+v, want one for a1 with %q", n, want)
		}
		if got := h.run(run.ID).State; got != model.RunReported {
			t.Errorf("run state = %s, want %s", got, model.RunReported)
		}
		if s := h.rt.stops(); len(s) != 0 {
			t.Errorf("stopped %v, want the pane kept", s)
		}
	})

	t.Run("FP-3_blocked_with_a_failed_note_still_ends_blocked", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.nt.err = errNoteDown
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultBlocked, Text: question})
		h.until(h.hasEvent("a1", model.EventNotifyFailed))
		if got := h.task("a1").State; got != model.TaskNeedsOwner {
			t.Errorf("task state = %s, want %s", got, model.TaskNeedsOwner)
		}
		if got := h.events("a1", model.EventNotifyFailed); !strings.Contains(got[0], errNoteDown.Error()) {
			t.Errorf("notify_failed = %q, want the cause", got[0])
		}
	})

	t.Run("FP-3_a_pushed_analysis_is_blocked_not_done", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultPushed, Branch: "fix/x"})
		h.until(h.hasEvent("a1", model.EventNotified))
		if got := h.task("a1").State; got != model.TaskNeedsOwner {
			t.Errorf("task state = %s, want %s", got, model.TaskNeedsOwner)
		}
		if got := h.lastDetail("a1"); !strings.HasPrefix(got, BlockedDetail+run.Worker.Agent+": ") ||
			!strings.Contains(got, string(model.ResultPushed)) {
			t.Errorf("task detail = %q, want blocked naming %s", got, model.ResultPushed)
		}
	})

	t.Run("FP-3_a_pane_that_cannot_close_is_logged_and_the_task_stays_done", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.rt.stopErr = errPaneGone
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultNothing, Text: textNoTodo})
		h.until(h.logged(closeByHnd))
		if got := h.task("a1").State; got != model.TaskDone {
			t.Errorf("task state = %s, want %s", got, model.TaskDone)
		}
	})

	t.Run("FP-3_an_unwritable_end_changes_nothing_and_is_retried", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startAnalysis("a1")
		h.fault("tasks", "UPDATE")
		h.report(run, model.Result{Kind: model.ResultIssue, Issue: issueNum, IssueURL: issueURL})
		h.until(h.logged(stepFailed))
		if got := h.task("a1").State; got != model.TaskRunning {
			t.Errorf("task state after the fault = %s, want %s", got, model.TaskRunning)
		}
		if s := h.rt.stops(); len(s) != 0 {
			t.Errorf("stopped %v before the end was written", s)
		}
		h.clearFault("tasks", "UPDATE")
		h.advance(RetryAfter)
		h.until(h.stopped(run.Worker.Agent))
		if got := h.task("a1").State; got != model.TaskDone {
			t.Errorf("task state = %s, want %s", got, model.TaskDone)
		}
	})
}

// gateConfig is testConfig with alpha's gate waiting once on pending checks.
func gateConfig() config.Config {
	cfg := testConfig()
	cfg.Projects[0].Gate = config.Gate{GitHub: config.GitHubGate{WatchTimeout: config.Duration(30 * time.Minute),
		MaxWaits: 1, MaxWatchFailures: 1}}
	return cfg
}

func TestAnalysisPR(t *testing.T) {
	ready := []struct {
		name  string
		files []string
		err   error
		want  string
	}{
		{"GH-11_green_analysis_PR_with_code_is_ready_for_the_owner_go_no_merge",
			[]string{docFile, codeFile}, nil, GateCodeNote},
		{"GH-12_docs_only_analysis_PR_is_ready_as_docs_only", []string{docFile, "README.md"}, nil, GateDocsNote},
		{"GH-12_CLAUDE_md_counts_as_code", []string{docFile, agentFile}, nil, GateCodeNote},
		{"GH-12_unreadable_files_count_as_code", nil, errNoForge, GateCodeNote},
	}
	for _, tc := range ready {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.gates = &fakeGates{forge: &fakeForge{prs: []forge.PR{greenPR()}, files: tc.files, filesErr: tc.err}}
			h.start()
			run := h.startAnalysis("a1")
			h.report(run, model.Result{Kind: model.ResultDonePR, PR: prNumber})
			h.until(h.hasEvent("a1", model.EventGateVerdict))
			v := h.events("a1", model.EventGateVerdict)[0]
			if !strings.HasPrefix(v, ReadyDetail+"#42: GREEN "+prTitle) || !strings.Contains(v, tc.want) {
				t.Errorf("verdict = %q, want ready #42 GREEN with %q", v, tc.want)
			}
			if got := h.task("a1").State; got != model.TaskGating {
				t.Errorf("task state = %s, want %s", got, model.TaskGating)
			}
			if err := owner.New(h.st, owner.NoDeliverer{}, slog.New(slog.DiscardHandler)).Go(t.Context(), "a1"); err != nil {
				t.Errorf("owner go = %v, want the task ready for it", err)
			}
			h.quiet()
			if m := h.events("a1", model.EventMerged); len(m) != 0 {
				t.Errorf("merged %v, want no merge", m)
			}
			if s := h.rt.stops(); len(s) != 0 {
				t.Errorf("stopped %v, want the pane kept while the PR waits", s)
			}
		})
	}

	t.Run("GH-11_red_analysis_PR_moves_to_the_owner_and_notifies", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.gates = &fakeGates{forge: &fakeForge{prs: []forge.PR{redPR()}}}
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultDonePR, PR: prNumber})
		h.until(h.hasEvent("a1", model.EventNotified))
		if got := h.task("a1").State; got != model.TaskNeedsOwner {
			t.Errorf("task state = %s, want %s", got, model.TaskNeedsOwner)
		}
		if got := h.lastDetail("a1"); !strings.HasPrefix(got, BlockedDetail+run.Worker.Agent+": #42 RED") {
			t.Errorf("task detail = %q, want blocked #42 RED", got)
		}
	})

	t.Run("GH-11_pending_checks_arm_the_gate_timer_and_read_again", func(t *testing.T) {
		h := newHarness(t, gateConfig())
		f := &fakeForge{prs: []forge.PR{pendingPR(), greenPR()}, files: []string{codeFile}}
		h.gates = &fakeGates{forge: f}
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultDonePR, PR: prNumber})
		h.until(func() bool { _, ok := h.timers("a1")[gate.TimerWait]; return ok })
		if v := h.events("a1", model.EventGateVerdict); len(v) != 0 {
			t.Errorf("verdict %v while the checks run, want none", v)
		}
		h.advance(30 * time.Minute)
		h.until(h.hasEvent("a1", model.EventGateVerdict))
		if got := f.prReads(); got != 2 {
			t.Errorf("PR reads = %d, want 2", got)
		}
		if got := h.task("a1").State; got != model.TaskGating {
			t.Errorf("task state = %s, want %s", got, model.TaskGating)
		}
	})

	t.Run("GH-11_an_unreachable_forge_is_an_error_never_a_verdict", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.gates = &fakeGates{forge: &fakeForge{errs: []error{forge.ErrNetwork}}}
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultDonePR, PR: prNumber})
		for range gate.MaxReadErrors - 1 {
			h.until(func() bool { _, ok := h.timers("a1")[gate.TimerRetry]; return ok })
			h.advance(gate.RetryInterval)
		}
		h.until(h.taskIn("a1", model.TaskNeedsOwner))
		if got := h.lastDetail("a1"); !strings.HasPrefix(got, GateErrorDetail+"#42: ") {
			t.Errorf("task detail = %q, want %s#42", got, GateErrorDetail)
		}
		if v := h.events("a1", model.EventGateVerdict); len(v) != 0 {
			t.Errorf("verdict %v, want none", v)
		}
	})

	t.Run("GH-11_a_project_without_a_gate_reader_errors", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.gates = &fakeGates{err: errNoForge}
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultDonePR, PR: prNumber})
		h.until(h.taskIn("a1", model.TaskNeedsOwner))
		if got := h.lastDetail("a1"); !strings.Contains(got, errNoForge.Error()) {
			t.Errorf("task detail = %q, want the cause", got)
		}
	})

	t.Run("GH-11_no_gate_seam_leaves_the_PR_for_the_owner_unread", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.start()
		run := h.startAnalysis("a1")
		h.report(run, model.Result{Kind: model.ResultDonePR, PR: prNumber})
		h.until(h.hasEvent("a1", model.EventGateVerdict))
		if v := h.events("a1", model.EventGateVerdict)[0]; v != ReadyDetail+"#42: "+GateUnreadNote {
			t.Errorf("verdict = %q, want %q", v, ReadyDetail+"#42: "+GateUnreadNote)
		}
		if got := h.task("a1").State; got != model.TaskGating {
			t.Errorf("task state = %s, want %s", got, model.TaskGating)
		}
	})
}
