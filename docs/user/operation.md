# Operation

## Starting fanind

Under systemd (see [Install](install.md)): `sudo systemctl start|stop|restart fanind` and `systemctl status
fanind`. By hand:

```sh
export FANIN_OWNER_TOKEN=...        # the name set in owner_token_env
fanind --config /path/to/config
```

`fanind` opens (or creates) the store, serves the API on `listen` and logs `fanind: serving` with the address.
It exits with 1 and a message naming the fix when the config is missing or invalid, the token variable or a
webhook secret variable is empty, or the address is taken.

SIGINT or SIGTERM stops it: open requests end, the store is closed and the exit code is 0. A stop changes no task.

### Following the bash orchestrator

```sh
fanind --config /path/to/config --shadow /path/to/orchestrator/state   # or FANIN_SHADOW=...
```

With `--shadow`, `fanind` reads the bash orchestrator's state directory (`<project>.md`, hold and merge-lock files)
every 30 s and mirrors it into the store, so `fanin status` shows the bash state. It never writes to that
directory. An unreadable directory or file stops the start with exit 1 and a message naming it; later failures are
logged at `ERROR` and the next import tries again. Imported task IDs start with `shadow-`.

### Webhooks, cron and releases

With a `triggers.yaml` ([Configuration](configuration.md#triggersyaml)), `fanind` also serves
`POST /api/v1/webhooks/<name>` and runs the cron entries and release deploys. A webhook needs no owner token but a
valid signature; a wrong one gets 401 and queues nothing. To reach it from a forge, expose only that path (for
example through a reverse proxy at `https://hooks.example.com`), never the rest of the API.

Cron and release tasks are logged as `intake: cron task queued` and `intake: release bump queued`; a failed
release deploy is logged at `ERROR` with the reason and queues nothing. A release still waiting in the queue when
`fanind` stops is dropped; redeliver it from the forge's webhook page.

## fanin status

```sh
fanin --daemon-url http://127.0.0.1:7420 status          # every project, in config order
fanin --daemon-url http://127.0.0.1:7420 status alpha    # only alpha
```

`--daemon-url` falls back to `$FANIN_URL`. Each line shows:

- `loop=active|idle`: whether a task of the project is running, in its gates or in post-merge.
- `worker=<n>`: the workers still running.
- `merge=free`, `held by <holder>` or `stale (<holder>)`: the project lock. `stale` means the lease ran out or its
  holder is gone; the lock is free to take.
- `last:` the newest event of the project.
- `open:` ready, blocked, paused, needs-owner and failed tasks, and the active holds.

A daemon that cannot be reached is an error (exit 1), never an empty status.

## Owner commands

All of them need `--daemon-url` (or `$FANIN_URL`) and the owner token (`--token` or `$FANIN_TOKEN`); each prints
the task, hold or project and what happened. A refused or failed command exits with 1 and names the reason.

```sh
fanin answer t1 2              # pick option 2 of t1's open question
fanin answer t1 ship it today  # free text, only where the question allows it
fanin go t1                    # give the go for a task in its gates
fanin hold lift h1             # lift a hold after the cause is fixed
fanin pause alpha              # stop alpha before its next worker, once
fanin resume alpha             # clear that pause
fanin wait --owner             # block until something needs the owner; prints one event
fanin task add --project alpha --task "fix the build" --model opus   # queue an owner task; prints its ID
fanin task add --project alpha --analyze "why is CI slow?"           # queue an owner question
fanin task list --project alpha  # open tasks, with their `after` dependencies
fanin task import batch.chain --project alpha                        # queue a chain file
fanin task cancel t1 --reason "merged outside fanind"                # end a stale task for good
```

- `task cancel` ends a queued, running, gating or blocked task: its worker's pane closes and its slot goes to
  the next task. It merges nothing and leaves the PR as it is. A done or merged task is refused.
- `answer` refuses free text on a numbered dialog and lists the options; nothing reaches the worker then. When
  the answer cannot be delivered, nothing is recorded and the question stays open.
- On a `loop: fanin` project a worker's dialog (a permission prompt, a question with numbered options) becomes an
  open question with a note to you; `fanin answer t1 2` presses key 2 in the worker's pane. A dialog fanind cannot
  read only sends a note: answer it in the pane. An answer typed in the pane, or a result the worker reports,
  closes the open question.
- `task add` takes `--task` or `--analyze`, never both, plus `--model`, `--worker` and `--after <id>` (repeatable). An
  unknown model or worker, `--worker opencode` for an analysis or a Gitea project, or an unknown `--after` task is a
  usage error (exit 2) and queues nothing.
- An analysis on a `loop: fanin` project ends by its report: an issue or nothing ends it `done` and closes its
  pane; blocked notifies you and keeps the pane; a PR is gated and waits for your `go`, nothing merges.
- `task import` reads one `model|task` per line; `#` lines and blank lines are skipped but counted. Each task waits
  for the one before it. Importing a changed file again adds only the new lines.
- `go` records the go as an `owner_go` event; a task outside its gates is refused.
- `wait` prints `<seq> <type> <project> <task> <run>: <detail>`. `--after <seq>` starts after that event instead
  of waiting only for new ones, so a loop of `wait --after <last seq>` misses nothing.

## Holds and pauses

A hold stops a task or project until the owner lifts it; `fanin status` lists the active holds under `open:` with
their ids, and the board of the web UI shows them with a Lift button. Fix the cause the hold names, then
`fanin hold lift <id>`. A refused lift names the reason.

`fanin pause <project>` keeps the project from starting its next worker, once; workers already running finish.
`fanin resume <project>` clears it.

## Stuck and spent workers

A worker that stalls gets one nudge; at the next stall fanin ends the run and starts a fresh one. The fresh
run's prompt carries a handover: state, open items, branch, last errors and the pane tail. A spent budget
(`budgets` in the config) does the same, and so does a run still working `worker_max_wall` after its start. A worker that stops without a result is waited for while a background
shell runs, then nudged to continue, then to push. After that its task is needs-owner, with the unpushed commits
and the last words in the detail; its pane stays open. Once a task has used `budgets.max_runs_per_task` runs, a
spent budget pauses it and a second stall sets it needs-owner.

## Logs

`fanind` logs to stderr, so under systemd `journalctl -u fanind` reads them (`-f` follows, `--since "1 hour ago"`
narrows). `log.format: json` (default) prints one object per line; `jq` filters it:

```sh
journalctl -u fanind -o cat | jq 'select(.project == "alpha")'
journalctl -u fanind -o cat | jq 'select(.task_id == "t1")'
```

Lines carry `task_id`, `run_id` and `project` where they apply; `log.level` sets the detail. Tokens are never
logged.

## Web UI

Open `http://<listen address>/ui/` in a browser. The inbox lists open owner questions and answers them with one
click, the board shows each project's done, next, waiting, hold and decision tasks with a Lift button for holds, and
the hosts view lists the workers per host (an unreachable host is one warning line). The page updates live. Commands
ask for the owner token once per tab and keep it in that tab only.

Reading needs no token: every `GET` (the UI, `/api/v1/snapshot`, `events`, `questions`, `tasks`, `wait`,
`/metrics`) answers anyone who reaches `listen`, with every task, question, hold and event detail. Commands need
the owner token, reports the run token and webhooks their signature. To open the UI in the LAN, set `listen` to the
host's LAN address (workers then reach the daemon there too) or put a reverse proxy in front; serve the proxy over
HTTPS, since the UI sends the owner token with every command.

## Owner MCP server

`fanin mcp` serves the owner tools over stdio, so an agent session answers questions without a shell. Register it
in the session's MCP config with `FANIN_URL` and `FANIN_TOKEN` in its environment:

```json
{"mcpServers": {"fanin": {"command": "fanin", "args": ["mcp"], "env": {"FANIN_URL": "http://127.0.0.1:8470", "FANIN_TOKEN": "<owner token>"}}}}
```

- `fanin_questions`: the open questions with their options.
- `fanin_answer` (`task`, `answer`): same rules as `fanin answer`; an invalid option is refused with the options.
- `fanin_go` (`task`): same as `fanin go`.
- `fanin_status` (optional `project`): tasks, runs, questions, holds, locks and the newest events, as JSON.

A daemon that cannot be reached, or a refused call, is a tool error that names the cause, never a success message.
`fanin_task_add` follows once the daemon has a task-creation endpoint.

## Worker MCP server

`fanin worker-mcp` serves the worker tools over stdio. fanind sets `FANIN_URL`, `FANIN_RUN_ID` and `FANIN_RUN_TOKEN`
for each run; without them the command refuses to start and names the missing variable.

- `fanin_report` (`kind`, plus `pr`, `branch`, `issue`, `issue_url` or `text`): the run's result. `kind` is one of
  `done_pr`, `pushed`, `committed`, `issue`, `nothing`, `blocked`; texts are kept as one line of at most 400
  characters.
- `fanin_progress` (optional `text`): the heartbeat of a long run.

A report counts only with the token of the task's current run; a report from an older run is refused and logged.
A worker without MCP writes `result.json` (the same fields plus `"token"`) into its run directory instead; a file
that cannot be parsed is an error, never a result.

`fanin report <kind> [--pr n] [--branch b] [--issue n] [--issue-url u] [--text t]` is the shell form of both. With
`--dir <run directory>` it writes `result.json` atomically (the run token from `FANIN_RUN_TOKEN`); without it, with
`FANIN_URL`, `FANIN_RUN_ID` and `FANIN_RUN_TOKEN` set, it sends the report to the daemon. An unknown kind or a missing
field is exit 2 and writes nothing; a refused token or an unwritable directory is exit 1.

`fanin worker-usage` is a Claude worker's status line. fanind starts each Claude worker with a settings file whose
`statusLine` runs it with the run's `mcp.json`, where it finds the run token, so a worker shows `fanin <run> · context <n>%` instead of your own status line. It sends the
tokens and the context share of every response to the daemon, which adds them up for `budgets.max_tokens_per_run`
and checks the share against `budgets.rollover_pct`. Each response counts its whole context again, as tokens
processed; an update Claude Code cancels is not counted, so the total runs a little low. opencode workers report
no usage.

## Metrics, overview and doctor

- `GET /metrics` on the daemon's `listen` address serves Prometheus text: `fanin_tasks` and `fanin_runs` per
  project and state, and the counters `fanin_runs_started_total`, `fanin_nudges_total`, `fanin_merges_total` and
  `fanin_adapter_errors_total`. It needs no token: bind `listen` to a trusted interface. An unreadable store is a
  500.
- `fanin overview [project...]` prints each project's board: done this run, next, waiting for owner, ready for
  go (PR number and gate verdict line), holds with the command that lifts them, and decisions.
- `fanin doctor` checks config, store, herdr, the forge login and the `fanin/review` branch protection per project,
  and the systemd unit. It prints a table and exits 1 when any row says `FAIL`. A `skip` row is a check that cannot
  run on this host. No token is printed.
- Every log line about a task carries `project`, `task_id` and `run_id`.
