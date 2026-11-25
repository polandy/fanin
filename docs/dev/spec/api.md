# HTTP API and SSE

Status: implemented in `internal/api` (server), `internal/api/wire` (JSON types) and `internal/api/client` (Go
client for the CLI). Direction: [concept](../concept.md), owner UI. Rules: [guidelines](../guidelines.md), HTTP.

## Conventions

- `net/http` with the Go 1.22 mux patterns (`"GET /api/v1/health"`); no framework. Paths start with `/api/v1/`.
- Bodies are JSON. Every non-2xx answer is `{"error": {"code": "<code>", "message": "<what, and the fix>"}}` with
  one of the codes in `wire`: `not_found`, `unauthorized`, `forbidden`, `bad_request`, `too_large`, `conflict`, `delivery_failed`,
  `internal`.
  An unknown path is a JSON 404 too.
- An unreadable store is a 500 `internal`, never an empty answer.

## Owner token

- `api.New` refuses an empty owner token (`ErrNoOwnerToken`): command endpoints fail closed.
- Every route whose method is not GET or HEAD is a command endpoint. `New` wraps it; a route cannot opt out.
  - No or wrong `Authorization: Bearer <token>`: 401 `unauthorized`. The token is compared in constant time and is
    never logged.
  - A correct token on a request that carries webhook headers (`X-GitHub-*`, `X-Gitea-*`, `X-Gogs-*`,
    `X-Gitlab-*`, `X-Hub-Signature*`): 403 `forbidden`. A webhook proves itself by its signature on its own intake
    route, never as the owner.
  - The body is capped at `api.MaxBodyBytes` (1 MiB).
- A route with `Auth: api.AuthRun` takes a worker's run token instead: the middleware only requires a non-empty
  bearer, and the handler checks it against the run inside the store transaction (see
  [worker contract](worker-contract.md)). The webhook-header 403 and the body cap apply as above.
- GET endpoints need no token: the API is meant for a loopback or tailnet listener.

## Route registry

A file adds its endpoints with `api.RegisterBuiltin(fn)` from `init`; `fn` calls `Registry.Add(api.Route{Pattern,
Handler})`. `api.RegisterBuiltins(reg)` applies every such function, and `api.New` serves the registry. Handlers get
the `*api.Server` (`Source()`, `Clock()`, `Logger()`, `Projects()`). Other strands add endpoints in their own files and touch
none of the existing ones.

## Endpoints

| Route | Answer |
|---|---|
| `GET /api/v1/health` | `{"status": "ok"}` |
| `GET /api/v1/snapshot` | `wire.Snapshot`: open tasks, their runs, open questions, active holds, project locks with `stale`, the newest event per project, the owner board per project and the hosts ([ui](ui.md)), the projects (config order first, see [daemon](daemon.md)), and `last_seq` |
| `GET /ui/` | the embedded web UI, see [ui](ui.md); `GET /` redirects there |
| `GET /api/v1/events` | server-sent events, see below |
| `GET /api/v1/questions` | `wire.Questions`: the open owner questions, oldest first |
| `GET /api/v1/wait?owner=&after=` | one `wire.Event`, see [owner](owner.md) |
| `GET /api/v1/tasks?project=` | `wire.Tasks`: the open tasks, optionally of one project |
| `POST /api/v1/tasks` | body `wire.TaskRequest`; 201 `wire.Task`. 400 for a request that breaks an intake rule, 404 unknown project, 409 task ID exists, see [intake](intake.md) |
| `POST /api/v1/tasks/{id}/answer` | body `{"answer": "2"}`; `wire.Ack`. 400 with the options, 404 no open question, 502 `delivery_failed` |
| `POST /api/v1/tasks/{id}/go` | `wire.Ack`; 409 `conflict` when the task is not in `gating` |
| `POST /api/v1/holds/{id}/lift` | `wire.Ack`; 404 for no active hold |
| `POST /api/v1/projects/{p}/pause`, `/resume` | `wire.Ack`; 404 for a project outside the config |
| `POST /api/v1/runs/{id}/report`, `/progress` | run token; `wire.Ack`, see [worker contract](worker-contract.md) |

The owner routes answer 500 when the server has no `Config.Owner`, `POST /api/v1/tasks` when it has no
`Config.Intake`, the run routes when it has no `Config.Reports`.

The snapshot never carries a run's report token. `last_seq` is read before the rest, so a client that opens the
stream with `after=<last_seq>` sees every later change at least once.

## Event stream

- `GET /api/v1/events?after=<seq>` (or the `Last-Event-ID` header; `after` wins) streams the event log from the
  event after `seq`, then each new one. No cursor means the whole log. A bad cursor is a 400.
- One SSE event per log entry: `id: <seq>`, `event: <type>` (a `model.EventType`) and `data:` the `wire.Event` JSON.
- The handler takes `Store.Changed()` before it reads, then waits on that channel, the request context and a
  keep-alive timer; nothing polls. `Store.Changed` closes after each committed `Transition`.
- An idle stream gets `: keepalive` every `api.KeepAliveEvery` (25 s, from the injected clock).

## Go client

`client.New(baseURL, token, httpClient)`: `Health`, `Snapshot`, `Events(ctx, after, ready, fn)` and `Post(ctx, path,
body, out)` for the typed calls other strands add. Owner calls: `Answer`, `Go`, `LiftHold`, `Pause`, `Resume`,
`Questions` and `Wait(ctx, owner, after)` (`client.FromNow` for only new events). A failed call is a `*client.Error` (`errors.As`).

## Tests

`internal/api/api_test.go` (httptest, real SQLite): POST without a token 401 and with it 200; a webhook-shaped call
refused; a route registered from its own function; the snapshot golden file (`go test ./internal/api -update`);
SSE after a store write, waiting on a channel; resume by cursor; keep-alive from the manual clock. `owner_routes_test.go`: every owner route through the client with a fake
`Deliverer`, its failure codes, and `wait` with a cursor, a cancel and a bad parameter.
