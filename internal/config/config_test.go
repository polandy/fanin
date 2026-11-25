package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const (
	faninYAML = `listen: 127.0.0.1:7420
store: data/fanin.db
log:
  format: text
owner_token_env: FANIN_OWNER_TOKEN
defaults:
  review_skill: /team-review
  budgets:
    fix_rounds_per_run: 4
`
	hostsYAML = `hosts:
  host-a:
    address: host-a.example.com
`
	forgesYAML = `forges:
  github:
    kind: github
    token_env: GH_TOKEN
  gitea:
    kind: gitea
    url: https://git.example.com
    token_env: GITEA_TOKEN
`
	kindsYAML = `kinds:
  claude:
    runtime: herdr
    models: [haiku, sonnet, opus]
    default_model: sonnet
  opencode:
    runtime: herdr
  headless:
    runtime: headless
    models: [haiku, sonnet]
    default_model: haiku
picker:
  kind: headless
  model: haiku
`
	alphaYAML = `path: /path/to/alpha
host: local
forge: github
base: trunk
fallback: opencode
max_parallel: 2
budgets:
  fix_rounds_per_run: 1
`
	betaYAML = `path: /path/to/beta
host: host-a
forge: gitea
worker_kind: opencode
max_parallel: 1
model: sonnet
`
)

func baseTree() map[string]string {
	return map[string]string{
		fileFanin: faninYAML, fileHosts: hostsYAML, fileForges: forgesYAML, fileWorkerKinds: kindsYAML,
		fileTriggers: "", "projects/alpha.yaml": alphaYAML, "projects/beta.yaml": betaYAML,
	}
}

// writeTree writes files (relative path to body) into a fresh config directory.
func writeTree(t *testing.T, files map[string]string) string {
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

// with returns the base tree with edits applied.
func with(edits map[string]string) map[string]string {
	files := baseTree()
	for k, v := range edits {
		files[k] = v
	}
	return files
}

func mustLoad(t *testing.T, files map[string]string) (Config, string) {
	t.Helper()
	dir := writeTree(t, files)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg, dir
}

// TestLoad covers SCENARIOS ST-4: the project file gives path, host, forge, base and fallback; local is this host;
// a project value wins over the daemon default, which wins over the schema default.
func TestLoad(t *testing.T) {
	cfg, dir := mustLoad(t, baseTree())
	if cfg.Listen != "127.0.0.1:7420" || cfg.Store != filepath.Join(dir, "data", "fanin.db") ||
		cfg.Log != (Log{Format: LogText, Level: LevelInfo}) || cfg.OwnerTokenEnv != "FANIN_OWNER_TOKEN" {
		t.Errorf("daemon keys = %+v", cfg)
	}
	alpha, ok := cfg.Project("alpha")
	if !ok {
		t.Fatal("project alpha missing")
	}
	beta, _ := cfg.Project("beta")
	t.Run("ST-4_project_registry_keys", func(t *testing.T) {
		got := [...]string{alpha.Path, alpha.Host, alpha.Forge, alpha.Base, alpha.Fallback}
		want := [...]string{"/path/to/alpha", HostLocal, "github", "trunk", "opencode"}
		if got != want {
			t.Errorf("alpha = %v, want %v", got, want)
		}
	})
	t.Run("ST-4_local_is_this_host", func(t *testing.T) {
		if h := cfg.HostOf(alpha); !h.Local || h.Name != HostLocal {
			t.Errorf("HostOf(alpha) = %+v, want this host", h)
		}
		if h := cfg.HostOf(beta); h.Local || h.Address != "host-a.example.com" {
			t.Errorf("HostOf(beta) = %+v, want host-a", h)
		}
	})
	t.Run("ST-4_project_value_overrides_daemon_default", func(t *testing.T) {
		if alpha.Budgets.FixRoundsPerRun != 1 {
			t.Errorf("fix_rounds_per_run = %d, want the project's 1", alpha.Budgets.FixRoundsPerRun)
		}
		if alpha.ReviewSkill != "/team-review" {
			t.Errorf("review_skill = %q, want the daemon default", alpha.ReviewSkill)
		}
		if beta.Budgets.FixRoundsPerRun != 4 {
			t.Errorf("beta fix_rounds_per_run = %d, want the daemon default 4", beta.Budgets.FixRoundsPerRun)
		}
	})
	t.Run("ST-4_schema_defaults_fill_the_rest", func(t *testing.T) {
		want := Budgets{FixRoundsPerRun: 4, RolloverPct: 75, MaxTokensPerRun: 3000000, MaxRunsPerTask: 3}
		if diff := cmp.Diff(want, beta.Budgets); diff != "" {
			t.Errorf("budgets (-want +got):\n%s", diff)
		}
		if beta.Base != "main" || beta.OCMerge != OCMergeAuto || beta.StallAfter != Duration(15*time.Minute) ||
			beta.WorkerTimeout != Duration(45*time.Minute) || beta.ReviewSkill != "/team-review" {
			t.Errorf("beta defaults = %+v", beta)
		}
		gh := GitHubGate{WatchTimeout: Duration(30 * time.Minute), Settle: Duration(20 * time.Second), MaxWaits: 12,
			MaxWatchFailures: 6}
		gt := GiteaGate{PollInterval: Duration(time.Minute), NociAfterPolls: 5, MaxPolls: 40}
		if diff := cmp.Diff(Gate{GitHub: gh, Gitea: gt}, beta.Gate); diff != "" {
			t.Errorf("gate (-want +got):\n%s", diff)
		}
	})
	t.Run("ST-1_projects_in_name_order", func(t *testing.T) {
		if names := cfg.ProjectNames(); !cmp.Equal(names, []string{"alpha", "beta"}) {
			t.Errorf("ProjectNames = %v", names)
		}
	})
}

func TestLoadOptionalFilesMayBeMissing(t *testing.T) {
	cfg, _ := mustLoad(t, map[string]string{fileFanin: faninYAML})
	if len(cfg.Projects) != 0 || cfg.Log.Level != LevelInfo {
		t.Errorf("cfg = %+v, want no projects and defaults", cfg)
	}
}

// TestLoadRefusesABadConfig covers SCENARIOS ST-4 and LN-2: a bad config refuses to start and names the file,
// the path and the rule.
func TestLoadRefusesABadConfig(t *testing.T) {
	alpha := "projects/alpha.yaml"
	for _, tc := range []struct {
		name  string
		edits map[string]string
		file  string
		want  []string
	}{
		{"ST-4_unknown_host", map[string]string{alpha: strings.Replace(alphaYAML, "host: local", "host: nowhere", 1)},
			alpha, []string{"/host", `"nowhere"`, ruleHostKnown}},
		{"ST-4_unknown_forge", map[string]string{alpha: strings.Replace(alphaYAML, "forge: github", "forge: none", 1)},
			alpha, []string{"/forge", `"none"`, ruleForgeKnown}},
		{"ST-4_unknown_fallback_kind", map[string]string{alpha: strings.Replace(alphaYAML, "opencode", "nope", 1)},
			alpha, []string{"/fallback", `"nope"`, ruleKindKnown}},
		{"ST-4_model_the_kind_does_not_take", map[string]string{alpha: alphaYAML + "model: gpt\n"},
			alpha, []string{"/model", `"gpt"`, ruleModelKnown}},
		{"ST-4_picker_model_unknown", map[string]string{fileWorkerKinds: strings.Replace(kindsYAML, "  model: haiku",
			"  model: opus", 1)}, fileWorkerKinds, []string{"/picker/model", `"opus"`, ruleModelKnown}},
		{"ST-4_unknown_key", map[string]string{alpha: alphaYAML + "colour: blue\n"}, alpha, []string{"colour"}},
		{"ST-4_unknown_nested_key", map[string]string{fileFanin: strings.Replace(faninYAML, "format: text",
			"format: text\n  colour: blue", 1)}, fileFanin, []string{"/log", "colour"}},
		{"ST-4_unknown_defaults_key", map[string]string{fileFanin: faninYAML + "  colour: blue\n"}, fileFanin,
			[]string{"/defaults", "colour"}},
		{"ST-4_unknown_file", map[string]string{"extra.yaml": "a: 1\n"}, "extra.yaml", []string{fileFanin}},
		{"ST-4_gitea_without_url", map[string]string{fileForges: strings.Replace(forgesYAML,
			"    url: https://git.example.com\n", "", 1)}, fileForges, []string{"/forges/gitea", "url"}},
		{"ST-4_secret_value_instead_of_name", map[string]string{fileForges: strings.Replace(forgesYAML, "GH_TOKEN",
			"ghp_abc123", 1)}, fileForges, []string{"/forges/github/token_env"}},
		{"ST-4_local_in_hosts", map[string]string{fileHosts: hostsYAML + "  local:\n    address: x\n"}, fileHosts,
			[]string{"/hosts"}},
		{"ST-4_relative_path", map[string]string{alpha: strings.Replace(alphaYAML, "/path/to/alpha", "alpha", 1)},
			alpha, []string{"/path"}},
		{"ST-4_bad_duration", map[string]string{alpha: alphaYAML + "stall_after: soon\n"}, alpha,
			[]string{"/stall_after", "soon"}},
		{"ST-4_bad_project_file_name", map[string]string{"projects/Alpha Two.yaml": alphaYAML}, "Alpha Two.yaml",
			[]string{"name"}},
		{"ST-4_not_yaml", map[string]string{fileFanin: "listen: ["}, fileFanin, []string{"yaml"}},
		{"ST-4_missing_store", map[string]string{fileFanin: strings.Replace(faninYAML, "store: data/fanin.db\n", "",
			1)}, fileFanin, []string{"store"}},
		{"LN-2_max_parallel_zero", map[string]string{alpha: strings.Replace(alphaYAML, "max_parallel: 2",
			"max_parallel: 0", 1)}, alpha, []string{"/max_parallel", "minimum"}},
		{"LN-2_max_parallel_missing", map[string]string{alpha: strings.Replace(alphaYAML, "max_parallel: 2\n", "",
			1)}, alpha, []string{"max_parallel", "required"}},
		{"LN-2_max_parallel_not_a_default", map[string]string{fileFanin: faninYAML + "  max_parallel: 3\n"},
			fileFanin, []string{"/defaults", "max_parallel"}},
		{"LN-2_retired_lanes", map[string]string{alpha: alphaYAML + "lanes: [a, b]\n"}, alpha,
			[]string{"/lanes", "retired", "max_parallel"}},
		{"ST-4_retired_machine", map[string]string{alpha: alphaYAML + "machine: host-a\n"}, alpha,
			[]string{"/machine", "retired", "host"}},
		{"ST-4_retired_default", map[string]string{fileFanin: faninYAML + "  lanes: [a]\n"}, fileFanin,
			[]string{"/defaults/lanes", "retired", "max_parallel"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, with(tc.edits))
			_, err := Load(dir)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load: %v, want ErrInvalid", err)
			}
			for _, s := range append([]string{tc.file}, tc.want...) {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not name %q", err, s)
				}
			}
		})
	}
}

func TestLoadUnreadableIsErrRead(t *testing.T) {
	for name, dir := range map[string]func(t *testing.T) string{
		"missing dir":   func(t *testing.T) string { return filepath.Join(t.TempDir(), "none") },
		"missing fanin": func(t *testing.T) string { return writeTree(t, map[string]string{fileHosts: hostsYAML}) },
		"a file": func(t *testing.T) string {
			return filepath.Join(writeTree(t, map[string]string{fileFanin: faninYAML}), fileFanin)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(dir(t)); !errors.Is(err, ErrRead) {
				t.Fatalf("Load: %v, want ErrRead", err)
			}
		})
	}
}

func TestLoadRelativeDirGivesAnAbsoluteStore(t *testing.T) {
	dir := writeTree(t, map[string]string{fileFanin: faninYAML})
	t.Chdir(filepath.Dir(dir))
	got, err := Load(filepath.Base(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(dir, "data", "fanin.db"); got.Store != want {
		t.Errorf("Store = %q, want %q", got.Store, want)
	}
}

func TestExamplesLoad(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "config", "examples")); err != nil {
		t.Fatalf("config/examples: %v", err)
	}
}
