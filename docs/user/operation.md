# Operation

## Starting fanind

Under systemd (see [Install](install.md)): `sudo systemctl start|stop|restart fanind` and `systemctl status
fanind`. By hand:

```sh
export FANIN_OWNER_TOKEN=...        # the name set in owner_token_env
fanind --config /path/to/config
```

`fanind` opens (or creates) the store, serves the API on `listen` and logs `fanind: serving` with the address.
It exits with 1 and a message naming the fix when the config is missing or invalid, the token variable is empty,
or the address is taken.

SIGINT or SIGTERM stops it: open requests end, the store is closed and the exit code is 0. A stop changes no task.

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
```

- `answer` refuses free text on a numbered dialog and lists the options; nothing reaches the worker then. When
  the answer cannot be delivered, nothing is recorded and the question stays open.
- `task add` takes `--task` or `--analyze`, never both, plus `--model`, `--worker` and `--after <id>` (repeatable). An
  unknown model or worker, `--worker opencode` for an analysis or a Gitea project, or an unknown `--after` task is a
  usage error (exit 2) and queues nothing.
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
