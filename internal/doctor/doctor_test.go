package doctor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/doctor"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/store"
)

const (
	ghToken    = "ghp_0123456789abcdefghijklmnopqrstuv"
	giteaToken = "gitea-secret-token-value"
)

var errNotFound = errors.New("executable file not found in $PATH")

// fakeRunner answers by program and its first words; everything it was not told about is an error.
type fakeRunner struct {
	answers map[string]string
	fails   map[string]error
	seen    []runtime.Cmd
}

func (f *fakeRunner) Run(_ context.Context, cmd runtime.Cmd) (runtime.Result, error) {
	f.seen = append(f.seen, cmd)
	key := strings.Join(cmd.Argv, " ")
	if len(cmd.Argv) > 0 && cmd.Argv[0] == "curl" {
		key = "curl " + string(cmd.Stdin)
	}
	for prefix, err := range f.fails {
		if strings.HasPrefix(key, prefix) {
			return runtime.Result{}, err
		}
	}
	for prefix, out := range f.answers {
		if strings.HasPrefix(key, prefix) {
			return runtime.Result{Stdout: []byte(out)}, nil
		}
	}
	return runtime.Result{}, errNotFound
}

func healthy() *fakeRunner {
	return &fakeRunner{answers: map[string]string{
		"herdr workspace list":    "[]",
		"systemctl is-active":     "active\n",
		"git -C /r/alpha remote":  "git@github.com:acme/alpha.git\n",
		"git -C /r/beta remote":   "https://git.example.com/acme/beta.git\n",
		"gh auth status":          "Logged in\n",
		"gh api repos/acme/alpha": `["ci","fanin/review"]`,
		"curl url = \"https://git.example.com/api/v1/user\"":                            `{"login":"bot"}`,
		"curl url = \"https://git.example.com/api/v1/repos/acme/beta/branch_protections/main\"": `{"status_check_contexts":["ci","fanin/review"]}`,
	}}
}

func writeConfig(t *testing.T, mutate func(files map[string]string)) string {
	t.Helper()
	files := map[string]string{
		"fanin.yaml": "listen: 127.0.0.1:7420\nstore: fanin.db\nowner_token_env: FANIN_OWNER_TOKEN\n",
		"forges.yaml": "forges:\n  github:\n    kind: github\n    token_env: GH_TOKEN\n" +
			"  gitea:\n    kind: gitea\n    url: https://git.example.com\n    token_env: GITEA_TOKEN\n",
		"worker-kinds.yaml": "kinds:\n  claude:\n    runtime: herdr\n",
		"projects/alpha.yaml": "path: /r/alpha\nhost: local\nforge: github\nbase: main\nmax_parallel: 1\n",
		"projects/beta.yaml":  "path: /r/beta\nhost: local\nforge: gitea\nbase: main\nmax_parallel: 1\n",
	}
	if mutate != nil {
		mutate(files)
	}
	dir := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "fanin.db"), clock.NewManual(time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func env(name string) string {
	switch name {
	case "GH_TOKEN":
		return ghToken
	case "GITEA_TOKEN":
		return giteaToken
	}
	return ""
}

func run(t *testing.T, dir string, r runtime.Runner, getenv func(string) string) []doctor.Result {
	t.Helper()
	return doctor.Run(context.Background(), doctor.Deps{ConfigDir: dir, Runner: r, Getenv: getenv})
}

func find(t *testing.T, res []doctor.Result, check, subject string) doctor.Result {
	t.Helper()
	for _, r := range res {
		if r.Check == check && r.Subject == subject {
			return r
		}
	}
	t.Fatalf("no result for %s %q in %v", check, subject, res)
	return doctor.Result{}
}

func TestDoctor(t *testing.T) {
	t.Run("OB-3_a_healthy_setup_is_ok_in_every_row", func(t *testing.T) {
		res := run(t, writeConfig(t, nil), healthy(), env)
		for _, r := range res {
			if r.Status != doctor.OK {
				t.Errorf("%s %s = %s (%s), want ok", r.Check, r.Subject, r.Status, r.Detail)
			}
		}
		for _, c := range []struct{ check, subject string }{
			{doctor.CheckConfig, ""}, {doctor.CheckStore, ""}, {doctor.CheckHerdr, ""}, {doctor.CheckSystemd, ""},
			{doctor.CheckForgeAuth, "alpha"}, {doctor.CheckForgeAuth, "beta"},
			{doctor.CheckProtection, "alpha"}, {doctor.CheckProtection, "beta"},
		} {
			find(t, res, c.check, c.subject)
		}
		if doctor.Failed(res) != 0 {
			t.Errorf("Failed = %d, want 0", doctor.Failed(res))
		}
	})

	t.Run("OB-3_a_missing_component_is_an_error_never_ok", func(t *testing.T) {
		for _, tc := range []struct {
			name, check, subject string
			drop                 string
		}{
			{"herdr", doctor.CheckHerdr, "", "herdr workspace list"},
			{"systemd", doctor.CheckSystemd, "", "systemctl is-active"},
			{"github_auth", doctor.CheckForgeAuth, "alpha", "gh auth status"},
			{"gitea_auth", doctor.CheckForgeAuth, "beta", "curl url = \"https://git.example.com/api/v1/user\""},
			{"github_protection", doctor.CheckProtection, "alpha", "gh api repos/acme/alpha"},
			{"repo_unknown", doctor.CheckProtection, "beta", "git -C /r/beta remote"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := healthy()
				delete(r.answers, tc.drop)
				res := run(t, writeConfig(t, nil), r, env)
				got := find(t, res, tc.check, tc.subject)
				if got.Status != doctor.Fail || got.Detail == "" {
					t.Errorf("%s = %s %q, want fail with a cause", tc.name, got.Status, got.Detail)
				}
				if doctor.Failed(res) == 0 {
					t.Error("Failed = 0, want the missing component counted")
				}
			})
		}
	})

	t.Run("OB-3_unknown_systemd_state_is_an_error", func(t *testing.T) {
		r := healthy()
		r.answers["systemctl is-active"] = "inactive\n"
		got := find(t, run(t, writeConfig(t, nil), r, env), doctor.CheckSystemd, "")
		if got.Status != doctor.Fail || !strings.Contains(got.Detail, "inactive") {
			t.Errorf("systemd = %s %q, want fail naming the state", got.Status, got.Detail)
		}
	})

	t.Run("OB-3_branch_protection_without_the_review_status_is_an_error", func(t *testing.T) {
		r := healthy()
		r.answers["gh api repos/acme/alpha"] = `["ci"]`
		got := find(t, run(t, writeConfig(t, nil), r, env), doctor.CheckProtection, "alpha")
		if got.Status != doctor.Fail || !strings.Contains(got.Detail, "fanin/review") {
			t.Errorf("protection = %s %q, want fail naming fanin/review", got.Status, got.Detail)
		}
	})

	t.Run("OB-3_unreadable_protection_answer_is_an_error_not_a_verdict", func(t *testing.T) {
		r := healthy()
		r.answers["gh api repos/acme/alpha"] = "not json"
		got := find(t, run(t, writeConfig(t, nil), r, env), doctor.CheckProtection, "alpha")
		if got.Status != doctor.Fail {
			t.Errorf("protection = %s, want fail", got.Status)
		}
	})

	t.Run("OB-3_empty_forge_token_is_an_error", func(t *testing.T) {
		noToken := func(name string) string {
			if name == "GITEA_TOKEN" {
				return ""
			}
			return env(name)
		}
		got := find(t, run(t, writeConfig(t, nil), healthy(), noToken), doctor.CheckForgeAuth, "beta")
		if got.Status != doctor.Fail || !strings.Contains(got.Detail, "GITEA_TOKEN") {
			t.Errorf("forge auth = %s %q, want fail naming GITEA_TOKEN", got.Status, got.Detail)
		}
	})

	t.Run("OB-3_invalid_config_fails_and_no_other_row_is_ok", func(t *testing.T) {
		dir := writeConfig(t, func(f map[string]string) { f["projects/alpha.yaml"] = "path: /r/alpha\nbogus: 1\n" })
		res := run(t, dir, healthy(), env)
		if got := find(t, res, doctor.CheckConfig, ""); got.Status != doctor.Fail {
			t.Fatalf("config = %s, want fail", got.Status)
		}
		for _, r := range res {
			if r.Status == doctor.OK {
				t.Errorf("%s %s is ok after an invalid config", r.Check, r.Subject)
			}
		}
	})

	t.Run("OB-3_missing_store_file_is_an_error", func(t *testing.T) {
		dir := writeConfig(t, nil)
		if err := os.Remove(filepath.Join(dir, "fanin.db")); err != nil {
			t.Fatal(err)
		}
		got := find(t, run(t, dir, healthy(), env), doctor.CheckStore, "")
		if got.Status != doctor.Fail || !strings.Contains(got.Detail, "fanin.db") {
			t.Errorf("store = %s %q, want fail naming the file", got.Status, got.Detail)
		}
	})

	t.Run("OB-3_remote_host_project_is_skipped_with_a_reason_not_ok", func(t *testing.T) {
		dir := writeConfig(t, func(f map[string]string) {
			f["hosts.yaml"] = "hosts:\n  far:\n    address: far.example.com\n"
			f["projects/alpha.yaml"] = "path: /r/alpha\nhost: far\nforge: github\nbase: main\nmax_parallel: 1\n"
		})
		got := find(t, run(t, dir, healthy(), env), doctor.CheckProtection, "alpha")
		if got.Status != doctor.Skip || !strings.Contains(got.Detail, "far") {
			t.Errorf("protection = %s %q, want skip naming the host", got.Status, got.Detail)
		}
	})

	t.Run("GT-10_tokens_never_appear_in_any_result_or_command_line", func(t *testing.T) {
		r := healthy()
		// A tool that echoes the token in its error must not leak it through the table.
		r.fails = map[string]error{"gh auth status": errors.New("auth failed for " + ghToken + " Authorization: token " + giteaToken)}
		res := run(t, writeConfig(t, nil), r, env)
		for _, x := range res {
			for _, secret := range []string{ghToken, giteaToken} {
				if strings.Contains(x.Detail, secret) || strings.Contains(x.Subject, secret) {
					t.Errorf("%s %s leaks a token: %q", x.Check, x.Subject, x.Detail)
				}
			}
		}
		for _, c := range r.seen {
			for _, a := range c.Argv {
				if strings.Contains(a, giteaToken) {
					t.Errorf("token in argv %v; pass it on stdin", c.Argv)
				}
			}
		}
	})
}
