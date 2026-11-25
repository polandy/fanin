// Package schema embeds the JSON Schemas of the fanin config tree (spec: docs/dev/spec/config.md).
package schema

import "embed"

// FS holds every *.schema.json file of the config tree.
//
//go:embed *.schema.json
var FS embed.FS
