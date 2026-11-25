# Operation

## Starting fanind

```sh
export FANIN_OWNER_TOKEN=...        # the name set in owner_token_env
fanind --config /path/to/fanin.yaml
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
```

- `answer` refuses free text on a numbered dialog and lists the options; nothing reaches the worker then. When
  the answer cannot be delivered, nothing is recorded and the question stays open.
- `go` records the go as an `owner_go` event; a task outside its gates is refused.
- `wait` prints `<seq> <type> <project> <task> <run>: <detail>`. `--after <seq>` starts after that event instead
  of waiting only for new ones, so a loop of `wait --after <last seq>` misses nothing.

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
