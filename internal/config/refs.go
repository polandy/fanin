package config

import (
	"fmt"
	"maps"
	"slices"
)

// checkRefs checks the references between the files: hosts, forges, worker kinds and models.
func (t tree) checkRefs(c Config) error {
	kindsPath := t.path(fileWorkerKinds)
	for _, name := range slices.Sorted(maps.Keys(c.WorkerKinds)) {
		k := c.WorkerKinds[name]
		if k.DefaultModel != "" && !slices.Contains(k.Models, k.DefaultModel) {
			return refErr(kindsPath, pointer("/kinds", name, "default_model"), k.DefaultModel, ruleModelKnown,
				"not one of the kind's models", "list it under models")
		}
	}
	if c.Picker.Kind != "" {
		k, ok := c.WorkerKinds[c.Picker.Kind]
		if !ok {
			return refErr(kindsPath, "/picker/kind", c.Picker.Kind, ruleKindKnown, "not a kind of "+fileWorkerKinds,
				"add the kind under kinds")
		}
		if !slices.Contains(k.Models, c.Picker.Model) {
			return refErr(kindsPath, "/picker/model", c.Picker.Model, ruleModelKnown, "not a model of kind "+
				c.Picker.Kind, "list it under the kind's models")
		}
	}
	for i, p := range c.Projects {
		if err := c.checkProject(t.projects[i].path, p); err != nil {
			return err
		}
	}
	return nil
}

// checkProject checks the references of one project; a value may also come from defaults in fanin.yaml.
func (c Config) checkProject(path string, p Project) error {
	if _, ok := c.Hosts[p.Host]; !ok && p.Host != HostLocal {
		return refErr(path, "/host", p.Host, ruleHostKnown, "neither "+HostLocal+" nor a host of "+fileHosts,
			"add it to "+fileHosts+" or use "+HostLocal)
	}
	if _, ok := c.Forges[p.Forge]; !ok {
		return refErr(path, "/forge", p.Forge, ruleForgeKnown, "not a forge of "+fileForges, "add it to "+fileForges)
	}
	kind, ok := c.WorkerKinds[p.WorkerKind]
	if !ok {
		return refErr(path, "/worker_kind", p.WorkerKind, ruleKindKnown, "not a kind of "+fileWorkerKinds,
			"add it to "+fileWorkerKinds)
	}
	if _, ok := c.WorkerKinds[p.Fallback]; !ok && p.Fallback != "" {
		return refErr(path, "/fallback", p.Fallback, ruleKindKnown, "not a kind of "+fileWorkerKinds,
			"add it to "+fileWorkerKinds)
	}
	if p.Model != "" && len(kind.Models) > 0 && !slices.Contains(kind.Models, p.Model) {
		return refErr(path, "/model", p.Model, ruleModelKnown, "not a model of kind "+p.WorkerKind,
			"use one of its models in "+fileWorkerKinds)
	}
	return nil
}

func refErr(path, ptr, value, rule, what, fix string) error {
	return fmt.Errorf("%w: %s: at %s: %q is %s (rule %s); %s", ErrInvalid, path, ptr, value, what, rule, fix)
}
