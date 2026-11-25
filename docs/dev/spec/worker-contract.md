# Worker contract

Status: implemented in `pkg/faninmcp` (types, client, MCP tools), `cmd/fanin/cmd_workermcp.go` (stdio server),
`internal/api/report_routes.go` (routes), `internal/report` (token check, store writes) and `internal/resultwatch`
(the `result.json` fallback). Direction: [concept](../concept.md), D2 and C-REPORT.

## Design

- A worker reports its result with the MCP tool `fanin_report` and its heartbeat with `fanin_progress`. Both come
  from `fanin worker-mcp`, a stdio MCP server of the official Go SDK, which forwards each call to fanind.
- A worker without MCP writes `result.json` into its run directory; `resultwatch.Watch` picks it up.
- A report counts only with the token of the task's current run (SCENARIOS AW-1): the run with the highest
  number, in a state that is not terminal. The check and the write run in one store transaction.
- `pkg/faninmcp` is the only package under `pkg/`: a worker-side tool may import it without pulling in the daemon.

## Environment

fanind sets these for every run; `fanin worker-mcp` refuses to start without them (usage error naming the variable).

| Variable | Value |
|---|---|
| `FANIN_URL` | the daemon's base URL (also `--url`) |
| `FANIN_RUN_ID` | the run's ID |
| `FANIN_RUN_TOKEN` | the run's report token; never logged, never in the snapshot |

## Report fields

| Field | Type | Rule |
|---|---|---|
| `kind` | string | required; one of `done_pr`, `pushed`, `committed`, `issue`, `nothing`, `blocked` |
| `pr` | integer | required for `done_pr` (SCENARIOS GH-14) |
| `branch` | string | required for `pushed` and `committed`; one line, at most 400 characters |
| `issue` | integer | required for `issue` |
| `issue_url` | string | optional; absolute `http(s)` with a host, no whitespace, at most 400 characters; never cut, refused instead |
| `text` | string | required for `nothing` and `blocked`; one line, at most 400 characters (SCENARIOS AW-2) |

- `branch` and `text` are normalised before they are stored: every whitespace run becomes one space, then they are
  cut to `faninmcp.TextCap` (400, equal to `model.FieldCap`). A cap never refuses a report.
- Each report is one `result_reported` event whose detail is the kind; `issue`, `nothing` and `blocked` arrive as
  their own events like the others (SCENARIOS FP-3). A later report of the same run replaces the result.
- `fanin_progress` takes an optional `text`, capped the same way, and appends a `progress` event (detail
  `heartbeat` when empty).

## Tool examples

```json
{"name": "fanin_report", "arguments": {"kind": "done_pr", "pr": 42}}
{"name": "fanin_report", "arguments": {"kind": "blocked", "text": "Which database should the cache use?"}}
{"name": "fanin_progress", "arguments": {"text": "tests running"}}
```

`pkg/faninmcp/testdata/tools.golden.json` pins both tool schemas (`go test ./pkg/faninmcp -update`). A daemon
refusal is a tool result with `IsError` and the status, code and message; a connection failure names the daemon
URL, `$FANIN_URL` and the `result.json` fallback.

## Routes

| Route | Body | Answer |
|---|---|---|
| `POST /api/v1/runs/{id}/report` | a report as above | `wire.Ack` |
| `POST /api/v1/runs/{id}/progress` | `{"text": "..."}` | `wire.Ack` |

- Auth is the run token as `Authorization: Bearer <token>` (`api.AuthRun`), not the owner token. Webhook headers
  are a 403 and the body cap is 1 MiB, as on every command route.
- Unknown fields and trailing data are a 400.

| Case | Status |
|---|---|
| no or empty bearer, or a token that is not the run's (also the owner token) | 401 `unauthorized` |
| a run that is not its task's current run, or has ended | 409 `conflict` |
| an unknown run | 404 `not_found` |
| a body that is not one report, an unknown kind, a missing required field, a bad `issue_url` | 400 `bad_request` |
| a body over 1 MiB | 413 `too_large` |
| an unreadable store, or no report service configured | 500 `internal` |

A refused token or stale run is logged at Warn with `task_id`, `run_id` and `project`, never with a token.

## result.json

The fallback file is `result.json` in the run directory: one JSON object with the report fields plus the run token.
`pkg/faninmcp/testdata/result.schema.json` is its JSON Schema. A worker writes it atomically (temp file, rename).

```json
{"token": "<value of $FANIN_RUN_TOKEN>", "kind": "nothing", "text": "the bug is already fixed on main"}
```

- `resultwatch.Watch(ctx, clock, dir, every)` reads the file now and then every `every` (default 5 s) on the
  injected clock, until it appears or ctx ends.
- A file that exists but cannot be read or parsed (half written, not one object, an unknown field, no token, no
  kind) is `resultwatch.ErrUnreadable`, never a result.
- The parsed file goes through the same token check as the routes.

## Open

- Wiring `resultwatch` to `report.Service` belongs to the scheduler, which does not exist yet.
- GH-14: the check that the PR's head is the run's branch belongs to the gate.
- Whether a `progress` event moves a `stalled` run back to `working`, and how `stall_after` reads heartbeats
  (SCENARIOS AW-4), is decided with the await loop.
- Caps beyond the 400-character field cap (a number of reports per run, a rate) are not set; the 1 MiB body cap
  is the only bound so far.
