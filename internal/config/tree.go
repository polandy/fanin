package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The files of the config directory.
const (
	fileFanin       = "fanin.yaml"
	fileHosts       = "hosts.yaml"
	fileForges      = "forges.yaml"
	fileWorkerKinds = "worker-kinds.yaml"
	fileTriggers    = "triggers.yaml"
	dirProjects     = "projects"
	extYAML         = ".yaml"
	extYML          = ".yml"
	// keyDefaults holds the project settings defaults in fanin.yaml.
	keyDefaults = "defaults"
)

// rootSchemas maps each root file to its schema; only fanin.yaml is required.
var rootSchemas = map[string]string{
	fileFanin: schemaFanin, fileHosts: schemaHosts, fileForges: schemaForges, fileWorkerKinds: schemaWorkerKinds,
	fileTriggers: schemaTriggers,
}

// Rule names of the checks beyond the schema.
const (
	ruleFileKnown  = "file-known"
	ruleHostKnown  = "host-known"
	ruleForgeKnown = "forge-known"
	ruleKindKnown  = "kind-known"
	ruleModelKnown = "model-known"
	ruleRetired    = "retired-key"
)

// doc is one parsed, schema-valid config file.
type doc struct {
	path string
	m    map[string]any
}

// tree is the parsed config directory.
type tree struct {
	dir      string
	root     map[string]doc
	projects []doc
}

func (t tree) path(file string) string { return filepath.Join(t.dir, file) }

// readTree parses and validates every file of dir.
func readTree(dir string) (tree, error) {
	t := tree{dir: dir, root: map[string]doc{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return tree{}, fmt.Errorf("%w: %s: %w", ErrRead, dir, err)
	}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if _, known := rootSchemas[e.Name()]; !known && !e.IsDir() && (ext == extYAML || ext == extYML) {
			return tree{}, fmt.Errorf("%w: %s: unknown config file (rule %s); the directory holds %s and %s/*%s",
				ErrInvalid, t.path(e.Name()), ruleFileKnown,
				strings.Join(slices.Sorted(maps.Keys(rootSchemas)), ", "), dirProjects, extYAML)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(rootSchemas)) {
		var hook func(map[string]any) error
		if name == fileFanin {
			hook = func(m map[string]any) error {
				d, _ := m[keyDefaults].(map[string]any)
				return checkRetired(t.path(name), d, "/"+keyDefaults)
			}
		}
		d, err := readDoc(t.path(name), rootSchemas[name], hook)
		if errors.Is(err, fs.ErrNotExist) && name != fileFanin {
			continue
		}
		if err != nil {
			return tree{}, err
		}
		t.root[name] = d
	}
	return t, t.readProjects()
}

// readProjects parses projects/*.yaml in name order.
func (t *tree) readProjects() error {
	entries, err := os.ReadDir(t.path(dirProjects))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrRead, t.path(dirProjects), err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != extYAML {
			continue
		}
		path := filepath.Join(t.path(dirProjects), e.Name())
		if err := validateDoc(schemaName, projectName(path)); err != nil {
			return fmt.Errorf("%w: %s: the file name gives the project name: %w; rename the file", ErrInvalid, path, err)
		}
		d, err := readDoc(path, schemaProject, func(m map[string]any) error { return checkRetired(path, m, "") })
		if err != nil {
			return err
		}
		t.projects = append(t.projects, d)
	}
	return nil
}

func projectName(path string) string { return strings.TrimSuffix(filepath.Base(path), extYAML) }

// readDoc parses path, runs hook on the raw mapping and validates it against the schema at loc.
func readDoc(path, loc string, hook func(map[string]any) error) (doc, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc{}, fmt.Errorf("%w: %s: %w; create it (see config/examples)", ErrRead, path, err)
	}
	if err != nil {
		return doc{}, fmt.Errorf("%w: %s: %w", ErrRead, path, err)
	}
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return doc{}, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	if v == nil {
		v = map[string]any{}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return doc{}, fmt.Errorf("%w: %s: at /: the file must be a mapping of keys", ErrInvalid, path)
	}
	if hook != nil {
		if err := hook(m); err != nil {
			return doc{}, err
		}
	}
	if err := validateDoc(loc, m); err != nil {
		return doc{}, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	return doc{path: path, m: m}, nil
}

// decodeStrict decodes the mapping m into out with unknown keys rejected.
func decodeStrict(path string, m map[string]any, out any) error {
	raw, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	return nil
}

type faninFile struct {
	Listen        string         `yaml:"listen"`
	Store         string         `yaml:"store"`
	Log           Log            `yaml:"log"`
	OwnerTokenEnv string         `yaml:"owner_token_env"`
	Defaults      map[string]any `yaml:"defaults"`
}

type hostsFile struct {
	Hosts map[string]struct {
		Address string `yaml:"address"`
	} `yaml:"hosts"`
}

type forgesFile struct {
	Forges map[string]Forge `yaml:"forges"`
}

type kindsFile struct {
	Kinds  map[string]WorkerKind `yaml:"kinds"`
	Picker Picker                `yaml:"picker"`
}

// build decodes the parsed tree into a Config and applies the defaults.
func (t tree) build() (Config, error) {
	var f faninFile
	if err := decodeStrict(t.path(fileFanin), t.root[fileFanin].m, &f); err != nil {
		return Config{}, err
	}
	cfg := Config{Dir: t.dir, Listen: f.Listen, Store: f.Store, Log: f.Log, OwnerTokenEnv: f.OwnerTokenEnv,
		Hosts: map[string]Host{}, Forges: map[string]Forge{}, WorkerKinds: map[string]WorkerKind{}}
	if !filepath.IsAbs(cfg.Store) {
		cfg.Store = filepath.Join(t.dir, cfg.Store)
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = LogJSON
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = LevelInfo
	}
	var hosts hostsFile
	var forges forgesFile
	var kinds kindsFile
	for file, out := range map[string]any{fileHosts: &hosts, fileForges: &forges, fileWorkerKinds: &kinds} {
		if d, ok := t.root[file]; ok {
			if err := decodeStrict(d.path, d.m, out); err != nil {
				return Config{}, err
			}
		}
	}
	for name, h := range hosts.Hosts {
		cfg.Hosts[name] = Host{Name: name, Address: h.Address}
	}
	maps.Copy(cfg.Forges, forges.Forges)
	maps.Copy(cfg.WorkerKinds, kinds.Kinds)
	cfg.Picker = kinds.Picker
	base, err := settingsDefaults()
	if err != nil {
		return Config{}, err
	}
	daemonDefaults, _ := t.root[fileFanin].m[keyDefaults].(map[string]any)
	for _, d := range t.projects {
		merged := merge(merge(merge(map[string]any{}, base), daemonDefaults), d.m)
		var p Project
		if err := decodeStrict(d.path, merged, &p); err != nil {
			return Config{}, err
		}
		p.Name = projectName(d.path)
		cfg.Projects = append(cfg.Projects, p)
	}
	return cfg, nil
}
