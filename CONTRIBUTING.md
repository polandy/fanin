# Contributing to fanin

fanin is at the concept stage. Issues and discussion about the design in
[`docs/dev/concept.md`](docs/dev/concept.md) are welcome; code contributions make sense once the first packages
land.

## Workflow

- Never commit to `main`. Open a branch `<type>/<slug>` and a pull request; `main` is protected and only takes
  squash merges of green PRs.
- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org/) with the types `feat`,
  `fix`, `docs`, `chore`, `refactor`, `test` and `ci`. The PR title becomes the squash subject.
- Never rebase or force-push a branch that is pushed. To catch up, merge `origin/main` into it.
- Run `make ci` before every push. CI runs the same checks.
- A behaviour change starts with its scenario in [`docs/dev/scenarios.md`](docs/dev/scenarios.md) and a failing
  test. The full rules are in [`docs/dev/guidelines.md`](docs/dev/guidelines.md).

## Docs

Documentation lives in `docs/user/` (install, operation, configuration) and `docs/dev/` (concept, specs, ADRs,
guidelines). `docs/brand/` holds only the logo files.

## No private data

This repository is public. Do not commit hostnames, IP addresses, LAN names, home directory paths, personal
names or email addresses. Use generic examples such as `example.com`, `/path/to/repo` and `host-a`.

`scripts/check-private-data.sh` (run by `make ci` and CI) rejects private IP ranges, home paths, LAN host names
and email addresses in the tree and in the history. Terms that are private themselves, such as your own host
names, go one per line into `.private-terms` (gitignored) or into `FANIN_PRIVATE_TERMS`, so the check finds them
without publishing them.

## License

By contributing you agree that your contribution is licensed under the Apache License 2.0.
