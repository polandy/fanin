package config

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Secrets looks up a secret by its name. The config only ever holds names; values stay with the caller.
type Secrets interface {
	Lookup(name string) (string, bool)
}

// SecretRef names a secret and says whether it is set; it never holds the value.
type SecretRef struct {
	Env string `yaml:"env"`
	Set bool   `yaml:"set"`
}

// Effective is the resolved config of one project, safe to print.
type Effective struct {
	Daemon  EffectiveDaemon `yaml:"daemon"`
	Name    string          `yaml:"project"`
	Project Project         `yaml:"settings"`
	Host    Host            `yaml:"host"`
	Forge   EffectiveForge  `yaml:"forge"`
	Kind    WorkerKind      `yaml:"worker_kind"`
}

// EffectiveDaemon is the daemon part of Effective.
type EffectiveDaemon struct {
	Listen     string    `yaml:"listen"`
	Store      string    `yaml:"store"`
	Log        Log       `yaml:"log"`
	OwnerToken SecretRef `yaml:"owner_token"`
}

// EffectiveForge is the forge of a project with its token as a reference.
type EffectiveForge struct {
	Name  string    `yaml:"name"`
	Kind  string    `yaml:"kind"`
	URL   string    `yaml:"url,omitempty"`
	Token SecretRef `yaml:"token"`
}

// Effective resolves project name; sec only tells whether each named secret is set.
func (c Config) Effective(name string, sec Secrets) (Effective, error) {
	p, ok := c.Project(name)
	if !ok {
		return Effective{}, fmt.Errorf("%w: %q has no file %s/%s%s in %s", ErrUnknownProject, name, dirProjects, name,
			extYAML, c.Dir)
	}
	f := c.Forges[p.Forge]
	return Effective{
		Daemon: EffectiveDaemon{Listen: c.Listen, Store: c.Store, Log: c.Log, OwnerToken: ref(sec, c.OwnerTokenEnv)},
		Name:   name, Project: p, Host: c.HostOf(p),
		Forge: EffectiveForge{Name: p.Forge, Kind: f.Kind, URL: f.URL, Token: ref(sec, f.TokenEnv)},
		Kind:  c.WorkerKinds[p.WorkerKind],
	}, nil
}

func ref(sec Secrets, env string) SecretRef {
	v, ok := sec.Lookup(env)
	return SecretRef{Env: env, Set: ok && v != ""}
}

// WriteYAML writes e as YAML.
func (e Effective) WriteYAML(w io.Writer) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(e); err != nil {
		return fmt.Errorf("config: dump %s: %w", e.Name, err)
	}
	return enc.Close()
}
