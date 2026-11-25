package sched

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/hook"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/runtime"
)

const (
	preGoCmd  = "scripts/dry-activate.sh"
	preGoOut  = "dry-activate: firewall: no port or firewall rule changes\nwould: restart nginx\n"
	preGoLine = "dry-activate: firewall: no port or firewall rule changes; would: restart nginx"
	preGoTree = stateDir + "/" + hook.PreGoDir + "/alpha-42"
)

// preGoRunner answers git with success and the check with out and err; a check waits for block to close.
type preGoRunner struct {
	mu    sync.Mutex
	calls [][]string
	out   string
	err   error
	in    chan struct{}
	block chan struct{}
}

func (f *preGoRunner) Run(ctx context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd.Argv)
	f.mu.Unlock()
	if cmd.Argv[0] != "sh" {
		return runtime.Result{}, nil
	}
	if f.block != nil {
		f.in <- struct{}{}
		select {
		case <-f.block:
		case <-ctx.Done():
			return runtime.Result{}, ctx.Err()
		}
	}
	if f.err != nil {
		return runtime.Result{}, &runtime.ExitError{Argv: cmd.Argv, Code: 1, StderrTail: f.err.Error()}
	}
	return runtime.Result{Stdout: []byte(f.out)}, nil
}

func (f *preGoRunner) argvs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.calls {
		out = append(out, strings.Join(a, " "))
	}
	return out
}

// preGoRig is a Gitea green-code PR rig whose project runs preGoCmd before the go.
func preGoRig(t *testing.T, fr *preGoRunner) *mergeRig {
	t.Helper()
	cfg := mergeConfig()
	cfg.Projects[0].PreGoCheck = preGoCmd
	cfg.Projects[0].Gate.Gitea = config.GiteaGate{PollInterval: config.Duration(time.Minute), NociAfterPolls: 1, MaxPolls: 2}
	r := prRig(t, cfg)
	r.f.files = []string{codeFile}
	r.gates = &fakeGates{forge: r.f, kind: forge.KindGitea}
	r.preGo = hook.NewPreGo(fr)
	return r
}

func TestPreGo(t *testing.T) {
	wantGit := []string{
		"git -C " + pathAlpha + " fetch --quiet origin main " + headBranch,
		"git -C " + pathAlpha + " worktree add --force --detach " + preGoTree + " FETCH_HEAD",
		"sh -c " + preGoCmd,
		"git -C " + pathAlpha + " worktree remove --force " + preGoTree,
	}
	t.Run("GT-8_the_ready_line_carries_the_check_output_and_the_worktree_is_removed", func(t *testing.T) {
		fr := &preGoRunner{out: preGoOut}
		r := preGoRig(t, fr)
		r.start()
		r.report(r.startPR("a1", ""), donePR)
		r.until(r.hasEvent("a1", model.EventGateVerdict))
		want := ReadyDetail + "#42: GREEN code " + prTitle + "; " + GateCodeNote + "; " + preGoLine
		if v := r.events("a1", model.EventGateVerdict); len(v) != 1 || v[0] != want {
			t.Errorf("verdicts = %q, want [%q]", v, want)
		}
		if diff := cmp.Diff(wantGit, fr.argvs()); diff != "" {
			t.Errorf("runner calls (-want +got):\n%s", diff)
		}
		r.noMerge()
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskDone))
	})
	t.Run("GT-8_FP-6_a_failing_check_asks_the_owner_with_its_error_and_never_merges", func(t *testing.T) {
		fr := &preGoRunner{err: errors.New("dry-activate: build failed")}
		r := preGoRig(t, fr)
		r.start()
		r.report(r.startPR("a1", ""), donePR)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		det := r.events("a1", model.EventTaskState)
		if last := det[len(det)-1]; !strings.Contains(last, PreGoFailedDetail+"#42") ||
			!strings.Contains(last, "dry-activate: build failed") {
			t.Errorf("question = %q, want the pre-go check error", last)
		}
		if got := fr.argvs(); got[len(got)-1] != wantGit[3] {
			t.Errorf("last call = %q, want the worktree removed", got[len(got)-1])
		}
		if v := r.events("a1", model.EventGateVerdict); len(v) != 0 {
			t.Errorf("verdicts = %q, want none", v)
		}
		r.noMerge()
	})
	t.Run("GT-8_FP-6_a_missing_runner_asks_the_owner_and_never_merges", func(t *testing.T) {
		r := preGoRig(t, &preGoRunner{})
		r.preGo = nil
		r.start()
		r.report(r.startPR("a1", ""), donePR)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		r.noMerge()
	})
	t.Run("GT-8_a_go_during_the_check_merges_only_after_it", func(t *testing.T) {
		fr := &preGoRunner{out: preGoOut, in: make(chan struct{}, 1), block: make(chan struct{})}
		r := preGoRig(t, fr)
		r.start()
		r.report(r.startPR("a1", ""), donePR)
		r.await(fr.in)
		r.goMerge("a1")
		r.noMerge()
		close(fr.block)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})
}
