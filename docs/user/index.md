# fanin user documentation

fanin is at an early stage: `fanind` starts, keeps its SQLite store and serves the read-only HTTP API, and
`fanin status` prints one line per project. It does not run workers yet.

- [Install](install.md): build, directories, the systemd unit, secrets, branch protection
- [Configuration](configuration.md): the config directory, `fanin config validate` and `fanin config effective`
- [Pre-go check](pre-go-check.md): `pre_go_check`, the command whose output goes with a ready PR
- [Operation](operation.md): starting and stopping `fanind`, `fanin status`, answers, holds, pauses, logs

For the design, see the [README](../../README.md).
