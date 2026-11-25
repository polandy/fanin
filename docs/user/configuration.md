# Configuration

`fanind` reads one YAML file, passed with `--config` or `$FANIN_CONFIG`. It is checked against a JSON Schema at
start: an unknown key, a wrong type or a project listed twice stops `fanind` with the file, the key path and the
rule.

```yaml
listen: 127.0.0.1:7420          # host:port of the HTTP API; keep it on loopback or a private network
store: fanin.db                 # SQLite file; a relative path is taken from this file's directory
log:
  format: json                  # json (default) or text
  level: info                   # debug, info (default), warn or error
owner_token_env: FANIN_OWNER_TOKEN   # the environment variable that holds the owner token
projects:                       # fanin status lists them in this order
  - name: alpha
  - name: beta
```

| Key | Required | Notes |
|---|---|---|
| `listen` | yes | Address the API binds. Read endpoints need no token. |
| `store` | yes | The SQLite database; created on first start. |
| `log.format`, `log.level` | no | Defaults `json` and `info`. |
| `owner_token_env` | yes | Upper-case variable name. The token itself never goes into the file; `fanind` refuses to start while the variable is empty. |
| `projects[].name` | yes | Lower-case letters, digits, `.`, `_` and `-`. |
