package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"gopkg.in/yaml.v3"
)

// Origins that are not a file position.
const (
	originDefault  = "default"
	originBuiltin  = "built-in"
	originEnv      = "environment"
	secSet         = "set"
	keyOrigins     = "origins"
	keyProjectName = "project"
)

// keyLines maps the JSON pointer of every key of a parsed YAML document to its line.
func keyLines(n *yaml.Node) map[string]int {
	out := map[string]int{}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	walkKeys(n, "", out)
	return out
}

func walkKeys(n *yaml.Node, prefix string, out map[string]int) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		p := pointer(prefix, k.Value)
		out[p] = k.Line
		walkKeys(n.Content[i+1], p, out)
	}
}

// keyLines collects the key lines of every file of the tree by its path relative to the directory.
func (t tree) keyLines() map[string]map[string]int {
	out := map[string]map[string]int{}
	for name, d := range t.root {
		out[name] = d.lines
	}
	for _, d := range t.projects {
		out[projectFile(projectName(d.path))] = d.lines
	}
	return out
}

func projectFile(name string) string { return dirProjects + "/" + name + extYAML }

// at renders the position of pointer p in file, or "" when the file does not set it.
func (c Config) at(file, p string) string {
	if line, ok := c.lines[file][p]; ok {
		return fmt.Sprintf("%s:%d", file, line)
	}
	return ""
}

// orDefault is the position of p in file, else the default.
func (c Config) orDefault(file, p string) string {
	if o := c.at(file, p); o != "" {
		return o
	}
	return originDefault
}

// settingOrigin says where the project setting at pointer p comes from: the project file, the daemon defaults
// of fanin.yaml or the schema default.
func (c Config) settingOrigin(project, p string) string {
	if o := c.at(projectFile(project), p); o != "" {
		return o
	}
	return c.orDefault(fileFanin, "/"+keyDefaults+p)
}

// origin says where the value at path of the effective config e comes from.
func (c Config) origin(e Effective, path []string) string {
	last := path[len(path)-1]
	switch path[0] {
	case keyProjectName:
		return projectFile(e.Name)
	case "settings":
		return c.settingOrigin(e.Name, pointer("", path[1:]...))
	case "daemon":
		switch {
		case path[1] == "owner_token" && last == secSet:
			return originEnv
		case path[1] == "owner_token":
			return c.orDefault(fileFanin, "/owner_token_env")
		}
		return c.orDefault(fileFanin, pointer("", path[1:]...))
	case "host":
		if e.Host.Local {
			return originBuiltin
		}
		if last == "name" {
			return c.orDefault(fileHosts, pointer("/hosts", e.Host.Name))
		}
		return c.orDefault(fileHosts, pointer("/hosts", e.Host.Name, last))
	case "forge":
		f := pointer("/forges", e.Forge.Name)
		switch {
		case last == "name":
			return c.settingOrigin(e.Name, "/forge")
		case path[1] == "token" && last == secSet:
			return originEnv
		case path[1] == "token":
			return c.orDefault(fileForges, pointer(f, "token_env"))
		}
		return c.orDefault(fileForges, pointer(f, last))
	case "worker_kind":
		return c.orDefault(fileWorkerKinds, pointer("/kinds", e.Project.WorkerKind, last))
	}
	return originDefault
}

// Explained is an effective config with the origin of every value.
type Explained struct {
	node    yaml.Node
	origins map[string]string
}

// Explain resolves where each value of e comes from: file and line, or default.
func (c Config) Explain(e Effective) (Explained, error) {
	var x Explained
	if err := x.node.Encode(e); err != nil {
		return Explained{}, fmt.Errorf("config: dump %s: %w", e.Name, err)
	}
	x.origins = map[string]string{}
	c.annotate(e, &x.node, nil, x.origins)
	return x, nil
}

// annotate puts the origin as a line comment on each value and records it by pointer.
func (c Config) annotate(e Effective, n *yaml.Node, path []string, origins map[string]string) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		p := append(slices.Clone(path), k.Value)
		if v.Kind == yaml.MappingNode {
			c.annotate(e, v, p, origins)
			continue
		}
		o := c.origin(e, p)
		origins[pointer("", p...)] = o
		if v.Kind == yaml.ScalarNode {
			v.LineComment = o
		} else {
			k.LineComment = o
		}
	}
}

// WriteYAML writes the effective config with an origin comment on each value.
func (x Explained) WriteYAML(w io.Writer) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(&x.node); err != nil {
		return fmt.Errorf("config: write yaml: %w", err)
	}
	return enc.Close()
}

// WriteJSON writes the effective config as JSON with sorted keys and the origins by pointer under "origins".
func (x Explained) WriteJSON(w io.Writer) error {
	var v map[string]any
	if err := x.node.Decode(&v); err != nil {
		return fmt.Errorf("config: write json: %w", err)
	}
	v[keyOrigins] = x.origins
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("config: write json: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("config: write json: %w", err)
	}
	return nil
}
