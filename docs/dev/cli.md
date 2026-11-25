# fanin CLI: adding a subcommand

`cmd/fanin/main.go` parses the global flags (`--config`, `--daemon-url`, `--token`, falling back to
`FANIN_CONFIG`, `FANIN_URL`, `FANIN_TOKEN`) and dispatches to the registered subcommand. It is never edited to add
a subcommand.

To add one, create `cmd/fanin/cmd_<name>.go` with an `init()` that calls `register(Command{...})`
(see `cmd_version.go`). Return `cli.Usagef(...)` for bad arguments (exit 2) and any other error for a failure
(exit 1); exit codes are constants in `internal/cli`. A bad flag or unknown subcommand is a usage error before
anything runs (PK-7). Use only the stdlib `flag` package.
