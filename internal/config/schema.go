package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/polandy/fanin/config/schema"
)

// The schema of each config file, as embedded from config/schema.
const (
	schemaFanin       = "fanin.schema.json"
	schemaHosts       = "hosts.schema.json"
	schemaForges      = "forges.schema.json"
	schemaWorkerKinds = "worker-kinds.schema.json"
	schemaTriggers    = "triggers.schema.json"
	schemaProject     = "project.schema.json"
	schemaSettings    = "settings.schema.json"
	schemaSecret      = "secret.schema.json"
	// schemaName checks a project name taken from its file name.
	schemaName = schemaSettings + "#/$defs/name"
	// settingsPointer locates the overridable project settings inside schemaSettings.
	settingsPointer = "/$defs/settings"
	// schemaBase makes schema locations absolute so they never resolve against the working directory.
	schemaBase = "https://fanin.invalid/schema/"
)

var compiler = sync.OnceValues(func() (*jsonschema.Compiler, error) {
	c := jsonschema.NewCompiler()
	files, err := fs.Glob(schema.FS, "*.schema.json")
	if err != nil {
		return nil, fmt.Errorf("config: embedded schemas: %w", err)
	}
	for _, name := range files {
		raw, err := schema.FS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("config: embedded schema %s: %w", name, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("config: embedded schema %s: %w", name, err)
		}
		if err := c.AddResource(schemaBase+name, doc); err != nil {
			return nil, fmt.Errorf("config: embedded schema %s: %w", name, err)
		}
	}
	return c, nil
})

var (
	compiledMu sync.Mutex
	compiled   = map[string]*jsonschema.Schema{}
)

// compile returns the compiled schema for a location such as fanin.schema.json.
func compile(loc string) (*jsonschema.Schema, error) {
	compiledMu.Lock()
	defer compiledMu.Unlock()
	if s, ok := compiled[loc]; ok {
		return s, nil
	}
	c, err := compiler()
	if err != nil {
		return nil, err
	}
	s, err := c.Compile(schemaBase + loc)
	if err != nil {
		return nil, fmt.Errorf("config: embedded schema %s: %w", loc, err)
	}
	compiled[loc] = s
	return s, nil
}

// validateDoc checks one decoded YAML document against the schema at loc; the error lists each broken rule.
func validateDoc(loc string, doc any) error {
	sch, err := compile(loc)
	if err != nil {
		return err
	}
	js, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%w; use string keys only", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		return err
	}
	err = sch.Validate(inst)
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	var msgs []string
	for _, u := range leaves(*ve.DetailedOutput()) {
		loc := u.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		msg := fmt.Sprintf("at %s: %s (rule %s)", loc, u.Error, u.KeywordLocation)
		switch {
		case strings.Contains(u.AbsoluteKeywordLocation, schemaSecret):
			msg = fmt.Sprintf("at %s: must name an environment variable such as GH_TOKEN, never hold the secret "+
				"(rule %s); move the value into the environment", loc, u.KeywordLocation)
		case isUnknownKey(u.KeywordLocation):
			msg = fmt.Sprintf("at %s: unknown key (rule %s: %s); remove it or fix its spelling", loc,
				u.KeywordLocation, u.Error)
		case loc != "/":
			if v, ok := lookup(inst, u.InstanceLocation); ok {
				if js, err := json.Marshal(v); err == nil {
					msg += "; got " + string(js)
				}
			}
		}
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		return ve
	}
	return errors.New(strings.Join(msgs, "; "))
}

// leaves returns the innermost failed units of an output tree; they carry the specific message.
func leaves(u jsonschema.OutputUnit) []jsonschema.OutputUnit {
	if len(u.Errors) == 0 {
		if u.Error == nil {
			return nil
		}
		return []jsonschema.OutputUnit{u}
	}
	var out []jsonschema.OutputUnit
	for _, c := range u.Errors {
		out = append(out, leaves(c)...)
	}
	return out
}

// isUnknownKey tells whether a failed keyword is the closed-object rule that refuses a key.
func isUnknownKey(keywordLocation string) bool {
	return strings.HasSuffix(keywordLocation, "/additionalProperties") ||
		strings.HasSuffix(keywordLocation, "/unevaluatedProperties")
}

// lookup returns the value at a JSON pointer such as /hosts/host-a/address.
func lookup(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	cur := doc
	for _, tok := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[tok]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}
