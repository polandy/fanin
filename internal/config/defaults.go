package config

import (
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/polandy/fanin/config/schema"
)

// settingsDefaults returns the schema defaults of the project settings as a nested mapping; the schema is the
// only place the default values live.
var settingsDefaults = sync.OnceValues(func() (map[string]any, error) {
	return schemaDefaults(schemaSettings, settingsPointer)
})

// releaseDefaults returns the schema defaults of the release section of triggers.yaml.
var releaseDefaults = sync.OnceValues(func() (map[string]any, error) {
	return schemaDefaults(schemaTriggers, releasePointer)
})

// schemaDefaults returns the defaults of the object schema at ptr in the embedded schema file.
func schemaDefaults(file, ptr string) (map[string]any, error) {
	raw, err := schema.FS.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("config: embedded schema %s: %w", file, err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("config: embedded schema %s: %w", file, err)
	}
	node, ok := lookup(doc, ptr)
	if !ok {
		return nil, fmt.Errorf("config: embedded schema %s has no %s", file, ptr)
	}
	return collectDefaults(node), nil
}

// collectDefaults gathers the default of each property of an object schema, recursing into nested objects.
func collectDefaults(node any) map[string]any {
	out := map[string]any{}
	n, _ := node.(map[string]any)
	props, _ := n["properties"].(map[string]any)
	for key, p := range props {
		pm, _ := p.(map[string]any)
		if d, ok := pm["default"]; ok {
			out[key] = d
			continue
		}
		if nested := collectDefaults(pm); len(nested) > 0 {
			out[key] = nested
		}
	}
	return out
}

// merge copies src into dst, recursing where both hold a mapping, and returns dst.
func merge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		sm, sok := v.(map[string]any)
		dm, dok := dst[k].(map[string]any)
		switch {
		case sok && dok:
			dst[k] = merge(merge(map[string]any{}, dm), sm)
		case sok:
			dst[k] = merge(map[string]any{}, sm)
		default:
			dst[k] = v
		}
	}
	return dst
}

// pointer joins JSON pointer tokens, escaping each.
func pointer(prefix string, tokens ...string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, t := range tokens {
		b.WriteString("/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return b.String()
}
