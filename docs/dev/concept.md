# fanin concept

Status: concept. Nothing here is implemented yet. This page records every decision taken so far and the
direction for the parts that are still open. The acceptance list is [`scenarios.md`](scenarios.md); the coding
rules are in [`guidelines.md`](guidelines.md).

## What fanin is

fanin runs coding agents on a list of tasks and brings their work back to `main`:

- **Fan out.** It picks the next task of a project, starts one fresh worker (Claude Code, opencode, …) for it in
  its own terminal pane and worktree, and watches it until the worker reports a result.
- **Fan in.** Every task ends as one reviewable pull request. CI and a review check gate it, and the forge
  merges it once it is green. fanin asks the owner only when a decision is really theirs.

It replaces a bash orchestrator that does this today with loops, pane scraping and a Markdown log. That
orchestrator is the behavioural reference: [`scenarios.md`](scenarios.md) writes its behaviour down as the
scenarios fanin must pass.

## Decisions

### D1. A Go daemon `fanind` and a CLI `fanin`

- One Go module, `github.com/polandy/fanin`, with two binaries:
  - `cmd/fanind`: the long-running daemon (scheduler, store, adapters, HTTP API).
  - `cmd/fanin`: the CLI for the owner (`fanin task add`, `fanin answer`, `fanin status`, …). The owner's MCP
    server and a TUI come later in the same binary.
- Why Go: one static binary per host and no interpreter to manage; typed tasks, events and config instead of
  regexes over text; goroutines and `context` fit the shape (one reactor, adapters reporting over channels);
  tests run on an injected clock and fakes, without sleeps.
- `fanind` is the event loop. No interactive agent session has to stay open for work to progress; a session
  becomes a client that answers questions through the CLI, MCP or UI.

### D2. herdr first, behind a `WorkerRuntime` interface

- Workers run in [herdr](https://github.com/herdrdev/herdr) panes, so the owner can watch and take over any of
  them. herdr already handles panes on remote machines and reports agent states (working, blocked, idle).
- All worker handling goes through one interface, roughly:

  ```go
  type WorkerRuntime interface {
      Start(ctx context.Context, spec RunSpec) (RunHandle, error)
      Prompt(ctx context.Context, h RunHandle, text string) error
      Observe(ctx context.Context, h RunHandle) (<-chan RunEvent, error)
      Stop(ctx context.Context, h RunHandle) error
  }
  ```

- herdr is the first adapter. Headless runs (`claude -p`, `opencode run`) for pickers and reviews and a sandboxed
  opencode runtime are further adapters behind the same interface. Nothing outside `internal/runtime` knows
  which one runs a task.
- Workers report their result through a small MCP tool (`fanin_report`, plus `fanin_progress` as a heartbeat);
  a `result.json` in the run directory is the fallback for workers without MCP. Pane text is read only to detect
  dialogs and stalls, never to find the result.

### D3. SQLite is the single source of truth

- One SQLite file in WAL mode (`modernc.org/sqlite`, pure Go). Tables for tasks, runs, events, project locks,
  holds, timers and owner questions.
- Every state change is one transaction that appends its event and updates the task row. Status, overview and
  the per-project Markdown log are rendered from the store, never parsed back.
- Locks, holds and timers are rows with leases, not lock files or process IDs. A restart reloads every
  non-terminal task, re-attaches to its run and re-arms its timers.

### D4. The forge merges (variant D)

fanin does not run merges itself. It combines existing building blocks and stays a thin layer above them:

- **GitHub:** branch protection on the base branch with required checks, auto-merge set on the PR, and a merge
  queue where the repo has one.
- **Gitea/Forgejo:** "merge when checks succeed", with [gitea-mq](https://github.com/Mic92/gitea-mq) as the merge
  queue.
- fanin opens the PR (or lets the worker open it), sets the merge intent and watches the result. Whether a PR is
  green is decided by the forge's branch protection, not by a script.
- What stays with fanin: the owner's go for projects that require it (as an approval or a status the
  protection requires), post-merge hooks such as a deploy under the project's lock, and a hold that stops
  further merges when such a hook fails.
- Why: the merge path is the most delicate part of the bash orchestrator. On the forge it becomes configuration
  that the forge enforces for humans and agents alike.

### D5. The review is a required check

- A review run posts its verdict as a commit status on the PR head. Branch protection lists that status as a
  required check, next to CI.
- The review skill defaults to `/pr-review` and is configurable per project (another skill or command, another
  model or worker kind).
- A review counts only for the head it reviewed. A new push needs a new review; an unreadable review is an
  error, never a verdict.

### D6. The data model follows the Symphony SPEC

- Task states, runs, stall detection (`stall_timeout`), workspace hooks and token accounting follow OpenAI's
  [Symphony SPEC](https://github.com/openai/symphony), an open specification meant to be implemented in other
  languages.
- fanin adds what the SPEC does not cover: forges with PR gates, owner questions as first-class state
  (`needs_owner` with options and a recommendation), project holds and post-merge hooks.

### D7. The bash orchestrator is frozen

- The bash orchestrator receives bug fixes only. Every new feature goes into fanin.
- Its behaviour is captured in [`scenarios.md`](scenarios.md). Each scenario is marked `PARITY` (fanin behaves
  the same) or `CHANGES` (a decision above replaces it).

### D8. Changes against the bash orchestrator

Decided together with the scenarios; each tag is used there.

| Tag | Decision |
|---|---|
| C-REPORT | Workers report through `fanin_report`; `result.json` is the fallback (D2) |
| C-STORE | SQLite is the source of truth; Markdown is rendered from it (D3) |
| C-SLOTS | Anonymous slots per project and host instead of named lanes; no lane names in agent or branch names |
| C-LOOP | `fanind` is the event loop; the owner session only answers (D1) |
| C-CONFIG | YAML validated by a JSON Schema replaces header keys and the project registry |
| C-BUDGET | Budgets per run (fix rounds, context roll-over, tokens) and per task (runs); a spent run is replaced by a fresh run with a structured handover |
| C-LOCK | Locks and timers are store rows with leases; there are no loop processes to kill |

### D9. Name and license

- The project is called **fanin**: fan out to coding agents, fan in to main.
- Apache License 2.0. The brand files in [`../brand/`](../brand/) use the same license; the Geist fonts stay
  under the SIL OFL.

## Direction (not yet decided in detail)

These come from the design drafts and guide the first implementation steps; they are refined in the PR that
builds them.

- **Configuration.** One directory of YAML files (`fanin.yaml`, hosts, forges, worker kinds, one file per project,
  triggers), validated against an embedded JSON Schema; unknown keys are rejected. Secrets are referenced by
  name and never stored in config.
- **Task shapes.** Besides "implement and merge a PR" a task can end in a report (analysis, alert analysis) or
  in a decision request for the owner.
- **Intake.** CLI, forge webhooks and labels, cron, alert webhooks and chat commands all produce the same task
  request.
- **Context and token budget.** One fresh worker per task; runs are bounded and handed over instead of nudged
  forever; pickers and reviews are short headless runs; `fanind` itself spends no tokens.
- **Owner UI.** One HTTP API with server-sent events. A web UI first (static files embedded in the binary, no
  build step), a TUI second. Owner questions are answered from any of them, from the CLI or from an agent
  session.
- **Observability.** Structured logs with task, run and project IDs, Prometheus metrics, `fanin status`,
  `fanin overview` and `fanin doctor`.

## Migration

Each step keeps the bash orchestrator running and can be rolled back on its own:

1. Write down today's behaviour as scenarios ([`scenarios.md`](scenarios.md)). Done.
2. Config extraction: the YAML tree plus `fanin config effective <project>`, checked against what the bash
   scripts derive today.
3. Shadow store: `fanind` follows the bash state and fills SQLite; `fanin status` is compared with the old output.
4. Owner channel: questions come from the store; the CLI, MCP and UI answer them.
5. New task shapes (analysis, alerts) run end to end on fanin only.
6. Structured worker result in the old path too, pane text only as the fallback.
7. The PR path moves to fanin project by project, low-risk projects first.
8. The bash loops are retired.
