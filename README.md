<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo/fanin-lockup-horizontal-dark.svg">
    <img alt="fanin" src="docs/brand/logo/fanin-lockup-horizontal-light.svg" width="360">
  </picture>
</p>

<p align="center"><strong>Fan out to coding agents. Fan in to main.</strong></p>

fanin runs coding agents on your projects' task lists and brings their work back to `main`. For each task it
starts one fresh worker (Claude Code, opencode, …) in its own [herdr](https://github.com/herdrdev/herdr) pane and
worktree, watches it until it reports, and turns the result into one reviewable pull request. CI and a review
check gate the PR; the forge merges it when it is green. You are asked only when a decision is really yours.

- `fanind`: a Go daemon with a scheduler, a SQLite store and adapters for worker runtimes and forges.
- `fanin`: the CLI to add tasks, answer questions and see what runs.

## Status

**Concept, nothing runnable yet.** The two binaries are stubs. The design is in
[`docs/dev/concept.md`](docs/dev/concept.md), the acceptance scenarios in
[`docs/dev/scenarios.md`](docs/dev/scenarios.md) and the coding rules in
[`docs/dev/guidelines.md`](docs/dev/guidelines.md).

## Development

You need Go (the version in `go.mod`) and `make`.

```sh
make ci    # gofmt, go vet, go build, go test, private-data checks
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md).

## License

Apache License 2.0, see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE). The brand files in
[`docs/brand/`](docs/brand/) use the same license; the Geist fonts there stay under the SIL Open Font License.
