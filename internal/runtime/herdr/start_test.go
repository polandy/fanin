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
	shellPause = herdr.DefaultShellPause
	interval   = herdr.DefaultStartInterval
	check      = herdr.DefaultWorkCheckInterval
)

func TestStart(t *testing.T) {
	t.Run("SP-1_found_workspace_new_tab_start_prompt_working", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: working})

		h, waits, err := f.start(t, spec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if diff := cmp.Diff(want, h); diff != "" {
			t.Errorf("handle (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]time.Duration{shellPause, check}, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("SP-1_creates_the_workspace_on_the_host_with_model", func(t *testing.T) {
		f := newFixture(t)
		s := spec
		s.Host, s.Model, s.Title = remoteHost, "opus", ""
		m := hdOn
		f.runner.Expect(m("workspace", "list"), testutil.Reply{Stdout: wsNone})
		f.runner.Expect(m("workspace", "create", "--cwd", worktree, "--label", project, "--no-focus"),
			testutil.Reply{Stdout: wsCreated})
		f.runner.Expect(m("tab", "create", "--workspace", ws, "--cwd", worktree, "--label", agent, "--no-focus"),
			testutil.Reply{Stdout: tabCreated})
		f.runner.Expect(m("agent", "start", agent, "--pane", pane, "--timeout", "120000", "--kind", "claude", "--",
			"--permission-mode", "auto", "--strict-mcp-config", "--mcp-config", mcpFile, "--model", "opus",
			"--name", project+" · "+agent), testutil.Reply{})
		f.runner.Expect(m("agent", "prompt", agent, prompt), testutil.Reply{})
		f.runner.Expect(m("agent", "get", agent), testutil.Reply{Stdout: working})

		h, _, err := f.start(t, s)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if h.Host != remoteHost || h.Pane != pane {
			t.Errorf("handle = %+v", h)
		}
	})

	t.Run("SP-2_retries_8_times_at_120s_then_names_the_last_error_and_keeps_the_tab", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		for range herdr.DefaultStartTries - 1 {
			f.failedStart("timed out")
		}
		f.failedStart("last try timed out")

		_, waits, err := f.start(t, spec)
		if !errors.Is(err, runtime.ErrStartFailed) {
			t.Fatalf("err = %v, want ErrStartFailed", err)
		}
		mustContain(t, err, "last try timed out", "8 tries", "tab "+tab+" left open", pane)
		wantWaits := append([]time.Duration{shellPause}, repeat(interval, herdr.DefaultStartTries-1)...)
		if diff := cmp.Diff(wantWaits, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("AW-14_claude_gets_the_status_line_settings", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		s := spec
		s.Settings = "/run/r1/settings.json"
		f.runner.Expect(hd("agent", "start", agent, "--pane", pane, "--timeout", "120000", "--kind", "claude", "--",
			"--permission-mode", "auto", "--strict-mcp-config", "--mcp-config", mcpFile,
			"--settings", s.Settings, "--name", session), testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: working})

		if _, _, err := f.start(t, s); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})

	t.Run("SP-2_a_later_try_succeeds", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.failedStart("timed out")
		f.runner.Expect(startArgv, testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: working})

		_, waits, err := f.start(t, spec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if diff := cmp.Diff([]time.Duration{shellPause, interval, check}, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("SP-3_error_after_the_agent_was_created_is_no_second_start", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{Stderr: "empty api response", ExitCode: 1})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: idle})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: working})

		if _, _, err := f.start(t, spec); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})

	t.Run("SP-3_foreign_agent_of_that_name_gets_no_prompt", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{Stderr: "name taken", ExitCode: 1})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: `{"result":{"agent":{"agent_status":"working","pane_id":"w4:p1"}}}`})

		_, _, err := f.start(t, spec)
		if !errors.Is(err, runtime.ErrAgentTaken) {
			t.Fatalf("err = %v, want ErrAgentTaken", err)
		}
		mustContain(t, err, "w4:p1", pane, "name taken")
	})

	t.Run("SP-4_trust_dialog_ends_the_spawn_at_once_naming_the_pane", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{Stderr: "timed out", ExitCode: 1})
		f.runner.Expect(getArgv, testutil.Reply{Stderr: "no such agent", ExitCode: 1})
		f.runner.Expect(listArgv, testutil.Reply{Stdout: noAgents})
		f.runner.Expect(trustArgv, testutil.Reply{Stdout: " Do you trust this folder?\n ❯ 1. Yes, proceed\n"})

		_, waits, err := f.start(t, spec)
		if !errors.Is(err, runtime.ErrTrustDialog) {
			t.Fatalf("err = %v, want ErrTrustDialog", err)
		}
		mustContain(t, err, "pane "+pane, tab, worktree)
		if diff := cmp.Diff([]time.Duration{shellPause}, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("SP-5_refused_prompt_fails_with_reason_and_pane", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{Stderr: "agent not ready", ExitCode: 1})

		_, _, err := f.start(t, spec)
		if !errors.Is(err, runtime.ErrPromptRefused) {
			t.Fatalf("err = %v, want ErrPromptRefused", err)
		}
		mustContain(t, err, "agent not ready", pane)
	})

	odd := []struct {
		name    string
		script  func(f *fixture)
		mention string
	}{
		{"SP-5_odd_tab_answer_before_the_start", func(f *fixture) {
			f.runner.Expect(hd("workspace", "list"), testutil.Reply{Stdout: wsList})
			f.runner.Expect(hd("tab", "create", "--workspace", ws, "--cwd", worktree, "--label", agent, "--no-focus"),
				testutil.Reply{Stdout: `{"result":{"tab":{}}}`})
		}, `{"result":{"tab":{}}}`},
		{"SP-5_odd_workspace_list", func(f *fixture) {
			f.runner.Expect(hd("workspace", "list"), testutil.Reply{Stdout: "<html>"})
		}, "<html>"},
		{"SP-5_odd_workspace_create", func(f *fixture) {
			f.runner.Expect(hd("workspace", "list"), testutil.Reply{Stdout: wsNone})
			f.runner.Expect(hd("workspace", "create", "--cwd", worktree, "--label", project, "--no-focus"),
				testutil.Reply{Stdout: `{"result":{}}`})
		}, project},
	}
	for _, tc := range odd {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.script(f)
			_, waits, err := f.start(t, spec)
			if !errors.Is(err, runtime.ErrOddAnswer) {
				t.Fatalf("err = %v, want ErrOddAnswer", err)
			}
			mustContain(t, err, tc.mention)
			if len(waits) != 0 {
				t.Errorf("waited %v before failing", waits)
			}
		})
	}

	t.Run("SP-6_resends_on_an_empty_box_else_presses_enter", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: idle})
		f.runner.Expect(inputArgv, testutil.Reply{Stdout: "──────\n❯ \n──────\n"})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: idle})
		f.runner.Expect(inputArgv, testutil.Reply{Stdout: "──────\n❯ do the task\n──────\n"})
		f.runner.Expect(enterArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: `{"result":{"agent":{"agent_status":"blocked","pane_id":"w1:p9"}}}`})

		_, waits, err := f.start(t, spec)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if diff := cmp.Diff([]time.Duration{shellPause, check, check, check}, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("SP-6_gives_up_after_10_checks_at_3s", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{})
		for range herdr.DefaultWorkChecks - 1 {
			f.runner.Expect(getArgv, testutil.Reply{Stdout: idle})
			f.runner.Expect(inputArgv, testutil.Reply{Stdout: "❯ do the task\n"})
			f.runner.Expect(enterArgv, testutil.Reply{})
		}
		f.runner.Expect(getArgv, testutil.Reply{Stdout: idle})

		_, waits, err := f.start(t, spec)
		if !errors.Is(err, runtime.ErrNotWorking) {
			t.Fatalf("err = %v, want ErrNotWorking", err)
		}
		mustContain(t, err, "10 checks", "status idle", pane)
		wantWaits := append([]time.Duration{shellPause}, repeat(check, herdr.DefaultWorkChecks)...)
		if diff := cmp.Diff(wantWaits, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})

	t.Run("SP-11_unknown_status_after_a_failed_start", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{Stderr: "timed out", ExitCode: 1})
		f.runner.Expect(getArgv, testutil.Reply{Stderr: "server gone", ExitCode: 1})
		f.runner.Expect(listArgv, testutil.Reply{Stderr: "server gone", ExitCode: 1})

		_, _, err := f.start(t, spec)
		if !errors.Is(err, runtime.ErrStatusUnknown) {
			t.Fatalf("err = %v, want ErrStatusUnknown", err)
		}
		mustContain(t, err, "status of "+agent+" unknown", "no second worker started", "server gone")
	})

	t.Run("SP-11_listed_agent_without_a_readable_status", func(t *testing.T) {
		f := newFixture(t)
		f.setup()
		f.runner.Expect(startArgv, testutil.Reply{})
		f.runner.Expect(promptArgv, testutil.Reply{})
		f.runner.Expect(getArgv, testutil.Reply{Stdout: `{"result":{"agent":{}}}`})
		f.runner.Expect(listArgv, testutil.Reply{Stdout: `{"result":{"agents":[{"name":"proj-3"}]}}`})

		_, _, err := f.start(t, spec)
		if !errors.Is(err, runtime.ErrStatusUnknown) || !errors.Is(err, runtime.ErrOddAnswer) {
			t.Fatalf("err = %v, want ErrStatusUnknown wrapping ErrOddAnswer", err)
		}
	})

	t.Run("invalid_spec_calls_no_herdr", func(t *testing.T) {
		f := newFixture(t)
		s := spec
		s.MCPConfig = ""
		_, _, err := f.start(t, s)
		if !errors.Is(err, runtime.ErrInvalidSpec) {
			t.Fatalf("err = %v, want ErrInvalidSpec", err)
		}
		mustContain(t, err, "RunSpec.MCPConfig")
	})
}

func TestSessionName(t *testing.T) {
	t.Run("SP-1_label_title_agent", func(t *testing.T) {
		if got := spec.SessionName(); got != session {
			t.Errorf("SessionName = %q, want %q", got, session)
		}
		s := spec
		s.Title = ""
		if got, want := s.SessionName(), project+" · "+agent; got != want {
			t.Errorf("SessionName without title = %q, want %q", got, want)
		}
	})
}
