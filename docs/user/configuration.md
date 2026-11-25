# Configuration

`fanind` reads one directory, passed with `--config` or `$FANIN_CONFIG`. Every file is checked against a JSON
Schema at start: an unknown key, a wrong type, a retired key or a name that points nowhere stops `fanind` with
the file, the key path, the rule and the fix. A full example is in `config/examples/`; the keys and their
defaults are listed below.

```
config/
  fanin.yaml          # daemon settings and project defaults (required)
  hosts.yaml          # remote worker hosts
  forges.yaml         # GitHub and Gitea forges
  worker-kinds.yaml   # claude, opencode, headless: models and the picker
  triggers.yaml       # reserved; leave it empty or out
  projects/
    alpha.yaml        # one file per project; the file name is the project name
```

## fanin.yaml

```yaml
listen: 127.0.0.1:7420              # keep it on loopback or a private network
store: /var/lib/fanin/fanin.db      # SQLite file; a relative path is relative to this directory
log:
  format: json                      # json (default) or text
  level: info                       # debug, info (default), warn or error
owner_token_env: FANIN_OWNER_TOKEN  # the variable that holds the owner token
notify:                             # optional push channel for owner questions
  kind: signal                      # log (default) or signal
  signal_number: "+15550100"        # your own number: signal-cli account and the only recipient
  ui_url: https://fanin.example/ui  # base URL of the web UI; the note links to <ui_url>/#/task/<id>
  prefix: "[nuc/fanin]"             # optional; default [<hostname>/fanin]
defaults:                           # project settings every project inherits
  review_skill: /pr-review
```

With `notify.kind: signal`, `fanind` sends a Signal note to your own number whenever a task asks a question: the
prefix, the question and the link to the task page, at most 600 characters. `signal-cli` must be registered for
that number; `signal_cli` names another program. A failed send is logged as a `notify_failed` event and the
question stays open. The number is never logged. With the default `log`, the note only goes to the log.

## hosts.yaml and forges.yaml

```yaml
hosts:
  host-a:
    address: host-a.example.com     # `local` (this host) is built in; do not list it
```

```yaml
forges:
  github:
    kind: github
    token_env: GH_TOKEN
  gitea:
    kind: gitea
    url: https://git.example.com
    token_env: GITEA_TOKEN
```

## worker-kinds.yaml

```yaml
kinds:
  claude:
    runtime: herdr
    models: [opus, sonnet]
    default_model: sonnet
  opencode:
    runtime: herdr                  # no models: the sandbox fixes the model
picker:
  kind: claude
  model: sonnet
```

The worker model is the owner's choice, else the project's `model`, else the picker's, else the kind's
`default_model`.

## projects/<name>.yaml

```yaml
path: /path/to/repo
host: local
forge: github
max_parallel: 2                     # runs at once; required, at least 1
base: main
fallback: opencode
budgets:
  fix_rounds_per_run: 2
```

### Project keys

Project-only keys (not allowed under `defaults`):

| Key | Type | Default | Notes |
|---|---|---|---|
| `path` | absolute path | required | the clone on its host |
| `host` | name | required | `local` or a host of `hosts.yaml` |
| `forge` | name | required | a forge of `forges.yaml` |
| `max_parallel` | integer ≥ 1 | required | runs at once |
| `fallback` | kind name | none | kind used when `worker_kind` is unavailable |
| `post_merge_hook` | string | none | command run after a merge, under the project lock |
| `pre_go_check` | string | none | command whose output goes with a ready PR before the owner's go |

Settings, in a project file or under `defaults`:

| Key | Type | Default | Notes |
|---|---|---|---|
| `base` | string | `main` | the branch PRs aim at |
| `worker_kind` | kind name | `claude` | |
| `model` | string | none | checked against the kind's `models` |
| `review_skill` | string | `/pr-review` | |
| `review_model` | string | `sonnet` | model of the review run |
| `review_timeout` | duration | `30m` | longest review run |
| `oc_merge` | `auto` \| `manual` | `auto` | a green opencode PR merges or waits for the go |
| `loop` | `bash` \| `fanin` | `bash` | `fanin` lets `fanind` start the workers; local projects only for now |
| `stall_after` | duration | `15m` | no heartbeat for this long is a stall |
| `worker_timeout` | duration | `45m` | no heartbeat or result for this long ends the run |
| `budgets.fix_rounds_per_run` | integer ≥ 0 | `2` | |
| `budgets.rollover_pct` | 1–100 | `75` | context use that rolls over to a fresh run |
| `budgets.max_tokens_per_run` | integer ≥ 1 | `3000000` | |
| `budgets.max_runs_per_task` | integer ≥ 1 | `3` | |
| `gate.github.watch_timeout` | duration | `30m` | longest wait on pending checks |
| `gate.github.settle` | duration | `20s` | |
| `gate.github.max_waits` | integer ≥ 1 | `12` | |
| `gate.github.max_watch_failures` | integer ≥ 0 | `6` | failed waits that do not use up a wait |
| `gate.gitea.poll_interval` | duration | `60s` | |
| `gate.gitea.noci_after_polls` | integer ≥ 1 | `5` | polls without a status before `NOCI` |
| `gate.gitea.max_polls` | integer ≥ 1 | `40` | polls of running checks before RED |

A duration is a Go duration string with a non-zero leading number (`90s`, `15m`, `1h30m`).

A project value overrides `defaults` in `fanin.yaml`, which overrides the built-in default.

## Secrets

The files name secrets, never hold them: `owner_token_env` and `token_env` are upper-case variable names.
`fanind` refuses to start while the owner token variable is empty.

Under systemd the variables come from the unit's `EnvironmentFile` (`/etc/fanin/fanind.env`, mode 0600), see
[Install](install.md#secrets). `fanind` reads nothing else from the environment but `FANIN_CONFIG` as the
fallback of `--config`.

## Listening

Worker hosts report results to the API at `listen`. For workers on other hosts set it to a private address of
the control host (never a public one) and make sure each worker host reaches that port.

## Retired keys

`lanes` (use `max_parallel`), `machine` (use `host`), `oc-merge` (use `oc_merge`) and `review` (use
`review_skill`) are refused with that hint.

## Check the config

`fanin config validate [DIR]` loads the directory (`DIR`, else `--config`, else `$FANIN_CONFIG`) and prints
`ok` with the project count. An invalid config exits 1 and names the file, the key path, the rule and the fix.

`fanin config effective [--json] <project>` prints what `fanind` would use for one project: the daemon
settings, the project settings with every default applied, its host, its forge and its worker kind. Each value
carries a comment with where it comes from:

```yaml
settings:
  base: main                        # default
  review_skill: /pr-review          # fanin.yaml:9
  max_parallel: 1                   # projects/beta.yaml:5
```

`projects/beta.yaml:5` is a line of that project file, `fanin.yaml:9` a line under `defaults` (or a daemon key),
`default` a built-in default, `built-in` the `local` host and `environment` whether a secret variable is set.
Secrets show as `{env, set}`; their values are never printed. A project that does not exist exits 2 and lists
the known ones.

With `--json` the output is one JSON object with sorted keys; the same origins are under `origins`, keyed by
the path of the value (`/settings/max_parallel`). The order of keys and the text are the same on every run, so
the output can be saved and compared with `diff`.
