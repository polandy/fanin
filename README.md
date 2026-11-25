<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo/fanin-lockup-horizontal-dark.svg">
    <img alt="fanin" src="docs/brand/logo/fanin-lockup-horizontal-light.svg" width="360">
  </picture>
</p>

<p align="center"><strong>Fan out to coding agents. Fan in to main.</strong></p>

<p align="center">
  <a href="https://github.com/polandy/fanin/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/polandy/fanin/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/polandy/fanin"></a>
  <img alt="Status: concept" src="https://img.shields.io/badge/status-concept-orange">
</p>

fanin runs coding agents on your projects' task lists and brings their work back to `main`. For each task it
starts one fresh worker (Claude Code, opencode, …) in its own [herdr](https://github.com/herdrdev/herdr) pane and
worktree, watches it until it reports, and turns the result into one reviewable pull request.

> **Status: concept.** `fanind` serves its store over a read-only API and `fanin status` reads it; no workers run
> yet. See [Status](#status).

## Why fanin

- **One task, one worker, one PR.** Every task gets a fresh agent in an isolated worktree and ends as a single
  pull request you can review.
- **The forge decides what merges.** Branch protection, required checks and the forge's merge queue gate the PR.
  A review check counts like CI. fanin adds no merge logic of its own.
- **You are asked only when it is yours to decide.** Questions come with options and a recommendation, and can be
  answered from the CLI, an agent session or a UI.
- **No agent session has to stay open.** A daemon drives the work; state lives in SQLite, so a restart picks up
  where it stopped.
- **You can watch and take over.** Workers run in terminal panes, not behind a curtain.

## How it works

```mermaid
flowchart LR
    T[Task list] --> F{fanind}
    F -->|fan out| W1[Worker 1<br/>own worktree]
    F -->|fan out| W2[Worker 2<br/>own worktree]
    F -->|fan out| W3[Worker 3<br/>own worktree]
    W1 --> P1[PR]
    W2 --> P2[PR]
    W3 --> P3[PR]
    P1 --> G[CI + review<br/>gate]
    P2 --> G
    P3 --> G
    G -->|fan in| M[(main)]
```

- `fanind`, a Go daemon, holds a scheduler, a SQLite store and adapters for worker runtimes and forges.
- `fanin`, the CLI, adds tasks, answers questions and shows what runs.
- Workers report a structured result through a small MCP tool; pane text is read only to spot dialogs and stalls.
- A review run posts its verdict as a commit status that branch protection requires, next to CI.
- On GitHub the PR is set to auto-merge; on Gitea or Forgejo it merges when checks succeed.
- Runs are bounded: a spent worker is replaced by a fresh one with a structured handover.

## Status

The plan below is built. The bash loops are retired project by project, following the
[cutover runbook](docs/dev/runbooks/cutover.md).

- [x] Behaviour of the existing bash orchestrator written down as acceptance scenarios
- [x] Config as validated YAML
- [x] Shadow store: `fanind` follows the old state and fills SQLite
- [x] Owner channel: questions answered through CLI, MCP and UI
- [x] New task shapes: analysis tasks end to end (alerts follow)
- [x] The PR path moves to fanin, project by project
- [x] The bash loops are retired

The full plan is in [`docs/dev/concept.md`](docs/dev/concept.md#migration).

## Documentation

- [Concept and decisions](docs/dev/concept.md)
- [Acceptance scenarios](docs/dev/scenarios.md)
- [Cutover runbook](docs/dev/runbooks/cutover.md)
- [Coding guidelines](docs/dev/guidelines.md)
- [Configuration](docs/user/configuration.md) and [operation](docs/user/operation.md)
- [Brand files](docs/brand/README.md)

## Contributing

You need Go (the version in `go.mod`), `make` and `shellcheck`.

```sh
make ci    # gofmt, shellcheck, go vet, go build, go test, private-data checks
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md).

## License

Apache License 2.0, see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE). The brand files in
[`docs/brand/`](docs/brand/) use the same license; the Geist fonts there stay under the SIL Open Font License.
