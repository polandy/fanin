# Configuration

`fanind` reads one directory, passed with `--config` or `$FANIN_CONFIG`. Every file is checked against a JSON
Schema at start: an unknown key, a wrong type, a retired key or a name that points nowhere stops `fanind` with
the file, the key path, the rule and the fix. A full example is in `config/examples/`; every key and default is
listed in [the config spec](../dev/spec/config.md).

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
store: fanin.db                     # SQLite file; relative to this directory
log:
  format: json                      # json (default) or text
  level: info                       # debug, info (default), warn or error
owner_token_env: FANIN_OWNER_TOKEN  # the variable that holds the owner token
defaults:                           # project settings every project inherits
  review_skill: /pr-review
```

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

A project value overrides `defaults` in `fanin.yaml`, which overrides the built-in default.

## Secrets

The files name secrets, never hold them: `owner_token_env` and `token_env` are upper-case variable names.
`fanind` refuses to start while the owner token variable is empty.

## Retired keys

`lanes` (use `max_parallel`), `machine` (use `host`), `oc-merge` (use `oc_merge`) and `review` (use
`review_skill`) are refused with that hint.
