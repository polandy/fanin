# fanin coding guidelines

These rules are consolidated from the owner's repos: skipper-cd (Go, the template for everything Go), Stella,
glissando, framehive, honeycomb, JIT-Pack, and the bash orchestrator itself. "All" means every repo that has the
rule; a repo without the rule never contradicts it unless it is listed under Conflicts.

## 1. Shared rules (adopted as is)

### Spec first

- A behaviour change starts in a spec under `docs/dev/`. The acceptance list is [`scenarios.md`](scenarios.md), and every
  scenario ID gets a test.
- An ADR (`docs/dev/adr/NNNN-slug.md`) is written only for a real tradeoff.
- The spec or ADR ships in the same PR as its code. Never merge a spec-only PR ahead of the code.

### Tests

- **Test-first.** A bug fix or a new scenario starts with the failing test. That test is **its own `test:`
  commit**, before the fix.
- **Deterministic.** No `time.Sleep` and no wall-clock or polling waits in tests.
  - Time comes from an injected clock, and completion from a signal (a channel, a fake's callback).
  - An absence assertion needs a positive signal next to it ("X happened, and Y did not"). Prove the test can
    fail.
- **Failure paths are required** for safety-critical code: gate, merge, lock, hold, store, owner answers, and
  every external call. "Unreadable" must never turn into a verdict (SCENARIOS FP-6).

### Errors and strings

- **No magic strings or numbers.** Status words, verdicts, event types, config keys and limits are named
  constants or config, defined once in the package that owns them. No constants grab-bag package.
- **No silently dropped errors.** Never write `_ = f()` on an error that matters. Log it at minimum; better,
  return it wrapped.
- Error messages are actionable: they name the key, the bad value and what to set.

### Comments

- Doc comments on every exported symbol (enforced by revive).
- Comments are short and say what, plus one why, about the code as it is now, never its history. Point to a spec
  or ADR with `// see ADR-0007` or `// see SCENARIOS MG-4`.

### Git

- Never rebase and never force-push. To catch up, merge `origin/main` in.
- Squash merge with a hand-written Conventional Commit subject and body; main stays linear.
- No attribution lines in commits or PR bodies.
- A branch is `<type>/<slug>` in its own worktree. Check `git branch --show-current` before every commit and push.
- The local CI mirror is green before every push.
- Run `/pr-review` on your own PR (docs-only PRs are exempt).

### Docs

- Docs live in `docs/user/` (install, operation, configuration) and `docs/dev/` (specs, ADRs, architecture,
  reviews, runbooks). The only other folder is `docs/brand/` with the logo files.
- `docs/user` never links into `docs/dev`.

### Persistence and config

- Every file write (rendered `state/*.md`, config dumps) is atomic: write a temp file, then rename.
- Unknown config keys are rejected, never ignored.

### Dependencies

- Pinned exactly. Run `go mod tidy` after every change. Each new dependency is justified in the PR body.

## 2. Go specifics (from skipper-cd, adjusted for fanin)

### Toolchain and layout

- **Toolchain.** Use the Go version from `go.mod`, matched by `flake.nix` (`go_1_26` today); `nix develop` is the
  only supported shell.
- **Module.** `github.com/polandy/fanin`. Binaries live in `cmd/fanind` (the daemon) and `cmd/fanin` (the CLI, the
  owner's MCP server, the TUI later).
- **Packages.** Code lives under `internal/` per [`concept.md`](concept.md):
  - config, store, model, sched, intake, runtime (herdr, headless, opencode adapters), forge (github, gitea), gate,
    hook, owner, prompt, render, metrics, api (HTTP + SSE).
  - The only `pkg/` is `pkg/faninmcp` (the worker MCP contract).
  - Package names are nouns; no `util` or `common`.
- **Vendoring.** Commit `vendor/` as skipper does (offline nix builds, `vendorHash = null`). `make vendor-check`
  fails on drift. Never edit vendor code or search in it.

### Errors

- Wrap with `fmt.Errorf("…: %w", err)`; `%v` never carries an error.
- Sentinels (`gate.ErrUnreadable`, `store.ErrLockHeld`, `forge.ErrNetwork`) are defined in the owning package and
  matched with `errors.Is`.
- Typed errors (`*runtime.ExitError{StderrTail}`) are matched with `errors.As`. Never match on error strings.
- No `panic` outside `main` and init-time programmer errors.

### Context and goroutines

- `ctx context.Context` is the first parameter of anything that blocks or calls out, and is never stored in a
  struct.
- The root context comes from `signal.NotifyContext` in `main`. A failure caused by shutdown (`ctx.Err() != nil`)
  is never recorded as a task failure.
- Every goroutine starts through a `safego.Go` helper that recovers and logs panics.
- Shared state is guarded by the store (one writer, transactions), not by ad-hoc mutexes across packages.

### Logging

- `log/slog` only, JSON in production and text in dev, selected by config.
- Every line about a task carries `task_id`, `run_id` and `project`.
- Tokens and credentials are never logged. Forge clients redact them, and tests assert it.

### Time and seams

- `type Clock interface{ Now() time.Time; After(d) <-chan time.Time }` is injected everywhere; tests use a manual
  clock.
- Timers that must outlive a restart are `timers` rows in the store, not `time.After`.
- Every external process (herdr, gh, git, claude, opencode, ssh, nixos-rebuild) goes through a `runtime.Runner`
  interface. Tests use a recording fake that asserts the exact argv, as skipper's `recordingRunner` does.
- Real processes run only in `internal/runtime/exec` and in tests tagged `integration`.

### Store

- SQLite through `modernc.org/sqlite` (pure Go, no cgo, so nix builds stay simple), in WAL mode, with migrations
  embedded and numbered.
- Tests use a real `:memory:` or `t.TempDir()` database, never a mocked store.
- Every state transition is one transaction that also appends its `events` row.

### Config

- YAML via `gopkg.in/yaml.v3` with `KnownFields(true)`, validated against the JSON Schema in
  `config/schema/*.json` (embedded) at load.
- A bad config refuses to start and names the file, the path and the rule.
- Retired keys are listed in `internal/config/keys.go` with a hint.

### Tests

- Stdlib `testing` only, with `go-cmp` allowed for diffs. No testify and no mock generators; fakes are written by
  hand next to the code (`fake_test.go`, shared fakes in `internal/<pkg>/testutil`).
- Table-driven tests with `t.Run(name, …)`. Test names cite their scenario: `TestGate/GH-2_review_older_than_head`.
- `go test -race` in the mirror, plus a plain `-count=1 -shuffle=on` pass.
- Coverage: no global gate. `internal/gate`, `internal/sched`, `internal/store` and `internal/hook` must be at
  least 90 % lines (see Conflicts 9).

### Lint

- golangci-lint v2, pinned in the flake. It runs the default set (errcheck, govet, ineffassign, staticcheck,
  unused) plus errorlint, revive (exported doc comments), gocritic, misspell, nilerr, bodyclose and goconst.
- errcheck excludes only `(*os.File).Close` on read paths and `os.Remove` in cleanup.
- gosec stays off as in skipper; `govulncheck` covers dependencies.
- Format with `gofmt` (no gofumpt).

### HTTP

- The API and SSE use `net/http` with the 1.22+ mux patterns; there are no web frameworks.
- The UI is static files in `embed.FS`, vanilla JS, with no build step (see [`concept.md`](concept.md), owner UI).
- Command endpoints require the owner token. Tests cover a webhook that is refused on them.

### Go style

- Use `any`, not `interface{}`.
- Keep files small and cut by feature. There is no hard line cap; the soft cap is in Conflicts 8.

## 3. Conflicts and recommendations

| # | Conflict | Recommendation for fanin |
|---|---|---|
| 1 | Worktree location: `.claude/worktrees/` (skipper-cd, JIT-Pack) vs `../<repo>-<slug>` (Stella, glissando, framehive, honeycomb) | `../fanin-<slug>`: the majority choice, and it keeps `.claude/` free of checkouts that test globs would pick up |
| 2 | Attribution: banned (glissando, framehive, honeycomb) vs silent | Banned. It matches the current owner instruction |
| 3 | Failing test as its own commit (honeycomb) vs "each commit passes tests" (glissando, JIT-Pack) | The failing test is its own `test:` commit (owner's step-0 list). Only the PR head and the squash must be green; the mirror runs on the head |
| 4 | Commit types: the strict 7 (skipper) vs also `build`/`perf`/`revert` (Stella) vs `build` only from Dependabot | The 7 types (feat, fix, docs, chore, refactor, test, ci); `build:` only from Dependabot |
| 5 | Who merges: agents never (honeycomb) vs per-PR grant (skipper, framehive, glissando) | Per-PR grant from the owner for the fanin repo itself. Note that this differs from fanin's runtime rules: GitHub projects auto-merge on green |
| 6 | Open-PR limit: 1 (JIT-Pack), 1 per session (glissando), 2 (Stella) | One open feature PR per worker; the orchestrator's slot count bounds the total |
| 7 | Spec-only PRs and "ADR before" (framehive, honeycomb) vs same PR / ADR only for a tradeoff (skipper, JIT-Pack) | Specs and ADRs ship with their code. Exception: step-0 style design docs in `docs/dev/` may land alone because they are inputs, not decisions on code |
| 8 | File size: 300 / 350 / 400 / 800 with a ratchet / none | Soft cap of 400 lines per `.go` file (tests excluded), checked by `scripts/check-file-size.sh` with a shrinking exception list |
| 9 | Coverage: 95/90 (honeycomb), 75/90 (JIT-Pack), ≥90 + mutation (framehive), none (skipper, glissando) | No global gate; ≥90 % lines for gate, sched, store and hook, enforced in the mirror. Mutation testing is not adopted for now |
| 10 | Docs: `docs/user` + `docs/dev` (5 repos) vs flat `docs/` + `dev-docs/` (JIT-Pack); ADR naming `NNNN-slug` vs `ADR-NNN_Title` vs a decision log | `docs/user` + `docs/dev`, ADRs at `docs/dev/adr/NNNN-slug.md` (owner global rule) |
| 11 | Trailers judged per commit (framehive) vs per PR (honeycomb) | No spec trailers. The PR body names the scenario IDs it covers; a check fails on an unknown ID |
| 12 | Mocks: none (glissando, JIT-Pack) vs framework mocks allowed (honeycomb PHP) | Hand-written fakes only |
| 13 | Prettier: banned (honeycomb) vs used | Not relevant (vanilla JS UI); run `prettier --check` on `web/` only if the UI grows past one file per view |
| 14 | Doc comments required vs no rule (glissando) | Required on exported symbols (revive) |
| 15 | `go test ./...` (skipper's CLAUDE.md) vs never (JIT-Pack, skipper's own Makefile) | `go test ./cmd/... ./internal/... ./pkg/...`, so `vendor/` and `web/` stay out; the Makefile owns the list |
| 16 | Stale docs inside a repo (skipper's local-ci.md names go_1_25; framehive's changelog rule) | The Go version lives in `go.mod` and `flake.nix` only; docs never repeat it. No CHANGELOG by hand: release notes come from the squash subjects |

## 4. Draft `CLAUDE.md` for the fanin repo

`AGENTS.md` is a symlink to this file.

```markdown
# fanin

The Go daemon that replaces the bash orchestrator. It runs on a control host; each worker host runs a result
watcher. Spec: `docs/dev/concept.md`; acceptance list: `docs/dev/scenarios.md` (every ID has a test).

## Workflow

- Never commit to main. One branch `<type>/<slug>` per change, in a worktree at `../fanin-<slug>`; check
  `git branch --show-current` before each commit and push.
- Test-first: the failing test is its own `test:` commit, then the fix. Test names cite the scenario ID.
- Never rebase, never force-push; to catch up, merge `origin/main` in.
- `make ci` (inside `nix develop`) is green before every push. Then `/pr-review` on your own PR.
- Merge only on the owner's explicit go for that PR: squash, hand-written Conventional Commit subject
  (feat|fix|docs|chore|refactor|test|ci), no attribution lines.
- Specs and ADRs ship with their code. ADRs only for a real tradeoff: `docs/dev/adr/NNNN-slug.md`
  (check the next free number on origin/main).
- Docs: `docs/user/` (install, operation, configuration) and `docs/dev/` only.

## Code

- Layout: `cmd/fanind`, `cmd/fanin`, `internal/<pkg>`, `pkg/faninmcp`. `vendor/` is committed: never edit or search
  it; after a dependency change run `go mod tidy && go mod vendor`.
- Errors: wrap with `%w`; sentinels in the owning package, matched with `errors.Is`/`errors.As`, never by string.
  No `_ = f()` on an error that matters. Messages name the key, the value and the fix.
- An unreadable gate, forge, herdr or store answer is an error, never a verdict. Fail closed.
- `ctx` first, never in a struct; goroutines only through `safego.Go`.
- No magic strings or numbers: statuses, verdicts, event types and limits are constants in the owning package or
  config. Use `slog` with `task_id`/`run_id`/`project`; never log tokens.
- Doc comments on every exported symbol; comments short, about the code as it is now.
- External processes only through `runtime.Runner`; time only through the injected `Clock`; file writes are atomic.
- Config: YAML, `KnownFields(true)`, validated against the embedded JSON Schema; unknown keys are an error.

## Tests

- stdlib `testing` plus `go-cmp`; hand-written fakes; table tests with `t.Run`.
- No `time.Sleep` and no wall-clock waits: manual clock and signalled completion (`make check-no-sleeps`).
  An absence assertion needs a positive signal next to it.
- Real SQLite (`:memory:` or `t.TempDir()`), never a mocked store. Real processes only under `-tags integration`.
- Failure paths are required for gate, sched, store, hook, owner and every adapter.

## Hard limits for workers in this repo

- No `herdr`, `ssh`, `sudo`, `nixos-rebuild`, deploys, or anything under `secrets/` or `~/.ssh`.
  Integration tests use fakes on PATH.
- The bash orchestrator that fanin replaces is frozen: bug fixes there only, never fanin work.
```

## 5. CI mirror for the fanin repo

`make ci` runs inside `nix develop`. GitHub CI runs the same target, plus `nix flake check`.

```make
GO_PKGS := ./cmd/... ./internal/... ./pkg/...

ci: fmt-check vet check-no-sleeps check-file-size vendor-check test test-shuffle coverage lint vuln schema-check scenario-check

fmt-check:       ; test -z "$$(gofmt -l cmd internal pkg)"
vet:             ; go vet $(GO_PKGS)
check-no-sleeps: ; ! grep -rnE 'time\.Sleep|time\.After\(' --include='*_test.go' cmd internal pkg
check-file-size: ; scripts/check-file-size.sh 400 scripts/file-size-exceptions.txt
vendor-check:    ; go mod tidy && go mod vendor && git diff --exit-code -- go.mod go.sum vendor
test:            ; go test -race -coverprofile=cover.out $(GO_PKGS)
test-shuffle:    ; go test -count=1 -shuffle=on -timeout=120s $(GO_PKGS)
coverage:        ; scripts/check-coverage.sh cover.out 90 internal/gate internal/sched internal/store internal/hook
lint:            ; golangci-lint config verify && golangci-lint run
vuln:            ; govulncheck $(GO_PKGS)
schema-check:    ; go run ./internal/config/cmd/schemacheck config/   # every example config validates
scenario-check:  ; scripts/check-scenarios.sh docs/dev/scenarios.md   # every ID has a test name, no unknown IDs
```

Notes on the targets:

- **Order.** The cheap static checks run first, so a failure shows within seconds.
- **`check-no-sleeps`.** It allows `time.After` only inside the clock package (an exclusion in the script).
- **`scenario-check`.** It starts in report-only mode in step 1 and becomes blocking once step 2 has ported the
  gate scenarios.
- **E2E later.** A `-tags integration` target (fake `herdr`/`gh`/`claude` binaries on PATH, as the bash tests do
  today) joins `ci` once the herdr adapter exists.
