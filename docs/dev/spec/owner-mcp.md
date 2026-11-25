# Owner MCP server

Status: implemented in `internal/ownermcp` (tools) and `cmd/fanin/cmd_mcp.go` (stdio). Direction:
[concept](../concept.md), D1: the owner's MCP server lives in the `fanin` binary. User view:
[operation](../../user/operation.md).

## Design

- `ownermcp.New(client, baseURL, version)` returns an `mcp.Server` of the official Go SDK (vendored). `fanin mcp`
  runs it on `mcp.StdioTransport`; tests run it on in-memory transports.
- Every tool calls the `internal/api/client` method the CLI command calls (`Questions`, `Answer`, `Go`,
  `Snapshot`). Validation stays in the daemon route, so an invalid option is refused with the options exactly as
  by `fanin answer` (OA-1). `TestOwnerMCP` runs the same cases through both and compares.
- Tools: `fanin_questions`, `fanin_answer`, `fanin_go`, `fanin_status`. Inputs are structs with `jsonschema`
  tags; `testdata/tools.golden.json` pins the schemas (`go test ./internal/ownermcp -update`).
- A failed call is a tool result with `IsError` set: an API error carries its status, code and message; a
  connection failure names the daemon URL and the fix. Missing arguments are refused before any call.

## Open

`fanin_task_add` needs a task-creation endpoint and a `fanin task add` command first; neither exists yet.
