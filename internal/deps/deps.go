//go:build tools

// Package deps pins the shared dependencies until the packages that use them land.
package deps

import (
	_ "github.com/google/go-cmp/cmp"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)
