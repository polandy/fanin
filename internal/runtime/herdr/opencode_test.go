package herdr_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/herdr"
	"github.com/polandy/fanin/internal/runtime/testutil"
)

const (
	shimDir   = "/path/to/oc-bin"
	ocEmpty   = "┃                \n┃  Ask anything...\n"
	ocWorking = `{"result":{"agent":{"agent_status":"working","pane_id":"w1:p9"}}}`
)

var ocSpec = runtime.RunSpec{
	Project: project, Worktree: worktree, Agent: agent, Title: title, Prompt: prompt,
	Kind: runtime.KindOpencode, PathDir: shimDir,
}

func TestStartOpencode(t *testing.T) {
	t.Run("SP-9_puts_the_shim_on_PATH_and_starts_opencode_with_the_sandbox_label", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(hd("pane", "run", pane, "set -gx PATH "+shimDir+" $PATH"), testutil.Reply{})
		f.runner.Expect(hd("agent", "start", agent, "--pane", pane, "--timeout", "120000", "--kind", "opencode",
			"--", project), testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: idle})
		f.runner.Expect(inputArgv, testutil.Reply{Stdout: ocEmpty})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: ocWorking})

		h, waits, err := f.start(t, ocSpec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if diff := cmp.Diff(want, h); diff != "" {
			t.Errorf("handle (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]time.Duration{shellPause, check, check}, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("SP-9_an_unknown_kind_is_refused_before_any_herdr_call", func(t *testing.T) {
		f := newFixture(t)
		s := ocSpec
		s.Kind = "codex"
		_, _, err := f.start(t, s)
		if !errors.Is(err, runtime.ErrInvalidSpec) {
			t.Fatalf("err = %v, want ErrInvalidSpec", err)
		}
		mustContain(t, err, "codex", herdr.AgentKind, runtime.KindOpencode)
	})

	t.Run("SP-9_a_claude_spec_still_needs_its_MCP_config", func(t *testing.T) {
		f := newFixture(t)
		s := spec
		s.MCPConfig = ""
		if _, _, err := f.start(t, s); !errors.Is(err, runtime.ErrInvalidSpec) {
			t.Fatalf("err = %v, want ErrInvalidSpec", err)
		}
	})
}
