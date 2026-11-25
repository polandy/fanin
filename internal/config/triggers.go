package config

import (
	"maps"
	"slices"
)

// Webhook kinds: what a delivery on the webhook queues.
const (
	WebhookForge   = "forge"
	WebhookAlert   = "alert"
	WebhookRelease = "release"
)

// releasePointer locates the release section inside schemaTriggers.
const releasePointer = "/properties/release"

// ruleReleaseKnown is the check that a release webhook has a release section.
const ruleReleaseKnown = "release-known"

// Triggers is triggers.yaml: the intake sources besides the CLI (spec: docs/dev/spec/intake.md).
type Triggers struct {
	Webhooks map[string]Webhook `yaml:"webhooks"`
	Cron     map[string]Cron    `yaml:"cron"`
	// Release is nil without a release section.
	Release *Release `yaml:"release"`
}

// Webhook is one signed webhook, served at POST /api/v1/webhooks/<name>.
type Webhook struct {
	Kind    string `yaml:"kind"`
	Project string `yaml:"project"`
	// SecretEnv names the variable that holds the HMAC secret; the secret is never in the files.
	SecretEnv string `yaml:"secret_env"`
	// Label is the issue label a forge webhook queues on.
	Label string `yaml:"label,omitempty"`
	Model string `yaml:"model,omitempty"`
}

// Cron queues one task every Every.
type Cron struct {
	Project string   `yaml:"project"`
	Every   Duration `yaml:"every"`
	Task    string   `yaml:"task,omitempty"`
	Analyze string   `yaml:"analyze,omitempty"`
	Model   string   `yaml:"model,omitempty"`
}

// Release is where a release webhook queues the image bump (SCENARIOS DR-1).
type Release struct {
	// Project is the config repo project; its base is the branch the compose modules are read from.
	Project     string `yaml:"project"`
	ComposeGlob string `yaml:"compose_glob"`
	Registry    string `yaml:"registry"`
	Model       string `yaml:"model"`
}

type triggersFile Triggers

// buildTriggers decodes triggers.yaml with the release defaults applied.
func (t tree) buildTriggers() (Triggers, error) {
	d, ok := t.root[fileTriggers]
	if !ok {
		return Triggers{}, nil
	}
	m := maps.Clone(d.m)
	if rel, ok := m["release"].(map[string]any); ok {
		base, err := releaseDefaults()
		if err != nil {
			return Triggers{}, err
		}
		m["release"] = merge(merge(map[string]any{}, base), rel)
	}
	var f triggersFile
	if err := decodeStrict(d.path, m, &f); err != nil {
		return Triggers{}, err
	}
	return Triggers(f), nil
}

// checkTriggers checks that every trigger names a configured project and a model of its worker kind.
func (t tree) checkTriggers(c Config) error {
	path := t.path(fileTriggers)
	for _, name := range slices.Sorted(maps.Keys(c.Triggers.Webhooks)) {
		w := c.Triggers.Webhooks[name]
		if err := c.checkTarget(path, pointer("/webhooks", name), w.Project, w.Model); err != nil {
			return err
		}
		if w.Kind == WebhookRelease && c.Triggers.Release == nil {
			return refErr(path, pointer("/webhooks", name, "kind"), w.Kind, ruleReleaseKnown,
				"a release webhook without a release section", "add release.project, the config repo project")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Triggers.Cron)) {
		cr := c.Triggers.Cron[name]
		if err := c.checkTarget(path, pointer("/cron", name), cr.Project, cr.Model); err != nil {
			return err
		}
	}
	if r := c.Triggers.Release; r != nil {
		return c.checkTarget(path, "/release", r.Project, r.Model)
	}
	return nil
}

// checkTarget checks the project and the model of the trigger at ptr.
func (c Config) checkTarget(path, ptr, project, model string) error {
	p, ok := c.Project(project)
	if !ok {
		return refErr(path, ptr+"/project", project, ruleProjectKnown, "not a project of "+dirProjects,
			"add "+dirProjects+"/"+project+extYAML+" or name a configured project")
	}
	if model == "" {
		return nil
	}
	if kind := c.WorkerKinds[p.WorkerKind]; !slices.Contains(kind.Models, model) {
		return refErr(path, ptr+"/model", model, ruleModelKnown, "not a model of worker kind "+p.WorkerKind+
			" of project "+project, "pick one of the kind's models")
	}
	return nil
}
