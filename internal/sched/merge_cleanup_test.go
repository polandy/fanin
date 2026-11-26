package sched

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
)

var errCleanup = errors.New("worktree is dirty")

// fakeCleaner records each cleanup and fails it with err.
type fakeCleaner struct {
	mu   sync.Mutex
	prs  []string
	stop *fakeRuntime
	// paneAtCleanup is the stopped-agent count seen at the first cleanup.
	paneAtCleanup int
	err           error
}

func (f *fakeCleaner) Cleanup(_ context.Context, p config.Project, pr int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prs) == 0 && f.stop != nil {
		f.paneAtCleanup = len(f.stop.stops())
	}
	f.prs = append(f.prs, p.Name+"#"+strconv.Itoa(pr))
	return f.err
}

func (f *fakeCleaner) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prs)
}

func TestMergeCleanup(t *testing.T) {
	cleaned := func(c *fakeCleaner) func() bool { return func() bool { return len(c.calls()) > 0 } }

	t.Run("MG-7_a_merge_cleans_up_after_its_pane_is_closed", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		c := &fakeCleaner{stop: r.rt}
		r.cleaner = c
		r.start()
		run := r.ready("a1", prNumber)
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskDone))
		r.until(cleaned(c))
		if got := c.calls(); len(got) != 1 || got[0] != projAlpha+"#42" {
			t.Errorf("cleanups = %v, want [%s#42]", got, projAlpha)
		}
		if !r.stopped(run.Worker.Agent)() || c.paneAtCleanup != 1 {
			t.Errorf("stops at cleanup = %d, want the pane closed first", c.paneAtCleanup)
		}
	})

	t.Run("MG-7_a_failed_cleanup_is_logged_and_the_merge_record_stands", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		c := &fakeCleaner{err: errCleanup}
		r.cleaner = c
		r.start()
		run := r.ready("a1", prNumber)
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskDone))
		r.until(func() bool { return strings.Contains(r.logs.String(), CleanupFailed) })
		if got := r.lastDetail("a1"); got != MergedDetail+"#42" {
			t.Errorf("task detail = %q, want %q", got, MergedDetail+"#42")
		}
		if got := r.run(run.ID).State; got != model.RunSucceeded {
			t.Errorf("run state = %s, want %s", got, model.RunSucceeded)
		}
		if !strings.Contains(r.logs.String(), errCleanup.Error()) {
			t.Errorf("log lacks the cleanup error %q:\n%s", errCleanup, r.logs.String())
		}
	})

	t.Run("MG-7_a_pane_already_closed_does_not_skip_the_cleanup", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		c := &fakeCleaner{}
		r.cleaner = c
		r.rt.stopErr = errors.New("pane not found")
		r.start()
		r.ready("a1", prNumber)
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskDone))
		r.until(cleaned(c))
		r.until(func() bool { return strings.Contains(r.logs.String(), closeByHand) })
	})

	t.Run("MG-7_no_cleaner_merges_as_before", func(t *testing.T) {
		r := newMergeRig(t, mergeConfig())
		r.start()
		r.ready("a1", prNumber)
		r.goMerge("a1")
		r.until(r.taskIn("a1", model.TaskDone))
	})
}
