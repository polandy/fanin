# Daemon and status

Status: implemented in `cmd/fanind`, `internal/config` and `cmd/fanin/cmd_status.go`. Direction:
[concept](../concept.md), migration step 3. The shadow importer of that step is in [shadow](shadow.md).

## Config (`internal/config`)

`config.Load(dir)` reads the config directory; the files, keys, defaults and rules are in [config](config.md),
the user-facing reference in [`docs/user/configuration.md`](../../user/configuration.md). `fanind` uses
`listen`, `store`, `log`, `owner_token_env` and the project names (file-name order).

## fanind

`fanind --config DIR [--shadow STATE_DIR]` (or `$FANIN_CONFIG`, `$FANIN_SHADOW`):

1. Loads the config and builds the `slog` logger it selects. A missing config, an empty owner token variable or a
   listen address it cannot bind refuses to start with a named error and exit 1.
2. Opens the store (`store.Open`). With `--shadow` it imports the bash state directory once, a failed import
   refusing to start, and keeps importing it until the stop ([shadow](shadow.md)).
3. When a project has `loop: fanin`, runs the [scheduler](sched.md) with the herdr runtime and the headless
   picker. A `loop: fanin` project on a remote host refuses to start. The per-run files go to `runs/` next to
   the store. The shadow import skips these projects. The forges these projects name read their PR gates and
   base CI (`Deps.Gates`, `Deps.BaseCI`): GitHub through `gh`, Gitea and Forgejo over their API with the token of
   `token_env`, which must be set or fanind refuses to start. The repo (`owner/name`) is read from the `origin`
   remote of the project's checkout on every read, and an unreadable origin is an error, never a verdict. The same
   forges merge on the owner's go (`Deps.Merger`): the PR is read, a merged one is success, else it is
   squash-merged with the subject `<title> (#n)`, GitHub retrying a network failure (MG-6). The post-merge hook
   (`hook.New`) and the pre-go check (`hook.NewPreGo`) run through the same runner, in `state/` next to the
   store (`Deps.StateDir`), which fanind creates at start; one it cannot create refuses the start. A scheduler
   that stops on an error (an unreadable store while it resumes) stops fanind with `fanind: scheduler stopped`
   and exit 1.
4. Serves the API (`api.New`) with the configured project order.
5. The root context comes from `signal.NotifyContext` (SIGINT, SIGTERM). On stop it cancels every open request,
   so event streams end, then waits up to `ShutdownGrace` (10 s, on the injected clock) for them, stops the
   scheduler and closes the store. A stop is a clean exit (0) and writes nothing to the store: no task or run changes state.

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
