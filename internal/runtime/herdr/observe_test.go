package herdr_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/herdr"
)

const (
	reply      = "⏺ Done: PR #12 is open, CI runs."
	bgTask     = footer + " · 1 background task"
	bgShell    = footer + " · 1 shell"
	approval   = "⏺ Bash(rm -rf build)\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)"
	askOwner   = "☐ Layout\n\nWhich layout should the inbox use?\n\n❯ 1. Cards\n     One card per question\n  2. Table\n     One row per question\n  3. Type something."
	numbered   = "⏺ I did three things:\n  1. Added the observer\n  2. Wrote the tests\n  3. Updated the spec"
	unsentText = "please continue with the red check"
)

var (
	idleScreen = screen(reply, "", footer)
	quietStall = herdr.DefaultStallSlice * herdr.DefaultStallSlices
)

// polls is how many poll intervals fit into d.
func polls(d time.Duration) int { return int(d / poll) }

// workingPane is a claude pane at output step n whose spinner shows the given seconds.
func workingPane(n, secs int) string {
	return screen(fmt.Sprintf("⏺ step %d\n\n✻ Thinking… (%ds · ↓ %d tokens · esc to interrupt)", n, secs, secs*10), "", footer)
}

func types(ts ...runtime.RunEventType) []runtime.RunEventType { return ts }

func TestObserveBackgroundFooter(t *testing.T) {
	t.Run("AW-3_live_footer_holds_the_wait_up_to_50_waits", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, screen(reply, "", bgTask))
		hold := polls(herdr.DefaultStallSlice) * herdr.DefaultMaxBGWaits
		o.tick(hold - 1)
		if len(o.got) != 0 {
			t.Fatalf("events while the footer is live = %v, want none", o.types())
		}
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventIdle), o.types()); diff != "" {
			t.Fatalf("events after %d waits (-want +got):\n%s", herdr.DefaultMaxBGWaits, diff)
		}
		if !o.got[0].BGFooter {
			t.Error("idle after the hold lacks BGFooter, want it set for the AW-11 ladder")
		}
	})

	t.Run("AW-3_later_status_is_picked_up", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, screen(reply, "", bgTask))
		o.tick(10)
		o.fake.set(herdr.StatusWorking, workingPane(1, 1))
		o.tick(1)
		o.fake.set(herdr.StatusIdle, idleScreen)
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventWorking, runtime.RunEventIdle), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
		if o.got[1].LastWords != reply {
			t.Errorf("last words = %q, want %q", o.got[1].LastWords, reply)
		}
		if o.got[1].BGFooter {
			t.Error("idle without a footer has BGFooter set")
		}
	})

	t.Run("AW-3_stale_footer_far_up_does_not_count", func(t *testing.T) {
		stale := screen("⏺ started a job\n"+bgTask+"\n"+strings.Repeat("⏺ more output\n", 20)+reply, "", footer)
		o := observe(t, herdr.StatusIdle, stale)
		if diff := cmp.Diff(types(runtime.RunEventIdle), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})
}

func TestObserveStall(t *testing.T) {
	t.Run("AW-4_unchanged_pane_for_3_slices_is_stalled_quiet", func(t *testing.T) {
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		for i := 1; i < polls(quietStall); i++ {
			o.fake.set(herdr.StatusWorking, workingPane(1, i*30))
			o.tick(1)
		}
		if diff := cmp.Diff(types(runtime.RunEventWorking), o.types()); diff != "" {
			t.Fatalf("events before the third slice ends (-want +got):\n%s", diff)
		}
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventWorking, runtime.RunEventStalled), o.types()); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
		if ev := o.last(); ev.Reason != runtime.StallQuiet || !ev.At.Equal(start.Add(quietStall)) {
			t.Errorf("stall = %q at %v, want %q at %v", ev.Reason, ev.At, runtime.StallQuiet, start.Add(quietStall))
		}
	})

	t.Run("AW-4_real_output_resets_the_count", func(t *testing.T) {
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		o.tick(20)
		o.fake.set(herdr.StatusWorking, workingPane(2, 600))
		o.tick(polls(quietStall))
		if diff := cmp.Diff(types(runtime.RunEventWorking), o.types()); diff != "" {
			t.Fatalf("events before the reset window ends (-want +got):\n%s", diff)
		}
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventWorking, runtime.RunEventStalled), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})

	t.Run("AW-5_unchanged_shell_tail_for_20_min_is_stalled_bg_shell", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, screen("⏺ Bash(make ci) running in the background", "", bgShell))
		o.tick(polls(herdr.DefaultBGShellWindow) - 1)
		if len(o.got) != 0 {
			t.Fatalf("events before 20 min = %v, want none", o.types())
		}
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventStalled), o.types()); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
		if ev := o.last(); ev.Reason != runtime.StallBGShell {
			t.Errorf("reason = %q, want %q", ev.Reason, runtime.StallBGShell)
		}
	})

	t.Run("AW-5_a_changed_tail_restarts_the_window", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, screen("⏺ Bash(make ci)\n  ok internal/a", "", bgShell))
		o.tick(30)
		o.fake.set(herdr.StatusIdle, screen("⏺ Bash(make ci)\n  ok internal/a\n  ok internal/b", "", bgShell))
		o.tick(polls(herdr.DefaultBGShellWindow))
		if len(o.got) != 0 {
			t.Fatalf("events within the restarted window = %v, want none", o.types())
		}
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventStalled), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})
}

func TestObserveReadFailures(t *testing.T) {
	t.Run("AW-6_status_read_retried_3_times_then_unreachable", func(t *testing.T) {
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		o.fake.do(func(p *paneFake) { p.down = 4 })
		o.drain()
		if diff := cmp.Diff(types(runtime.RunEventWorking, runtime.RunEventUnreachable), o.types()); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]time.Duration{poll, retry, retry, retry}, o.waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
		err := o.last().Err
		if !isUnreachable(err) || !errors.Is(err, runtime.ErrStatusUnknown) {
			t.Errorf("err = %v, want ErrUnreachable wrapping ErrStatusUnknown", err)
		}
		mustContain(t, err, agent, pane, "connection refused")
	})

	t.Run("AW-6_pane_read_recovers_on_the_third_retry", func(t *testing.T) {
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		o.fake.set(herdr.StatusWorking, workingPane(2, 30))
		o.fake.do(func(p *paneFake) { p.readFails = 3 })
		o.tick(4)
		o.fake.set(herdr.StatusWorking, workingPane(3, 150))
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventWorking), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]time.Duration{poll, retry, retry, retry, poll, poll}, o.waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("AW-6_pane_read_failing_4_times_is_unreachable", func(t *testing.T) {
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		o.fake.do(func(p *paneFake) { p.readFails = 4 })
		o.drain()
		if diff := cmp.Diff(types(runtime.RunEventWorking, runtime.RunEventUnreachable), o.types()); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
		mustContain(t, o.last().Err, pane, "pane read timed out")
	})

	t.Run("AW-6_hiccup_between_two_samples_is_not_settled", func(t *testing.T) {
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		for i := 1; i <= polls(quietStall)+5; i++ {
			o.fake.set(herdr.StatusWorking, workingPane(i, i*30))
			if i == polls(quietStall)/2 {
				o.fake.do(func(p *paneFake) { p.readFails, p.emptyReads = 1, 1 })
				o.tick(3)
				continue
			}
			o.tick(1)
		}
		if diff := cmp.Diff(types(runtime.RunEventWorking), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
		if !slices.Contains(o.waits, retry) {
			t.Error("the hiccup was not re-read")
		}
	})

	t.Run("AW-7_empty_idle_pane_is_read_again", func(t *testing.T) {
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		o.fake.set(herdr.StatusIdle, idleScreen)
		o.fake.do(func(p *paneFake) { p.emptyReads = 1 })
		o.tick(2)
		if diff := cmp.Diff(types(runtime.RunEventWorking, runtime.RunEventIdle), o.types()); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]time.Duration{poll, retry, poll}, o.waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
		if o.last().LastWords != reply {
			t.Errorf("last words = %q, want %q", o.last().LastWords, reply)
		}
	})

	t.Run("AW-7_gone_agent_has_no_retry", func(t *testing.T) {
		o := observe(t, herdr.StatusGone, idleScreen)
		if diff := cmp.Diff(types(runtime.RunEventGone), o.types()); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
		if !o.closed || len(o.waits) != 0 || o.fake.reads != 0 {
			t.Errorf("closed %v, waits %v, pane reads %d; want closed, no wait, no read", o.closed, o.waits, o.fake.reads)
		}
	})
}

func TestObserveResume(t *testing.T) {
	t.Run("AW-8_unsent_nudge_gets_Enter_at_most_twice_within_the_grace", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, screen(reply, unsentText, footer))
		grace := polls(herdr.DefaultResumeGrace)
		o.tick(grace - 1)
		if len(o.got) != 0 || o.fake.enters != herdr.DefaultResumeEnters {
			t.Fatalf("events %v and %d Enters within the grace, want none and %d", o.types(), o.fake.enters, herdr.DefaultResumeEnters)
		}
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventIdle), o.types()); diff != "" {
			t.Fatalf("events (-want +got):\n%s", diff)
		}
		if at := o.last().At; !at.Equal(start.Add(herdr.DefaultResumeGrace)) {
			t.Errorf("idle at %v, want after the %v grace", at, herdr.DefaultResumeGrace)
		}
		if o.fake.enters != herdr.DefaultResumeEnters {
			t.Errorf("Enters = %d, want %d", o.fake.enters, herdr.DefaultResumeEnters)
		}
	})

	t.Run("AW-8_submitted_nudge_starts_work_without_answered_in_pane", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, screen(reply, unsentText, footer), func(p *paneFake) {
			p.onEnter = func(p *paneFake) { p.status, p.text = herdr.StatusWorking, workingPane(1, 0) }
		})
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventWorking), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
		if o.fake.enters != 1 {
			t.Errorf("Enters = %d, want 1", o.fake.enters)
		}
	})
}

func TestObserveDialog(t *testing.T) {
	cases := []struct {
		name, text, want string
		opts             []string
	}{
		{"AW-9_approval_dialog", screen(approval, "", footer),
			"Do you want to proceed? 1. Yes / 2. No, and tell Claude what to do differently (esc)",
			[]string{"Yes", "No, and tell Claude what to do differently (esc)"}},
		{"AW-9_ask_user_question", askOwner + "\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n",
			"Which layout should the inbox use? 1. Cards / 2. Table / 3. Type something.",
			[]string{"Cards", "Table", "Type something."}},
		{"AW-9_unreadable_without_text", screen(border, "", footer), runtime.DialogUnreadable, nil},
		{"AW-9_numbered_list_in_a_blocked_pane_is_no_dialog", screen(numbered, "", footer), runtime.DialogUnreadable,
			nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := observe(t, herdr.StatusBlocked, c.text)
			if diff := cmp.Diff(types(runtime.RunEventDialog), o.types()); diff != "" {
				t.Fatalf("events (-want +got):\n%s", diff)
			}
			if got := o.last().Dialog; got != c.want {
				t.Errorf("dialog = %q, want %q", got, c.want)
			}
			if diff := cmp.Diff(c.opts, o.last().Options); diff != "" {
				t.Errorf("options (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("AW-9_numbered_list_in_a_normal_reply_is_no_dialog", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, screen(numbered, "", footer))
		if diff := cmp.Diff(types(runtime.RunEventIdle), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})

	t.Run("AW-9_same_dialog_is_reported_once", func(t *testing.T) {
		o := observe(t, herdr.StatusBlocked, screen(approval, "", footer))
		o.tick(5)
		if diff := cmp.Diff(types(runtime.RunEventDialog), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})
}

func TestObserveLastWords(t *testing.T) {
	t.Run("AW-12_last_words_drop_input_box_and_footer_and_tail_is_the_run_pane", func(t *testing.T) {
		body := strings.Repeat("⏺ earlier output\n", 10) + "⏺ I could not push:\n  the remote refused the branch."
		o := observe(t, herdr.StatusWorking, workingPane(1, 0))
		o.fake.set(herdr.StatusDone, screen(body, "", footer))
		o.tick(1)
		ev := o.last()
		if ev.Type != runtime.RunEventIdle {
			t.Fatalf("events = %v, want working, idle", o.types())
		}
		want := "⏺ earlier output\n⏺ earlier output\n⏺ earlier output\n⏺ I could not push:\n  the remote refused the branch."
		if ev.LastWords != want {
			t.Errorf("last words = %q, want %q", ev.LastWords, want)
		}
		if ev.Tail != screen(body, "", footer) || ev.Status != herdr.StatusDone {
			t.Errorf("tail %q status %q, want the pane read and done", ev.Tail, ev.Status)
		}
	})
}

func TestObserveAnsweredInPane(t *testing.T) {
	t.Run("WA-3_dialog_answered_in_the_pane", func(t *testing.T) {
		o := observe(t, herdr.StatusBlocked, screen(approval, "", footer))
		o.fake.set(herdr.StatusWorking, workingPane(1, 0))
		o.tick(1)
		want := types(runtime.RunEventDialog, runtime.RunEventAnsweredInPane, runtime.RunEventWorking)
		if diff := cmp.Diff(want, o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})

	t.Run("WA-3_idle_worker_answered_in_the_pane", func(t *testing.T) {
		o := observe(t, herdr.StatusIdle, idleScreen)
		o.fake.set(herdr.StatusWorking, workingPane(1, 0))
		o.tick(1)
		want := types(runtime.RunEventIdle, runtime.RunEventAnsweredInPane, runtime.RunEventWorking)
		if diff := cmp.Diff(want, o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})

	t.Run("WA-3_answer_through_fanin_is_not_in_pane", func(t *testing.T) {
		o := observe(t, herdr.StatusBlocked, screen(approval, "", footer))
		if err := o.rt.Prompt(context.Background(), want, "1"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		o.fake.set(herdr.StatusWorking, workingPane(1, 0))
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventDialog, runtime.RunEventWorking), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})

	t.Run("WA-3_OA-1_key_press_through_fanin_is_not_in_pane", func(t *testing.T) {
		o := observe(t, herdr.StatusBlocked, screen(approval, "", footer))
		if err := o.rt.Press(context.Background(), want, "1"); err != nil {
			t.Fatalf("Press: %v", err)
		}
		o.fake.set(herdr.StatusWorking, workingPane(1, 0))
		o.tick(1)
		if diff := cmp.Diff(types(runtime.RunEventDialog, runtime.RunEventWorking), o.types()); diff != "" {
			t.Errorf("events (-want +got):\n%s", diff)
		}
	})
}

func TestObserveNeedsAPane(t *testing.T) {
	f := newFixture(t)
	h := want
	h.Pane = ""
	if _, err := f.rt.Observe(context.Background(), h); !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("err = %v, want ErrInvalidSpec", err)
	}
	if n := len(f.runner.Calls()); n != 0 {
		t.Errorf("herdr called %d times, want 0", n)
	}
}
