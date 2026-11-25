package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/polandy/fanin/internal/cli"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	examplesDir = "../../config/examples"
	dirMarker   = "<config>"
	secretValue = "fake-secret-value-42"
	flagConfig  = "--config"
	subValidate = "validate"
	cmdConfig   = "config"
)

func writeConfig(t *testing.T, files map[string]string) string {
	t.Helper()
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
	return dir
}

func effective(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	return runArgs(t, defaultRegistry, nil, append([]string{flagConfig, dir, cmdConfig, "effective"}, args...)...)
}

// TestConfigEffective covers SCENARIOS ST-4: the effective config shows which value wins and where it is set.
func TestConfigEffective(t *testing.T) {
	t.Run("ST-4_project_value_overrides_fanin_yaml_default", func(t *testing.T) {
		dir := writeConfig(t, map[string]string{
			"fanin.yaml": "listen: 127.0.0.1:7420\nstore: fanin.db\nowner_token_env: FANIN_OWNER_TOKEN\n" +
				"defaults:\n  review_skill: /from-defaults\n  base: trunk\n",
			"projects/own.yaml":   "path: /path/to/repo\nhost: local\nforge: github\nmax_parallel: 1\nreview_skill: /from-project\n",
			"projects/other.yaml": "path: /path/to/repo\nhost: local\nforge: github\nmax_parallel: 1\n",
			"worker-kinds.yaml":   "kinds:\n  claude:\n    runtime: herdr\n",
			"forges.yaml":         "forges:\n  github:\n    kind: github\n    token_env: GH_TOKEN\n",
		})
		for project, want := range map[string][]string{
			"own": {"review_skill: /from-project # projects/own.yaml:5", "base: trunk # fanin.yaml:6",
				"max_parallel: 1 # projects/own.yaml:4", "worker_timeout: 45m0s # default"},
			"other": {"review_skill: /from-defaults # fanin.yaml:5"},
		} {
			code, stdout, stderr := effective(t, dir, project)
			if code != cli.ExitOK || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q", project, code, stderr)
			}
			for _, w := range want {
				if !strings.Contains(stdout, w) {
					t.Errorf("%s: output lacks %q:\n%s", project, w, stdout)
				}
			}
		}
	})

	t.Run("GT-10_a_secret_value_is_never_printed", func(t *testing.T) {
		t.Setenv("GH_TOKEN", secretValue)
		code, stdout, _ := effective(t, examplesDir, "alpha")
		if code != cli.ExitOK || strings.Contains(stdout, secretValue) ||
			!strings.Contains(stdout, "set: true # environment") {
			t.Fatalf("code=%d, want the token marked set without its value:\n%s", code, stdout)
		}
	})

	t.Run("ST-4_unknown_project_is_a_usage_error", func(t *testing.T) {
		code, _, stderr := effective(t, examplesDir, "nope")
		if code != cli.ExitUsage || !strings.Contains(stderr, `"nope"`) || !strings.Contains(stderr, "alpha, beta") {
			t.Fatalf("code=%d stderr=%q, want exit 2 naming the project and the known ones", code, stderr)
		}
	})

	t.Run("ST-4_invalid_config_names_file_path_and_rule", func(t *testing.T) {
		dir := writeConfig(t, map[string]string{
			"fanin.yaml":        "listen: 127.0.0.1:7420\nstore: fanin.db\nowner_token_env: FANIN_OWNER_TOKEN\n",
			"projects/bad.yaml": "path: /path/to/repo\nhost: local\nforge: github\nmax_parallel: 0\n",
		})
		for _, args := range [][]string{
			{flagConfig, dir, cmdConfig, "effective", "bad"},
			{cmdConfig, subValidate, dir},
		} {
			code, _, stderr := runArgs(t, defaultRegistry, nil, args...)
			for _, w := range []string{filepath.Join(dir, "projects", "bad.yaml"), "/max_parallel", "rule"} {
				if code != cli.ExitError || !strings.Contains(stderr, w) {
					t.Errorf("%v: code=%d stderr=%q, want exit 1 with %q", args, code, stderr, w)
				}
			}
		}
	})

	t.Run("ST-4_validate_reports_ok", func(t *testing.T) {
		code, stdout, _ := runArgs(t, defaultRegistry, map[string]string{envConfig: examplesDir}, cmdConfig, subValidate)
		if code != cli.ExitOK || !strings.Contains(stdout, "ok (2 projects)") {
			t.Fatalf("code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("ST-4_usage_errors", func(t *testing.T) {
		for _, args := range [][]string{
			{cmdConfig}, {cmdConfig, "bogus"}, {cmdConfig, subValidate}, {cmdConfig, "effective", projAlpha},
			{flagConfig, examplesDir, cmdConfig, "effective"}, {flagConfig, examplesDir, cmdConfig, "effective", "-x", "alpha"},
			{cmdConfig, subValidate, "a", "b"},
		} {
			if code, _, _ := runArgs(t, defaultRegistry, nil, args...); code != cli.ExitUsage {
				t.Errorf("%v: code=%d, want %d", args, code, cli.ExitUsage)
			}
		}
	})

	for _, project := range []string{projAlpha, "beta"} {
		for _, format := range []string{"yaml", "json"} {
			t.Run("ST-4_golden_"+project+"_"+format, func(t *testing.T) {
				t.Setenv("GH_TOKEN", "")
				t.Setenv("GITEA_TOKEN", "")
				t.Setenv("FANIN_OWNER_TOKEN", "")
				var args []string
				if format == "json" {
					args = append(args, "--json")
				}
				abs, err := filepath.Abs(examplesDir)
				if err != nil {
					t.Fatal(err)
				}
				render := func() string {
					code, stdout, stderr := effective(t, examplesDir, append(args, project)...)
					if code != cli.ExitOK {
						t.Fatalf("code=%d stderr=%q", code, stderr)
					}
					return strings.ReplaceAll(stdout, abs, dirMarker)
				}
				first := render()
				if second := render(); second != first {
					t.Fatalf("two runs differ:\n%s", cmp.Diff(first, second))
				}
				golden := filepath.Join("testdata", "config-effective-"+project+"."+format+".golden")
				if *update {
					if err := os.WriteFile(golden, []byte(first), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("read golden (run go test ./cmd/fanin -update): %v", err)
				}
				if diff := cmp.Diff(string(want), first); diff != "" {
					t.Errorf("output differs from %s (-want +got):\n%s", golden, diff)
				}
			})
		}
	}
}
