# fanin

The Go daemon that replaces the bash orchestrator. It runs on a control host; each worker host runs a result
watcher. Spec: `docs/dev/concept.md`; acceptance list: `docs/dev/scenarios.md` (every ID has a test).

## Workflow

- Never commit to main. One branch `<type>/<slug>` per change, in a worktree at `../fanin-<slug>`; check
  `git branch --show-current` before each commit and push.
- Test-first: the failing test is its own `test:` commit, then the fix. Test names cite the scenario ID.
- Never rebase, never force-push; to catch up, merge `origin/main` in.
- `make ci` is green before every push. Then `/pr-review` on your own PR.
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
