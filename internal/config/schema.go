package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaName = "fanin.schema.json"

//go:embed schema/fanin.schema.json
var schemaJSON []byte

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("config: embedded schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaName, doc); err != nil {
		return nil, fmt.Errorf("config: embedded schema: %w", err)
	}
	return c.Compile(schemaName)
})

// validateJSON checks one JSON document against the embedded schema.
func validateJSON(js []byte) error {
	sch, err := compiled()
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		return err
	}
	err = sch.Validate(doc)
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	var msgs []string
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil || len(u.Errors) > 0 {
			continue
		}
		loc := u.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		msg := fmt.Sprintf("at %s: %s (rule %s)", loc, u.Error, u.KeywordLocation)
		if v, ok := lookup(doc, u.InstanceLocation); ok && loc != "/" {
			if js, err := json.Marshal(v); err == nil {
				msg += "; got " + string(js)
			}
		}
		msgs = append(msgs, msg)
	}
	return errors.New(strings.Join(msgs, "; "))
}

// lookup returns the value at a JSON pointer such as /projects/0/name.
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
