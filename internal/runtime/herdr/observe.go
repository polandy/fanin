package herdr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
)

// The observe limits (see SCENARIOS AW-3 to AW-8).
const (
	DefaultPollInterval      = 30 * time.Second
	DefaultReadRetries       = 3
	DefaultReadRetryInterval = 30 * time.Second
	DefaultStallSlice        = 300 * time.Second
	DefaultStallSlices       = 3
	DefaultBGShellWindow     = 20 * time.Minute
	DefaultMaxBGWaits        = 50
	DefaultResumeEnters      = 2
	DefaultResumeGrace       = 120 * time.Second
	// PaneLines is how much of the pane each probe reads; it is the tail an idle or stalled event carries.
	PaneLines = 200
)

var errEmptyPane = errors.New("herdr: pane reads empty")

// phase is what the observer last saw the agent do.
type phase int

const (
	phaseNone phase = iota
	phaseWorking
	phaseBG
	phaseIdle
	phaseBlocked
)

// observation is one Observe's view of a run; only its own goroutine touches it.
type observation struct {
	r       *Runtime
	h       runtime.RunHandle
	ch      chan<- runtime.RunEvent
	started time.Time
	status  string
	phase   phase
	since   time.Time
	// waitSince is when the agent last began waiting for input, idle or in a dialog.
	waitSince time.Time
	fp        string
	changed   time.Time
	dialog    string
	enters    int
}

// Observe polls herdr and the pane and streams the run's events until ctx ends, the agent is gone or herdr
// stays unreadable (see docs/dev/spec/runtime-herdr.md, Observe).
func (r *Runtime) Observe(ctx context.Context, h runtime.RunHandle) (<-chan runtime.RunEvent, error) {
	if h.Agent == "" || h.Pane == "" {
		return nil, fmt.Errorf("%w: RunHandle needs Agent and Pane to observe, got agent %q, pane %q",
			runtime.ErrInvalidSpec, h.Agent, h.Pane)
	}
	ch := make(chan runtime.RunEvent)
	o := &observation{r: r, h: h, ch: ch, started: r.clock.Now()}
	safego.Go(ctx, r.logger, func(ctx context.Context) {
		defer close(ch)
		o.run(ctx)
	})
	return ch, nil
}

func (o *observation) run(ctx context.Context) {
	for {
		done, err := o.probe(ctx)
		if err != nil {
			if ctx.Err() == nil {
				o.emit(ctx, runtime.RunEvent{Type: runtime.RunEventUnreachable, Err: err})
			}
			return
		}
		if done || o.r.wait(ctx, o.r.cfg.PollInterval) != nil {
			return
		}
	}
}

// probe reads the status and the pane once; true when the agent is gone.
func (o *observation) probe(ctx context.Context) (bool, error) {
	st, err := retry(ctx, o, "agent get", o.agentStatus)
	if err != nil {
		return false, err
	}
	o.status = st
	if st == StatusGone {
		o.emit(ctx, runtime.RunEvent{Type: runtime.RunEventGone})
		return true, nil
	}
	text, err := retry(ctx, o, "pane read", o.paneText)
	if err != nil {
		return false, err
	}
	switch st {
	case StatusBlocked:
		o.blocked(ctx, text)
	case StatusWorking:
		o.working(ctx, text)
	default:
		return false, o.idle(ctx, text)
	}
	return false, nil
}

func (o *observation) agentStatus(ctx context.Context) (string, error) {
	st, err := o.r.agentState(ctx, o.h.Host, o.h.Agent)
	if err != nil {
		return "", err
	}
	switch st.Status {
	case StatusWorking, StatusBlocked, StatusIdle, StatusDone, StatusGone:
		return st.Status, nil
	}
	return "", fmt.Errorf("%w: herdr calls %s %q; want working, blocked, idle or done", runtime.ErrOddAnswer, o.h.Agent, st.Status)
}

func (o *observation) paneText(ctx context.Context) (string, error) {
	text, err := o.r.readPane(ctx, o.h.Host, o.h.Pane, PaneLines)
	if err == nil && strings.TrimSpace(text) == "" {
		err = fmt.Errorf("%w: pane %s", errEmptyPane, o.h.Pane)
	}
	return text, err
}

// retry reads up to ReadRetries more times, ReadRetryInterval apart; a failed read never counts as a sample (AW-6, AW-7).
func retry[T any](ctx context.Context, o *observation, what string, read func(context.Context) (T, error)) (T, error) {
	for attempt := 0; ; attempt++ {
		v, err := read(ctx)
		if err == nil {
			return v, nil
		}
		if attempt >= o.r.cfg.ReadRetries {
			return v, fmt.Errorf("%w: herdr %s for %s (pane %s) failed %d times; check herdr on the worker host: %w",
				runtime.ErrUnreachable, what, o.h.Agent, o.h.Pane, attempt+1, err)
		}
		o.r.logger.WarnContext(ctx, "herdr read failed, retrying", "task_id", o.h.TaskID, "run_id", o.h.RunID,
			"project", o.h.Project, "agent", o.h.Agent, "pane", o.h.Pane, "read", what, "attempt", attempt+1, "error", err)
		if werr := o.r.wait(ctx, o.r.cfg.ReadRetryInterval); werr != nil {
			return v, werr
		}
	}
}

// move enters p; leaving a dialog, or an idle wait for work, with no fanin input since is answered_in_pane (WA-3).
func (o *observation) move(ctx context.Context, p phase, text string) bool {
	if o.phase == p {
		return false
	}
	answered := o.phase == phaseBlocked || (o.phase == phaseIdle && p == phaseWorking)
	if answered && !o.r.promptedSince(o.h, o.waitSince) {
		o.emit(ctx, runtime.RunEvent{Type: runtime.RunEventAnsweredInPane})
	}
	now := o.r.clock.Now()
	o.phase, o.since = p, now
	o.fp, o.changed = fingerprint(text), now
	if p == phaseIdle || p == phaseBlocked {
		o.waitSince = now
	}
	return true
}

func (o *observation) working(ctx context.Context, text string) {
	if o.move(ctx, phaseWorking, text) {
		o.emit(ctx, runtime.RunEvent{Type: runtime.RunEventWorking})
		return
	}
	o.stall(ctx, text, o.r.cfg.StallSlice*time.Duration(o.r.cfg.StallSlices), runtime.StallQuiet)
}

// stall reports a pane whose fingerprint stayed the same for window, then starts the next window.
func (o *observation) stall(ctx context.Context, text string, window time.Duration, reason runtime.StallReason) {
	now := o.r.clock.Now()
	if fp := fingerprint(text); fp != o.fp {
		o.fp, o.changed = fp, now
		return
	}
	if now.Sub(o.changed) < window {
		return
	}
	o.changed = now
	o.emit(ctx, runtime.RunEvent{Type: runtime.RunEventStalled, Reason: reason, LastWords: lastWords(text), Tail: text})
}

func (o *observation) idle(ctx context.Context, text string) error {
	now := o.r.clock.Now()
	if now.Sub(o.started) < o.r.cfg.ResumeGrace && (o.enters > 0 || unsentInput(text)) {
		if unsentInput(text) && o.enters < o.r.cfg.ResumeEnters {
			o.enters++
			return o.r.enter(ctx, o.h)
		}
		return nil
	}
	kind := liveFooter(text)
	bgHold := o.r.cfg.StallSlice * time.Duration(o.r.cfg.MaxBGWaits)
	if kind != footerNone && o.phase != phaseIdle && (o.phase != phaseBG || now.Sub(o.since) < bgHold) {
		if !o.move(ctx, phaseBG, text) && kind == footerShell {
			o.stall(ctx, text, o.r.cfg.BGShellWindow, runtime.StallBGShell)
		}
		return nil
	}
	if o.move(ctx, phaseIdle, text) {
		o.emit(ctx, runtime.RunEvent{Type: runtime.RunEventIdle, LastWords: lastWords(text), Tail: text,
			BGFooter: kind != footerNone})
	}
	return nil
}

func (o *observation) blocked(ctx context.Context, text string) {
	d, ok := parseDialog(text)
	if !ok {
		d = runtime.DialogUnreadable
	}
	if !o.move(ctx, phaseBlocked, text) && d == o.dialog {
		return
	}
	o.dialog = d
	o.emit(ctx, runtime.RunEvent{Type: runtime.RunEventDialog, Dialog: d})
}

func (o *observation) emit(ctx context.Context, ev runtime.RunEvent) {
	ev.Status, ev.At = o.status, o.r.clock.Now()
	select {
	case o.ch <- ev:
	case <-ctx.Done():
	}
}
