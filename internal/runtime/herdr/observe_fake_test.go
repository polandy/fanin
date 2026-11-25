package herdr_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/herdr"
)

const (
	poll   = herdr.DefaultPollInterval
	retry  = herdr.DefaultReadRetryInterval
	border = "────────────────────────────────────────"
	footer = "  ⏵⏵ auto mode on (shift+tab to cycle)"
)

// screen is a claude pane: body, an input box holding input, and the footer line below it.
func screen(body, input, foot string) string {
	return body + "\n\n" + border + "\n❯ " + input + "\n" + border + "\n" + foot + "\n"
}

// paneFake answers agent get, agent list, pane read, prompt and send-keys from state the test changes between polls.
type paneFake struct {
	mu     sync.Mutex
	status string
	text   string
	// down fails that many agent get calls, each with its agent list; readFails and emptyReads hit pane reads.
	down       int
	listDown   bool
	readFails  int
	emptyReads int
	reads      int
	enters     int
	prompts    int
	// onEnter runs after each Enter, under the lock.
	onEnter func(p *paneFake)
}

func (p *paneFake) set(status, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.text = status, text
}

func (p *paneFake) do(f func(p *paneFake)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func fail(argv []string, why string) error {
	return &runtime.ExitError{Argv: argv, Code: 1, StderrTail: why}
}

func (p *paneFake) Run(_ context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := cmd.Argv[1:]
	switch strings.Join(a[:2], " ") {
	case "agent get":
		if p.listDown = p.down > 0; p.listDown {
			p.down--
			return runtime.Result{}, fail(cmd.Argv, "connection refused")
		}
		if p.status == herdr.StatusGone {
			return runtime.Result{}, fail(cmd.Argv, "no such agent")
		}
		return out(fmt.Sprintf(`{"result":{"agent":{"agent_status":%q,"pane_id":%q}}}`, p.status, pane)), nil
	case "agent list":
		if p.listDown {
			return runtime.Result{}, fail(cmd.Argv, "connection refused")
		}
		return out(noAgents), nil
	case "pane read":
		p.reads++
		if p.readFails > 0 {
			p.readFails--
			return runtime.Result{}, fail(cmd.Argv, "pane read timed out")
		}
		if p.emptyReads > 0 {
			p.emptyReads--
			return out(""), nil
		}
		n, err := strconv.Atoi(a[4])
		if err != nil {
			return runtime.Result{}, fmt.Errorf("bad --lines %q: %w", a[4], err)
		}
		lines := strings.SplitAfter(p.text, "\n")
		return out(strings.Join(lines[max(0, len(lines)-n):], "")), nil
	case "agent prompt":
		p.prompts++
		return out(""), nil
	case "agent send-keys":
		p.enters++
		if p.onEnter != nil {
			p.onEnter(p)
		}
		return out(""), nil
	}
	return runtime.Result{}, fmt.Errorf("unscripted call %q", cmd.Argv)
}

func out(s string) runtime.Result { return runtime.Result{Stdout: []byte(s)} }

// obs drives one Observe: it serves the observer's waits and collects its events.
type obs struct {
	t      *testing.T
	fake   *paneFake
	clk    *signalClock
	rt     *herdr.Runtime
	ch     <-chan runtime.RunEvent
	cancel context.CancelFunc
	got    []runtime.RunEvent
	waits  []time.Duration
	closed bool
}

// observe starts Observe on a pane in status showing text; setup runs on the fake before the first probe.
func observe(t *testing.T, status, text string, setup ...func(p *paneFake)) *obs {
	t.Helper()
	fake := &paneFake{status: status, text: text}
	for _, f := range setup {
		f(fake)
	}
	clk := &signalClock{Manual: clock.NewManual(start), waits: make(chan time.Duration)}
	rt := herdr.New(fake, clk, logger, herdr.DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := rt.Observe(ctx, want)
	if err != nil {
		cancel()
		t.Fatalf("Observe: %v", err)
	}
	o := &obs{t: t, fake: fake, clk: clk, rt: rt, ch: ch, cancel: cancel}
	t.Cleanup(o.stop)
	o.settle()
	return o
}

// settle collects events until the observer asks for its next wait; false once the channel is closed.
func (o *obs) settle() bool {
	for {
		select {
		case d := <-o.clk.waits:
			o.waits = append(o.waits, d)
			return true
		case ev, open := <-o.ch:
			if !open {
				o.closed = true
				return false
			}
			o.got = append(o.got, ev)
		}
	}
}

// tick lets the pending wait pass n times; each time the observer settles again.
func (o *obs) tick(n int) {
	o.t.Helper()
	for range n {
		o.clk.Advance(o.waits[len(o.waits)-1])
		if !o.settle() {
			o.t.Fatalf("channel closed early; events %v", o.types())
		}
	}
}

// drain lets every wait pass until the observer closes its channel.
func (o *obs) drain() {
	for !o.closed {
		o.clk.Advance(o.waits[len(o.waits)-1])
		o.settle()
	}
}

func (o *obs) stop() {
	o.cancel()
	for !o.closed {
		o.settle()
	}
}

func (o *obs) types() []runtime.RunEventType {
	ts := make([]runtime.RunEventType, len(o.got))
	for i, ev := range o.got {
		ts[i] = ev.Type
	}
	return ts
}

func (o *obs) last() runtime.RunEvent {
	o.t.Helper()
	if len(o.got) == 0 {
		o.t.Fatal("no event")
	}
	return o.got[len(o.got)-1]
}

func isUnreachable(err error) bool { return errors.Is(err, runtime.ErrUnreachable) }
