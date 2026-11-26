# Web UI

Status: implemented in `internal/api/web` (static files) and `internal/api/web_routes.go` (embed and routes).
Direction: [concept](../concept.md), owner UI. Data: [api](api.md). Scenarios: ST-2 (board), ST-3 (hosts).

## Delivery

- `index.html`, `app.js` and `style.css` are embedded with `//go:embed web` and served under `/ui/`; `GET /`
  redirects there. Vanilla JS, no framework, no build step, no `node`: `go build` is enough. The folder holds no
  `.go` file, so it is no Go package and lies outside `GO_PKGS`.
- The page reads only the API: `GET /api/v1/snapshot` for the data and `GET /api/v1/events` for live updates.
  Page text is always set as text, never as HTML.
- Language: English.

## Views

The nav has three views, chosen by the URL hash; the task page is the link target for a task.

| Hash | View |
|---|---|
| `#inbox` (default) | One card per open owner question: project, task link, the question and one button per option, the recommended one marked. A click calls `POST /api/v1/tasks/{id}/answer` with `{"answer": "<n>"}`, the same call as `fanin answer`, so the logged event is identical. A question with `allow_text` also has a free-text field. The nav badge counts open questions. |
| `#board` | One card per project, in snapshot order, with the ST-2 sections below. |
| `#hosts` | One card per host (ST-3), see below. |
| `#task/<id>` | An open task: title, project, shape, state, prompt, its runs and its events, which follow the stream live. A task that is not open says so. |

### Board sections (`snapshot.board`)

One `wire.BoardProject` per project in `snapshot.projects`:

| Field | Section | Content |
|---|---|---|
| `done` | Done | `count` of tasks done in the project's current batch and the newest four (`newest`, newest first). The batch starts when the project's oldest open task was created: a task counts when it was last updated at or after that time. A project without an open task has an empty batch. |
| `next` | Next | queued tasks, oldest first; `blocked` is set when a dependency is still open. Decision tasks are listed under `decisions`. |
| `waiting` | Waiting on you | tasks in `needs_owner` or `paused`; `question_id` names the open question |
| `ready` | Ready for go | gating tasks whose latest `gate_verdict` starts with `ready #<n>: `; `pr` is `<n>` and `verdict` the rest of the line. A ready verdict without a PR number fails the snapshot (500) |
| `holds` | CI and merge holds | the project's active holds, each with a Lift button (`POST /api/v1/holds/{id}/lift`) and its fix task |
| `decisions` | Decisions | open tasks of shape `decision` that are not already waiting |

### Hosts (`snapshot.hosts`)

- The daemon reads the hosts through `api.Config.Fleet` (`Hosts(ctx)`). Without a fleet the list is empty and the
  view says so.
- A host the fleet could not read has `warning` set and no workers: the view shows one warning line for it, and
  the other hosts are listed as usual. A fleet that fails as a whole fails the snapshot (500); an unreadable answer
  is never an empty list.
- Workers are ordered waiting-on-owner first, then managed before owner sessions, then by agent name. The view
  groups them as *Managed workers* and *Owner sessions*.

## Commands and the token

Command buttons send `Authorization: Bearer <owner token>`. The page asks for the token once per tab and keeps it
in `sessionStorage` only; a 401 forgets it. A refused or failed command shows the API's error message in the notice
bar; nothing is retried silently.

## Live updates

After the first snapshot the page opens `GET /api/v1/events?after=<last_seq>` and re-reads the snapshot (debounced)
on every event. The browser reconnects by itself and resumes with `Last-Event-ID`. The nav shows `live` or
`reconnecting`; on a failed snapshot read the notice bar says so and the last data stays visible.

## Tests

`internal/api/board_test.go`: one golden file per board section from one fixture (`testdata/board.*.golden.json`,
`go test ./internal/api -update`, `ready` included), the hosts view with an unreachable host and a failing fleet.
`web_routes_test.go`: the files are served, the tree has no `.go` file, and the request `app.js` sends for an answer
logs the same event as the CLI client.
