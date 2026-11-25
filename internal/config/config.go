// Package config loads and validates fanin.yaml (concept: Configuration; spec in docs/dev/spec/config.md).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// The config sentinels.
var (
	// ErrRead is returned when the config file cannot be read.
	ErrRead = errors.New("config: unreadable")
	// ErrInvalid is returned for a config that breaks the schema or a rule beyond it.
	ErrInvalid = errors.New("config: invalid")
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

// Config is fanin.yaml.
type Config struct {
	// Listen is the host:port of the HTTP API.
	Listen string `yaml:"listen"`
	// Store is the SQLite file, absolute after Load.
	Store string `yaml:"store"`
	Log   Log    `yaml:"log"`
	// OwnerTokenEnv names the environment variable that holds the owner token; the token is never in the file.
	OwnerTokenEnv string    `yaml:"owner_token_env"`
	Projects      []Project `yaml:"projects"`
}

// Log selects the log format and level.
type Log struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// Project is one project fanin follows.
type Project struct {
	Name string `yaml:"name"`
}

// Load reads path, validates it against the embedded schema and decodes it with unknown keys rejected.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %s: %w; pass an existing file with --config", ErrRead, path, err)
	}
	if err := validate(path, raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	seen := make(map[string]bool, len(cfg.Projects))
	for i, p := range cfg.Projects {
		if seen[p.Name] {
			return Config{}, fmt.Errorf("%w: %s: /projects/%d: project %q is listed twice; keep one entry",
				ErrInvalid, path, i, p.Name)
		}
		seen[p.Name] = true
	}
	if !filepath.IsAbs(cfg.Store) {
		cfg.Store = filepath.Join(filepath.Dir(path), cfg.Store)
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = LogJSON
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = LevelInfo
	}
	return cfg, nil
}

// validate checks raw against the schema; YAML is turned into JSON first, so the schema sees plain values.
func validate(path string, raw []byte) error {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	js, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%w: %s: %w; use string keys only", ErrInvalid, path, err)
	}
	if err := validateJSON(js); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	return nil
}

// ProjectNames returns the project names in config order.
func (c Config) ProjectNames() []string {
	out := make([]string, 0, len(c.Projects))
	for _, p := range c.Projects {
		out = append(out, p.Name)
	}
	return out
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
