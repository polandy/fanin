package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const doctorToken = "ghp_0123456789abcdefghijklmnopqrstuv"

// doctorFake answers every program with the same output, or fails the ones in fail.
type doctorFake struct{ fail map[string]error }

func (f doctorFake) Run(_ context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	if err := f.fail[cmd.Argv[0]]; err != nil {
		return runtime.Result{}, err
	}
	switch cmd.Argv[0] {
	case "systemctl":
		return runtime.Result{Stdout: []byte("active\n")}, nil
	case "git":
		return runtime.Result{Stdout: []byte("git@github.com:acme/alpha.git\n")}, nil
	case "gh":
		if cmd.Argv[1] == "api" {
			return runtime.Result{Stdout: []byte(`["ci","fanin/review"]`)}, nil
		}
	}
	return runtime.Result{Stdout: []byte("ok")}, nil
}

func doctorConfig(t *testing.T) string {
	t.Helper()
	dir := writeConfig(t, map[string]string{
		"fanin.yaml":          "listen: 127.0.0.1:7420\nstore: fanin.db\nowner_token_env: FANIN_OWNER_TOKEN\n",
		"projects/alpha.yaml": "path: /r/alpha\nhost: local\nforge: github\nbase: main\nmax_parallel: 1\n",
		"worker-kinds.yaml":   "kinds:\n  claude:\n    runtime: herdr\n",
		"forges.yaml":         "forges:\n  github:\n    kind: github\n    token_env: GH_TOKEN\n",
	})
	st, err := store.Open(context.Background(), filepath.Join(dir, "fanin.db"), clock.NewManual(time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runDoctorCmd(t *testing.T, r runtime.Runner, global ...string) (int, string, string) {
	t.Helper()
	prev := doctorRunner
	doctorRunner = r
	t.Cleanup(func() { doctorRunner = prev })
	env := map[string]string{"GH_TOKEN": doctorToken, "FANIN_OWNER_TOKEN": "owner-secret"}
	return runArgs(t, defaultRegistry, env, append(global, "doctor")...)
}

func TestDoctor(t *testing.T) {
	t.Run("OB-3_healthy_setup_prints_an_ok_table_and_exits_0", func(t *testing.T) {
		code, out, errOut := runDoctorCmd(t, doctorFake{}, flagConfig, doctorConfig(t))
		if code != cli.ExitOK || errOut != "" {
			t.Fatalf("doctor = %d %q\n%s", code, errOut, out)
		}
		for _, w := range []string{"CHECK", "STATUS", "config", "store", "herdr", "forge-auth", "branch-protection", "systemd"} {
			if !strings.Contains(out, w) {
				t.Errorf("table lacks %q:\n%s", w, out)
			}
		}
		if strings.Contains(out, "FAIL") {
			t.Errorf("healthy table has a FAIL row:\n%s", out)
		}
	})

	t.Run("OB-3_missing_component_is_a_FAIL_row_with_exit_1_and_no_token_in_any_output", func(t *testing.T) {
		boom := errors.New("herdr not found; last token " + doctorToken)
		code, out, errOut := runDoctorCmd(t, doctorFake{fail: map[string]error{"herdr": boom}}, flagConfig, doctorConfig(t))
		if code != cli.ExitError {
			t.Fatalf("doctor = %d, want 1", code)
		}
		if !strings.Contains(out, "herdr") || !strings.Contains(out, "FAIL") {
			t.Errorf("table shows no failed herdr row:\n%s", out)
		}
		if strings.Contains(out+errOut, doctorToken) || strings.Contains(out+errOut, "owner-secret") {
			t.Errorf("a token leaked:\n%s\n%s", out, errOut)
		}
		if !strings.Contains(errOut, "1 of") {
			t.Errorf("stderr = %q, want the failure count", errOut)
		}
	})

	t.Run("OB-3_without_a_config_directory_is_a_usage_error", func(t *testing.T) {
		code, _, errOut := runDoctorCmd(t, doctorFake{})
		if code != cli.ExitUsage || !strings.Contains(errOut, envConfig) {
			t.Errorf("doctor = %d %q, want usage error naming %s", code, errOut, envConfig)
		}
	})
}
