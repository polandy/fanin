// Package config loads and validates the fanin config directory (spec: docs/dev/spec/config.md).
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// The config sentinels.
var (
	// ErrRead is returned when the config directory or one of its files cannot be read.
	ErrRead = errors.New("config: unreadable")
	// ErrInvalid is returned for a config that breaks the schema or a rule beyond it.
	ErrInvalid = errors.New("config: invalid")
	// ErrUnknownProject is returned for a project name no project file defines.
	ErrUnknownProject = errors.New("config: unknown project")
	// ErrModel is returned when no model, or one the worker kind does not take, results for a worker.
	ErrModel = errors.New("config: bad model")
)

// Log formats.
const (
	LogJSON = "json"
	LogText = "text"
)

// Log levels.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Notify kinds.
const (
	NotifyLog    = "log"
	NotifySignal = "signal"
)

// DefaultSignalCLI is the signal-cli program when notify.signal_cli is unset.
const DefaultSignalCLI = "signal-cli"

// HostLocal is the built-in host name for the host fanind runs on.
const HostLocal = "local"

// Forge kinds.
const (
	ForgeGitHub = "github"
	ForgeGitea  = "gitea"
)

// Worker runtimes.
const (
	RuntimeHerdr    = "herdr"
	RuntimeHeadless = "headless"
)

// oc_merge values.
const (
	OCMergeAuto   = "auto"
	OCMergeManual = "manual"
)

// loop values: who drives a project's workers.
const (
	LoopBash  = "bash"
	LoopFanin = "fanin"
)

// Config is the loaded config directory.
type Config struct {
	// Dir is the absolute config directory.
	Dir string
	// Listen is the host:port of the HTTP API.
	Listen string
	// Store is the SQLite file, absolute after Load.
	Store string
	Log   Log
	// OwnerTokenEnv names the environment variable that holds the owner token; the token is never in the files.
	OwnerTokenEnv string
	// Notify is the push channel for owner notes.
	Notify Notify
	// Hosts are the remote hosts of hosts.yaml by name; local is not among them.
	Hosts map[string]Host
	// Forges are the forges of forges.yaml by name.
	Forges map[string]Forge
	// WorkerKinds are the kinds of worker-kinds.yaml by name.
	WorkerKinds map[string]WorkerKind
	// Picker selects the kind and model of the picker run.
	Picker Picker
	// Projects are the project files with defaults applied, sorted by name.
	Projects []Project
	// lines maps each file (relative to Dir) to the line of every key in it, for the effective config.
	lines map[string]map[string]int
}

// Log selects the log format and level.
type Log struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// Notify selects the push channel; SignalNumber is the owner's own number and never logged.
type Notify struct {
	Kind         string `yaml:"kind"`
	UIURL        string `yaml:"ui_url,omitempty"`
	Prefix       string `yaml:"prefix,omitempty"`
	SignalNumber string `yaml:"signal_number,omitempty"`
	SignalCLI    string `yaml:"signal_cli,omitempty"`
}

// Host is a worker host; Local marks this host.
type Host struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address,omitempty"`
	Local   bool   `yaml:"local"`
}

// Forge is a code forge; TokenEnv names the variable that holds its token.
type Forge struct {
	Kind     string `yaml:"kind"`
	URL      string `yaml:"url,omitempty"`
	TokenEnv string `yaml:"token_env"`
}

// WorkerKind is one way to run a worker; a kind without Models takes no model.
type WorkerKind struct {
	Runtime      string   `yaml:"runtime"`
	Models       []string `yaml:"models,omitempty"`
	DefaultModel string   `yaml:"default_model,omitempty"`
}

// Picker is the worker kind and model of the picker run.
type Picker struct {
	Kind  string `yaml:"kind"`
	Model string `yaml:"model"`
}

// Project is one project file with the daemon and schema defaults applied.
type Project struct {
	Name          string   `yaml:"-"`
	Path          string   `yaml:"path"`
	Host          string   `yaml:"host"`
	Forge         string   `yaml:"forge"`
	Base          string   `yaml:"base"`
	Fallback      string   `yaml:"fallback,omitempty"`
	WorkerKind    string   `yaml:"worker_kind"`
	Model         string   `yaml:"model,omitempty"`
	ReviewSkill   string   `yaml:"review_skill"`
	ReviewModel   string   `yaml:"review_model"`
	ReviewTimeout Duration `yaml:"review_timeout"`
	MaxParallel   int      `yaml:"max_parallel"`
	Budgets       Budgets  `yaml:"budgets"`
	Merge         Merge    `yaml:"merge"`
	StallAfter    Duration `yaml:"stall_after"`
	WorkerTimeout Duration `yaml:"worker_timeout"`
	Gate          Gate     `yaml:"gate"`
	OCMerge       string   `yaml:"oc_merge"`
	Loop          string   `yaml:"loop"`
	PostMergeHook string   `yaml:"post_merge_hook,omitempty"`
	PreGoCheck    string   `yaml:"pre_go_check,omitempty"`
}

// Budgets bound a run and a task (C-BUDGET).
type Budgets struct {
	FixRoundsPerRun int   `yaml:"fix_rounds_per_run"`
	RolloverPct     int   `yaml:"rollover_pct"`
	MaxTokensPerRun int64 `yaml:"max_tokens_per_run"`
	MaxRunsPerTask  int   `yaml:"max_runs_per_task"`
}

// Merge bounds the project's merge lock (SCENARIOS MG-2, MG-3).
type Merge struct {
	LockWait  Duration `yaml:"lock_wait"`
	LockLease Duration `yaml:"lock_lease"`
}

// Gate holds the wait limits of the gate per forge kind.
type Gate struct {
	GitHub GitHubGate `yaml:"github"`
	Gitea  GiteaGate  `yaml:"gitea"`
}

// GitHubGate are the pending-check limits of a GitHub gate (GH-7).
type GitHubGate struct {
	WatchTimeout     Duration `yaml:"watch_timeout"`
	Settle           Duration `yaml:"settle"`
	MaxWaits         int      `yaml:"max_waits"`
	MaxWatchFailures int      `yaml:"max_watch_failures"`
}

// GiteaGate are the poll limits of a Gitea gate (GT-4).
type GiteaGate struct {
	PollInterval   Duration `yaml:"poll_interval"`
	NociAfterPolls int      `yaml:"noci_after_polls"`
	MaxPolls       int      `yaml:"max_polls"`
}

// Load reads the config directory dir, validates every file against its embedded schema and the references
// between the files, and applies the defaults.
func Load(dir string) (Config, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %s: %w; pass the config directory with --config", ErrRead, dir, err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return Config{}, fmt.Errorf("%w: %s is not a readable directory; pass the config directory with --config",
			ErrRead, dir)
	}
	t, err := readTree(abs)
	if err != nil {
		return Config{}, err
	}
	cfg, err := t.build()
	if err != nil {
		return Config{}, err
	}
	if err := t.checkRefs(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ProjectNames returns the project names in config order.
func (c Config) ProjectNames() []string {
	out := make([]string, 0, len(c.Projects))
	for _, p := range c.Projects {
		out = append(out, p.Name)
	}
	return out
}

// Project returns the project called name.
func (c Config) Project(name string) (Project, bool) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

// HostOf returns the host p runs on; local is this host.
func (c Config) HostOf(p Project) Host {
	if p.Host == HostLocal {
		return Host{Name: HostLocal, Local: true}
	}
	return c.Hosts[p.Host]
}

// Logger returns the slog logger the Log section selects, writing to w.
func (c Config) Logger(w io.Writer) *slog.Logger {
	levels := map[string]slog.Level{LevelDebug: slog.LevelDebug, LevelInfo: slog.LevelInfo,
		LevelWarn: slog.LevelWarn, LevelError: slog.LevelError}
	opts := &slog.HandlerOptions{Level: levels[c.Log.Level]}
	if c.Log.Format == LogText {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
