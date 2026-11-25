//go:build tools

// Package deps pins the shared dependencies until the packages that use them land.
package deps

import (
	_ "github.com/google/go-cmp/cmp"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
