package herdr_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/herdr"
	"github.com/polandy/fanin/internal/runtime/testutil"
	"github.com/polandy/fanin/internal/safego"
)

const (
	project    = "proj"
	remoteHost = "host-a"
	worktree   = "/path/to/proj-wt"
	agent      = "proj-3"
	title      = "Add the herdr runtime"
	prompt     = "do the task"
	mcpFile    = "/path/to/worker-mcp.json"
	ws         = "w1"
	tab        = "w1:t5"
	pane       = "w1:p9"
	session    = project + " · " + title + " · " + agent

	wsList     = `{"result":{"workspaces":[{"label":"other","workspace_id":"w0"},{"label":"proj","workspace_id":"w1"}]}}`
	wsNone     = `{"result":{"workspaces":[]}}`
	wsCreated  = `{"result":{"workspace":{"workspace_id":"w1"}}}`
	tabCreated = `{"result":{"tab":{"tab_id":"w1:t5"},"root_pane":{"pane_id":"w1:p9"}}}`
	working    = `{"result":{"agent":{"agent_status":"working","pane_id":"w1:p9"}}}`
	idle       = `{"result":{"agent":{"agent_status":"idle","pane_id":"w1:p9"}}}`
	noAgents   = `{"result":{"agents":[{"name":"proj-1"}]}}`
	plainShell = "~/path/to/proj-wt> \n"
)

var (
	start  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	spec   = runtime.RunSpec{
		Project: project, Worktree: worktree, Agent: agent, Title: title, Prompt: prompt, MCPConfig: mcpFile,
	}
	want = runtime.RunHandle{Agent: agent, Workspace: ws, Tab: tab, Pane: pane}

	startArgv = hd("agent", "start", agent, "--pane", pane, "--timeout", "120000", "--kind", "claude", "--",
		"--permission-mode", "auto", "--strict-mcp-config", "--mcp-config", mcpFile, "--name", session)
	getArgv    = hd("agent", "get", agent)
	listArgv   = hd("agent", "list")
	promptArgv = hd("agent", "prompt", agent, prompt)
	trustArgv  = hd("pane", "read", pane, "--lines", "30")
	inputArgv  = hd("pane", "read", pane, "--lines", "6")
	enterArgv  = hd("agent", "send-keys", agent, "enter")
	closeArgv  = hd("pane", "close", pane)
)

func hd(args ...string) []string { return append([]string{herdr.Program}, args...) }

// hdOn is hd on remoteHost.
func hdOn(args ...string) []string {
	return hd(append([]string{herdr.FlagMachine, remoteHost}, args...)...)
}

// signalClock is a Manual clock that reports every wait it is asked for.
type signalClock struct {
	*clock.Manual
	waits chan time.Duration
}

func (c *signalClock) After(d time.Duration) <-chan time.Time {
	ch := c.Manual.After(d)
	c.waits <- d
	return ch
}

type fixture struct {
	runner *testutil.RecordingRunner
	clock  *signalClock
	rt     *herdr.Runtime
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	r := testutil.NewRecordingRunner(t)
	clk := &signalClock{Manual: clock.NewManual(start), waits: make(chan time.Duration)}
	return &fixture{runner: r, clock: clk, rt: herdr.New(r, clk, logger, herdr.DefaultConfig())}
}

// setup scripts the found workspace and the new tab.
func (f *fixture) setup() {
	f.runner.Expect(hd("workspace", "list"), testutil.Reply{Stdout: wsList})
	f.runner.Expect(hd("tab", "create", "--workspace", ws, "--cwd", worktree, "--label", agent, "--no-focus"),
		testutil.Reply{Stdout: tabCreated})
}

// failedStart scripts one start try that fails with no agent listed and no dialog in the pane.
func (f *fixture) failedStart(stderr string) {
	f.runner.Expect(startArgv, testutil.Reply{Stderr: stderr, ExitCode: 1})
	f.runner.Expect(getArgv, testutil.Reply{Stderr: "no such agent", ExitCode: 1})
	f.runner.Expect(listArgv, testutil.Reply{Stdout: noAgents})
	f.runner.Expect(trustArgv, testutil.Reply{Stdout: plainShell})
}

type started struct {
	handle runtime.RunHandle
	err    error
}

// start runs Start, advancing the clock past every wait, and returns the handle, the waits in order and the error.
func (f *fixture) start(t *testing.T, s runtime.RunSpec) (runtime.RunHandle, []time.Duration, error) {
	t.Helper()
	done := make(chan started, 1)
	safego.Go(context.Background(), logger, func(ctx context.Context) {
		h, err := f.rt.Start(ctx, s)
		done <- started{h, err}
	})
	var waits []time.Duration
	for {
		select {
		case d := <-f.clock.waits:
			waits = append(waits, d)
			f.clock.Advance(d)
		case res := <-done:
			f.runner.Done()
			return res.handle, waits, res.err
		}
	}
}

func repeat(d time.Duration, n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = d
	}
	return out
}

func mustContain(t *testing.T, err error, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("error %v does not name %q", err, p)
		}
	}
}
