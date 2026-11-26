package sched

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/model"
)

const (
	headFirst  = "abc123"
	headPushed = "def456"
)

var errReview = errors.New("review comment unreadable")

// fakeReviewer records the head of each review and answers from errs in turn; a review that succeeds makes the
// forge answer its entry of posts, as the posted status would. With hold set, the first review waits for it to close.
type fakeReviewer struct {
	mu    sync.Mutex
	f     *fakeForge
	heads []string
	errs  []error
	posts []forge.PR
	hold  chan struct{}
}

func (r *fakeReviewer) Review(ctx context.Context, _ config.Project, _ model.Run, pr int) (string, error) {
	head, err := r.f.PR(ctx, repoAlpha, pr)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	i := len(r.heads)
	r.heads = append(r.heads, head.HeadSHA)
	r.mu.Unlock()
	if r.hold != nil && i == 0 {
		select {
		case <-r.hold:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if i < len(r.errs) && r.errs[i] != nil {
		return "", r.errs[i]
	}
	if i < len(r.posts) {
		r.f.answer(r.posts[i])
	}
	return head.HeadSHA, nil
}

func (r *fakeReviewer) reviewed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.heads)
}

// noReviewPR has green CI at head and no fanin/review status.
func noReviewPR(head string) forge.PR {
	pr := greenPR()
	pr.HeadSHA, pr.Checks = head, pr.Checks[:1]
	return pr
}

func greenAt(head string) forge.PR {
	pr := greenPR()
	pr.HeadSHA = head
	return pr
}

// reviewRig is a merge rig whose forge answers first and whose reviewer posts posts in turn.
func reviewRig(t *testing.T, cfg config.Config, first forge.PR, posts ...forge.PR) (*mergeRig, *fakeReviewer) {
	t.Helper()
	r := prRig(t, cfg, first)
	rv := &fakeReviewer{f: r.f, posts: posts}
	r.reviewer = rv
	return r, rv
}

// see SCENARIOS OC-3, concept D5
func TestFaninReview(t *testing.T) {
	t.Run("OC-3_green_CI_without_a_review_runs_one_review_and_its_status_merges", func(t *testing.T) {
		r, rv := reviewRig(t, mergeConfig(), noReviewPR(headFirst), greenAt(headFirst))
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{headFirst}, rv.reviewed()); diff != "" {
			t.Errorf("reviewed heads (-want +got):\n%s", diff)
		}
		if n := r.events("a1", model.EventWorkerNudged); len(n) != 0 {
			t.Errorf("nudges = %q, want none", n)
		}
		if diff := cmp.Diff([]string{merged42, hooked42}, r.log.all()); diff != "" {
			t.Errorf("merges (-want +got):\n%s", diff)
		}
	})

	t.Run("OC-3_a_new_head_gets_a_new_review", func(t *testing.T) {
		r, rv := reviewRig(t, mergeConfig(), noReviewPR(headFirst), noReviewPR(headPushed), greenAt(headPushed))
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{headFirst, headPushed}, rv.reviewed()); diff != "" {
			t.Errorf("reviewed heads (-want +got):\n%s", diff)
		}
	})

	t.Run("OC-3_red_CI_runs_no_review_and_the_fixed_head_is_reviewed", func(t *testing.T) {
		red := noReviewPR(headFirst)
		red.Checks[0].State = forge.CheckFailure
		r, rv := reviewRig(t, mergeConfig(), red, greenAt(headPushed))
		r.start()
		run := r.startPR("a1", "")
		if text := r.redRound(run, 1); !strings.Contains(text, "failed: ci") {
			t.Errorf("nudge = %q, want the failed CI", text)
		}
		if got := rv.reviewed(); len(got) != 0 {
			t.Errorf("reviewed heads = %v, want none on red CI", got)
		}
		r.f.answer(noReviewPR(headPushed))
		r.report(run, donePR)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{headPushed}, rv.reviewed()); diff != "" {
			t.Errorf("reviewed heads (-want +got):\n%s", diff)
		}
	})

	t.Run("OC-3_a_failed_review_posts_nothing_is_logged_and_runs_again_on_the_next_tick", func(t *testing.T) {
		r, rv := reviewRig(t, mergeConfig(), noReviewPR(headFirst), forge.PR{}, greenAt(headFirst))
		rv.errs = []error{errReview}
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(func() bool { _, ok := r.timers("a1")[gate.TimerRetry]; return ok })
		r.quiet()
		if got := rv.reviewed(); len(got) != 1 {
			t.Errorf("reviewed heads = %v, want one before the tick", got)
		}
		if got := r.task("a1").State; got != model.TaskGating {
			t.Errorf("task state = %s, want %s", got, model.TaskGating)
		}
		if logs := r.logs.String(); !strings.Contains(logs, ReviewFailed) || !strings.Contains(logs, errReview.Error()) {
			t.Errorf("log lacks %q with %q:\n%s", ReviewFailed, errReview, logs)
		}
		if n := r.events("a1", model.EventWorkerNudged); len(n) != 0 {
			t.Errorf("nudges = %q, want none", n)
		}
		r.advance(gate.RetryInterval)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{headFirst, headFirst}, rv.reviewed()); diff != "" {
			t.Errorf("reviewed heads (-want +got):\n%s", diff)
		}
	})

	t.Run("OC-3_a_review_run_takes_no_worker_slot", func(t *testing.T) {
		cfg := mergeConfig()
		cfg.Projects[0].MaxParallel = 2
		r, rv := reviewRig(t, cfg, noReviewPR(headFirst), greenAt(headFirst))
		rv.hold = make(chan struct{})
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(func() bool { return len(rv.reviewed()) == 1 })
		second := r.startPR("a2", "")
		if second.Slot != 2 {
			t.Errorf("slot of a2 = %d, want 2 while a1 is reviewed", second.Slot)
		}
		close(rv.hold)
		r.until(r.taskIn("a1", model.TaskDone))
	})

	t.Run("OC-3_a_review_cut_short_by_a_restart_runs_again_after_the_tick", func(t *testing.T) {
		r, rv := reviewRig(t, mergeConfig(), noReviewPR(headFirst), forge.PR{}, greenAt(headFirst))
		rv.hold = make(chan struct{})
		defer close(rv.hold)
		r.start()
		run := r.startPR("a1", "")
		r.report(run, donePR)
		r.until(func() bool { return len(rv.reviewed()) == 1 })
		r.restart()
		r.advance(gate.RetryInterval)
		r.until(r.taskIn("a1", model.TaskDone))
		if diff := cmp.Diff([]string{headFirst, headFirst}, rv.reviewed()); diff != "" {
			t.Errorf("reviewed heads (-want +got):\n%s", diff)
		}
	})
}
