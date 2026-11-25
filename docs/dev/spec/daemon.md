# Daemon and status

Status: implemented in `cmd/fanind`, `internal/config` and `cmd/fanin/cmd_status.go`. Direction:
[concept](../concept.md), migration step 3. The shadow importer of that step is not built yet (see below).

## Config (`internal/config`)

`config.Load(dir)` reads the config directory; the files, keys, defaults and rules are in [config](config.md),
the user-facing reference in [`docs/user/configuration.md`](../../user/configuration.md). `fanind` uses
`listen`, `store`, `log`, `owner_token_env` and the project names (file-name order).

## fanind

`fanind --config DIR` (or `$FANIN_CONFIG`):

1. Loads the config and builds the `slog` logger it selects. A missing config, an empty owner token variable or a
   listen address it cannot bind refuses to start with a named error and exit 1.
2. Opens the store (`store.Open`) and serves the API (`api.New`) with the configured project order.
3. The root context comes from `signal.NotifyContext` (SIGINT, SIGTERM). On stop it cancels every open request,
   so event streams end, then waits up to `ShutdownGrace` (10 s, on the injected clock) for them and closes the
   store. A stop is a clean exit (0) and writes nothing to the store: no task or run changes state.

## Snapshot additions

The snapshot ([api](api.md)) carries what `fanin status` needs:

- `projects`: the configured projects in file-name order, then any other project the store names, sorted.
- `locks`: every project lock row. `stale` is true when the lease ran out at the server's clock or the holder is
  neither an open task nor an unfinished run of one: a lock left behind by a dead loop.
- `latest`: the newest event of each project.

## fanin status (SCENARIOS ST-1)

`fanin --daemon-url URL status [project...]` reads the snapshot and prints one line per project, all projects in
config order without arguments, the named ones in the given order otherwise (an unknown name is an error):

```
alpha  loop=active  worker=1  merge=held by r1  last: 2026-03-04T05:06:07Z run_started a1: run 1  open: ready 1, blocked 1
gamma  loop=idle  worker=0  merge=stale (r0)  last: 2026-03-04T05:06:07Z hold_set: merge hold h2: hook failed  open: needs-owner 1, ci hold (fix g9)
```

| Field | Meaning |
|---|---|
| `loop` | `active` while a task is `running`, `gating` or `post_merge`, else `idle` |
| `worker` | unfinished runs of the project's open tasks |
| `merge` | `free`, `held by <holder>` or `stale (<holder>)` |
| `last` | the project's newest event: time, type, task and detail; `-` without one |
| `open` | non-zero counts of ready, blocked (a dependency is still open), paused, needs-owner and failed tasks, then each active hold (`ci hold (fix <task>)`); `-` when nothing is open |

An unreachable daemon or an unknown state in the snapshot is an error with exit 1, never an empty status.

## Not built yet: the shadow importer

Step 3 also has `internal/shadow` follow the bash orchestrator's `state/*.md`, lock and hold files and map them to
tasks, runs and holds, read-only. It waits for anonymised fixtures of the bash state, which the importer's tests
compare byte for byte before and after a run.
