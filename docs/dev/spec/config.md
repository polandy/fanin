# Config

Status: implemented in `internal/config`, schemas in `config/schema/`, example tree in `config/examples/`.
Direction: [concept](../concept.md) (Configuration, C-CONFIG). Scenarios: ST-4, PK-5, LN-2, GH-7, GT-4, GT-10,
IN-1 to IN-5, DR-1.

## The directory

`config.Load(dir)` reads one directory:

| File | Schema | Required | Holds |
|---|---|---|---|
| `fanin.yaml` | `fanin.schema.json` | yes | daemon settings and the project defaults |
| `hosts.yaml` | `hosts.schema.json` | no | remote worker hosts |
| `forges.yaml` | `forges.schema.json` | no | forges and their token names |
| `worker-kinds.yaml` | `worker-kinds.schema.json` | no | worker kinds, their models and the picker |
| `triggers.yaml` | `triggers.schema.json` | no | webhooks, cron entries and the release target ([intake](intake.md)) |
| `projects/<name>.yaml` | `project.schema.json` | no | one project; the file name is the project name |

A project file must end in `.yaml`; a `.yml` one is an error. A missing optional file counts as empty, but every reference must resolve: a project names a forge, so
`forges.yaml` is needed as soon as there is a project. Any other `*.yaml` or `*.yml` beside them is an error.
Projects are listed (and `fanin status` shows them) in file-name order.

## Loading

1. Each file is parsed as YAML. Retired keys (`internal/config/keys.go`) are refused first, with their hint.
2. The file is validated against its embedded schema (`config/schema/*.schema.json`, embedded by the Go package
   `config/schema`). Closed objects reject unknown keys.
3. It is decoded with `KnownFields(true)`.
4. Each project is merged: schema defaults, then `defaults` of `fanin.yaml`, then the project file. Maps merge
   key by key; any other value replaces. The schema is the only place a default value lives.
5. References are checked: host, forge, worker kind, fallback kind and models; in `triggers.yaml` the projects,
   the models of their worker kinds and the release section.

Errors: `config.ErrRead` for an unreadable directory or `fanin.yaml`; `config.ErrInvalid` for everything else.
The message names the file, the JSON pointer, the rule (a schema keyword path or one of `file-known`,
`host-known`, `forge-known`, `kind-known`, `model-known`, `project-known`, `release-known`, `retired-key`) and
the fix. A value that breaks the
secret-name rule is not repeated, since it may be a pasted token.

## fanin.yaml

| Key | Type | Default | Notes |
|---|---|---|---|
| `listen` | string | required | host:port of the HTTP API |
| `store` | string | required | SQLite file; relative to the config directory |
| `log.format` | `json` \| `text` | `json` | |
| `log.level` | `debug` \| `info` \| `warn` \| `error` | `info` | |
| `owner_token_env` | secret name | required | variable that holds the owner token |
| `notify.kind` | `log` \| `signal` | `log` | push channel for owner questions, see [notify](notify.md) |
| `notify.ui_url` | URI | none | UI base URL for the task link; required with `signal` |
| `notify.signal_number` | `+<6-15 digits>` | none | the owner's own number; required with `signal`; never echoed in an error |
| `notify.prefix` | string | `[<hostname>/fanin]` | starts every note |
| `notify.signal_cli` | string | `signal-cli` | the program |
| `defaults` | settings | `{}` | any key of the project settings below, except the project-only ones |

## hosts.yaml

`hosts.<name>.address` (string, required): the ssh destination. `local`, the host `fanind` runs on, is built in
and may not be listed.

## forges.yaml

| Key | Type | Default | Notes |
|---|---|---|---|
| `forges.<name>.kind` | `github` \| `gitea` | required | |
| `forges.<name>.url` | `http(s)://…` | none | required for `gitea` |
| `forges.<name>.token_env` | secret name | required | variable that holds the forge token |

## worker-kinds.yaml

| Key | Type | Default | Notes |
|---|---|---|---|
| `kinds.<name>.runtime` | `herdr` \| `headless` | required | |
| `kinds.<name>.models` | list of strings | none | without it the kind takes no model (opencode: the sandbox fixes it) |
| `kinds.<name>.default_model` | string | none | one of `models`; needs `models` |
| `picker.kind` | kind name | required with `picker` | |
| `picker.model` | string | required with `picker` | one of the kind's `models` |

`Config.WorkerModel(project, owner, picked)` gives the worker model (PK-5): the owner's, else the project's
`model`, else the picked one, else the kind's `default_model`; `""` for a kind without models. A model outside
the kind's `models` is `config.ErrModel`.

## Project settings

Project-only keys (not allowed under `defaults`):

| Key | Type | Default | Notes |
|---|---|---|---|
| `path` | absolute path | required | the clone on its host |
| `host` | name | required | `local` or a host of `hosts.yaml` |
| `forge` | name | required | a forge of `forges.yaml` |
| `max_parallel` | integer ≥ 1 | required | runs at once; replaces lanes (LN-2) |
| `fallback` | kind name | none | kind used when `worker_kind` is unavailable |
| `post_merge_hook` | string | none | command run after a merge, under the project lock |
| `pre_go_check` | string | none | command whose output goes with a ready PR before the owner's go |
| `worktree_dir` | absolute path | checkout's parent | where fanind creates each run's worktree (SP-12) |

Settings, in a project file or under `defaults`:

| Key | Type | Default | Notes |
|---|---|---|---|
| `base` | string | `main` | the branch PRs aim at |
| `worker_kind` | kind name | `claude` | |
| `model` | string | none | checked against the kind's `models` |
| `review_skill` | string | `/pr-review` | |
| `review_model` | string | `sonnet` | model of the review run |
| `review_timeout` | duration | `30m` | longest review run (OC-3) |
| `oc_merge` | `auto` \| `manual` | `auto` | a green opencode PR merges or waits for the go (OC-7) |
| `loop` | `bash` \| `fanin` | `bash` | who drives the workers: the bash orchestrator or `fanind` ([sched](sched.md)) |
| `stall_after` | duration | `15m` | no heartbeat for this long is a stall (AW-4) |
| `worker_timeout` | duration | `45m` | no heartbeat or result for this long ends the run (WD-1) |
| `worker_max_wall` | duration | `90m` | a working run this long past its start hands over to a fresh run (WD-5) |
| `budgets.fix_rounds_per_run` | integer ≥ 0 | `2` | C-BUDGET |
| `budgets.rollover_pct` | 1–100 | `75` | context use that rolls over to a fresh run |
| `budgets.max_tokens_per_run` | integer ≥ 1 | `3000000` | |
| `budgets.max_runs_per_task` | integer ≥ 1 | `3` | |
| `merge.lock_wait` | duration | `1h` | a merge waits this long for the project lock, then goes to the owner (MG-2) |
| `merge.lock_lease` | duration | `10m` | lease of the merge lock, renewed while held; a dead holder frees it after this (MG-3) |
| `gate.github.watch_timeout` | duration | `30m` | longest wait on pending checks (GH-7) |
| `gate.github.settle` | duration | `20s` | pause before the re-read once pending checks settled |
| `gate.github.max_waits` | integer ≥ 1 | `12` | pending waits before RED; see [`gate.md`](gate.md) |
| `gate.github.max_watch_failures` | integer ≥ 0 | `6` | failed waits that do not use up a wait |
| `gate.gitea.poll_interval` | duration | `60s` | (GT-4) |
| `gate.gitea.noci_after_polls` | integer ≥ 1 | `5` | polls without a status before `NOCI` |
| `gate.gitea.max_polls` | integer ≥ 1 | `40` | polls of running checks before RED |

A duration is a Go duration string with a non-zero leading number (`90s`, `15m`, `1h30m`).

## triggers.yaml

| Key | Type | Default | Notes |
|---|---|---|---|
| `webhooks.<name>.kind` | `forge` \| `alert` \| `release` | required | served at `POST /api/v1/webhooks/<name>` |
| `webhooks.<name>.project` | project | required | the task's project; for `release` the released project |
| `webhooks.<name>.secret_env` | variable name | required | holds the HMAC secret; unset refuses the start |
| `webhooks.<name>.label` | string | none | required for `forge`, refused otherwise |
| `webhooks.<name>.model` | string | none | one of the project's kind's `models` |
| `cron.<name>.project` | project | required | |
| `cron.<name>.every` | duration | required | the first run is due one interval after the entry is first armed |
| `cron.<name>.task`, `.analyze` | string | one required | owner task or owner question |
| `cron.<name>.model` | string | none | as above |
| `release.project` | project | required with `release` | the config repo; its `base` is read |
| `release.compose_glob` | git pathspec | `modules/*/docker-compose.yml` | where the compose modules live |
| `release.registry` | string | `ghcr.io` | the image is `<registry>/<owner>/<repo>` |
| `release.model` | string | `sonnet` | model of the bump task; must be a model of the target's kind |

A `release` webhook needs the `release` section (`release-known`). Every named project must exist
(`project-known`) and every model, defaults included, must be one of its kind's `models` (`model-known`).

## Retired keys

| Key | Hint |
|---|---|
| `lanes`, `lane` | slots replace lanes: set `max_parallel` |
| `machine` | use `host` |
| `oc-merge` | use `oc_merge` |
| `review` | use `review_skill` |

## Secrets and the effective config

The config holds secret names only (`owner_token_env`, `token_env`; upper-case variable names).
`Config.Effective(project, secrets)` resolves one project with its host, forge and worker kind; each secret
appears as `{env, set}`, where `set` comes from the `config.Secrets` lookup. `Effective.WriteYAML` prints it and
never holds a value (GT-10).

`fanin config effective [--json] <project>` prints it through `Config.Explain`: every value gets a line comment
with its origin (`projects/x.yaml:5`, `fanin.yaml:9`, `hosts.yaml:3`, `default`, `built-in`, `environment`).
`Load` records the line of every key of every file for this. Keys follow the struct order, JSON keys are sorted
and carry the origins by JSON pointer under `origins`. An unknown project is a usage error (exit 2);
`fanin config validate [DIR]` exits 1 on an invalid config (ST-4).

## schema-check

`make schema-check` (part of `make ci`) runs `go run ./internal/config/cmd/schemacheck config/examples`, which
loads each directory it is given and exits 1 on the first error.
