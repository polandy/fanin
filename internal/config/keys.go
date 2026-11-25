package config

import (
	"fmt"
	"maps"
	"slices"
)

// retiredKeys are project settings keys fanin no longer reads, each with the hint that replaces it. They are
// refused in project files and under defaults in fanin.yaml.
var retiredKeys = map[string]string{
	"lanes":    "slots replace lanes: set max_parallel in the project file",
	"lane":     "slots replace lanes: set max_parallel in the project file",
	"machine":  "use host: local or a host of hosts.yaml",
	"oc-merge": "use oc_merge",
	"review":   "use review_skill",
}

// checkRetired refuses the first retired key of the settings mapping m found at prefix in path.
func checkRetired(path string, m map[string]any, prefix string) error {
	for _, key := range slices.Sorted(maps.Keys(m)) {
		if hint, ok := retiredKeys[key]; ok {
			return fmt.Errorf("%w: %s: at %s: key %q is retired (rule %s); %s",
				ErrInvalid, path, pointer(prefix, key), key, ruleRetired, hint)
		}
	}
	return nil
}
