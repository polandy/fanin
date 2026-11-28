package sched

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/scope"
)

var errScope = errors.New("files unreadable")

// scopeCall is one fanin/scope post the scheduler asked for.
type scopeCall struct {
	PR      int
	Head    string
	Allowed []string
}

// fakeScopes records each post and answers state, or err.
type fakeScopes struct {
	mu    sync.Mutex
	calls []scopeCall
	state forge.StatusState
	err   error
}

func (f *fakeScopes) PostScope(_ context.Context, _ config.Project, pr int, head string,
	allowed []string) (forge.StatusState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, scopeCall{PR: pr, Head: head, Allowed: allowed})
	return f.state, f.err
}

func (f *fakeScopes) posted() []scopeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scopeCall(nil), f.calls...)
}

// startScoped adds a PR owner task with allowed and waits until its first run works.
func (r *mergeRig) startScoped(id model.TaskID, allowed []string) model.Run {
	r.t.Helper()
	task := ownerTask(id)
	task.AllowedPaths = allowed
	r.addTask(task)
	r.until(r.runIn(RunID(id, 1), model.RunWorking))
	return r.run(RunID(id, 1))
}

// see SCENARIOS SC-2
func TestScope(t *testing.T) {
	allowed := []string{"internal/scope/"}

	t.Run("SC-2_a_reported_head_gets_fanin_scope_before_the_gate_and_success_merges", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		sc := &fakeScopes{state: forge.StatusSuccess}
		r.scopes = sc
		r.start()
		run := r.startScoped("a1", allowed)
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		want := []scopeCall{{PR: prNumber, Head: greenPR().HeadSHA, Allowed: allowed}}
		if diff := cmp.Diff(want, sc.posted()); diff != "" {
			t.Errorf("scope posts (-want +got):\n%s", diff)
		}
	})

	t.Run("SC-2_a_task_without_a_scope_still_gets_the_status", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		sc := &fakeScopes{state: forge.StatusSuccess}
		r.scopes = sc
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]scopeCall{{PR: prNumber, Head: greenPR().HeadSHA}}, sc.posted()); diff != "" {
			t.Errorf("scope posts (-want +got):\n%s", diff)
		}
	})

	t.Run("SC-2_a_file_outside_the_scope_is_a_CI_red_on_a_green_head", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		r.scopes = &fakeScopes{state: forge.StatusFailure}
		r.start()
		run := r.startScoped("a1", allowed)
		r.report(run, donePR)
		if got := r.nudgedWith(run, 1); !strings.Contains(got, scope.Context) {
			t.Errorf("nudge = %q, want it to name %s", got, scope.Context)
		}
		if m := r.log.all(); len(m) != 0 {
			t.Errorf("merges = %q, want none", m)
		}
	})

	t.Run("SC-2_a_scope_that_cannot_be_posted_asks_the_owner_and_never_gates", func(t *testing.T) {
		r := prRig(t, mergeConfig())
		r.scopes = &fakeScopes{err: errScope}
		r.start()
		run := r.startScoped("a1", allowed)
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskNeedsOwner))
		if got := r.lastDetail("a1"); !strings.HasPrefix(got, GateErrorDetail+"#42: ") ||
			!strings.Contains(got, errScope.Error()) {
			t.Errorf("task detail = %q, want %s#42 with the scope error", got, GateErrorDetail)
		}
		if v := r.events("a1", model.EventGateVerdict); len(v) != 0 {
			t.Errorf("verdicts %v, want none", v)
		}
		if m := r.log.all(); len(m) != 0 {
			t.Errorf("merges = %q, want none", m)
		}
	})
}
