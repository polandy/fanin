# Scheduler spec

Status: implemented in `internal/sched` and wired into `cmd/fanind` for every project with `loop: fanin`.
Direction: [concept](../concept.md) D1, C-LOOP, C-SLOTS and C-BUDGET. Scenarios: PK-3 to PK-6, SP-8, SP-10, SP-11,
LN-1 to LN-6, WD-1 to WD-3, FP-3, FP-6, AW-10 to AW-13, GH-11, GH-12, MG-1 to MG-5, MG-8.

`fanind` is the event loop. The owner session only answers. The bash orchestrator keeps every project with
`loop: bash` (the default), and `fanind --shadow` keeps importing the bash state of those projects only.

## Reactor

`Scheduler.Run` reads the event cursor, resumes the live runs, then repeats one step until ctx ends. A step:

1. reads the store events after the cursor in batches of `EventBatch` (256);
2. applies the runtime events the observers sent (`WorkerRuntime.Observe`);
3. fires the due `stall`, `worker_timeout` and `await` timers, and the gate timers of a gating analysis;
4. fills the free slots of every project.

Between steps the loop blocks on the next store commit (`Store.Changed`), a runtime event, a finished picker or
start, the earliest armed timer, or ctx. Only Run's goroutine touches the scheduler's state. Pickers, starts and
observers run in `safego.Go` and hand their result back to the loop.

A store read or write that fails is logged as `sched: step failed, retrying` and holds the loop for `RetryAfter`
(30 s, on the injected clock). The failed write changes nothing, so a run is never ended because the store
refused a write. Only an unreadable event cursor, or a failure while resuming, ends `Run` with an error.

## Slots (C-SLOTS)

A project has `max_parallel` slots, numbered from 1. A slot is used by the latest run of every task that is
neither queued nor ended while that run is live, and by every running picker. A new run gets the lowest free
slot. Lanes are gone: neither the agent name nor any other identifier carries a lane (LN-1 to LN-4).

## Choosing a task

`fill` takes the project's ready tasks in order, owner tasks first. It skips a task that a picker holds and a task
that a hold blocks. A ci hold lets its fix task through.

- An owner task spawns at once and never runs the picker (PK-6).
- Any other task first runs the picker in `internal/pick` ([headless picker](headless-picker.md)). Its slot is
  held in memory while the picker runs.
  - A pick spawns with the picked hint and model.
  - `NOTHING` moves the task through running to done with detail `idle: <reason>`, and starts no worker (PK-3).
  - A failed pick spawns with an empty hint, so the worker chooses its own task (PK-4).
- The model follows `config.WorkerModel`: the owner's model, then the project's, then the picked one, then the
  kind's default. A kind without models gets none (PK-5).
- A pending pause is consumed before the picker and again in the spawn transaction. The project then starts no
  worker until `project_resumed`.

## Spawn

One transaction writes the run row in state `starting`:

- ID `<task>-run-<n>`;
- agent `<task>-<n>`, where n is one past the task's latest run, so a failed run's number is never reused;
- the task moves to running.

Then, in the background:

1. `RunFiles.Prepare` writes `runs/<run>/mcp.json` next to the store. It is mode 0600 and holds the run token.
2. `WorkerRuntime.Start` starts the worker.

When the start succeeds, the run gets its workspace, tab and pane (`Tx.SetRunWorker`), moves to working, and the
task's `stall` and `worker_timeout` timers are armed.

A failed write or start ends the run failed, with detail `spawn failed <agent>: <cause>`, and the task failed
(SP-8). A worker kind whose runtime is not herdr is refused with `ErrWorkerKind`, and so is an opencode task on a
forge that is not GitHub or of a shape other than PR (SP-9). An opencode spec carries kind `opencode` and the
project's base. A started worker that cannot be
recorded is logged as having unknown status and keeps its slot: no second worker is started.

## Timers (WD-1, WD-2, WD-3, FP-6)

The timers are store rows armed on the injected clock, so they survive a restart. The newest sign of work is the
run's start, a `progress` event, or a runtime `working` event.

- **`stall`**: once `stall_after` has passed without a sign of work, a working run becomes stalled and climbs the
  stall ladder (below). A runtime `stalled` event with reason `quiet` does the same. A `progress` event or a
  `working` event moves it back. A `bg-shell` stall only marks the run.
- **`worker_timeout`**: once `worker_timeout` has passed without a sign of work, the run ends `timed_out` with
  detail `worker timeout: no heartbeat or result for <d>`, and the task fails.
- **When the timeout spares a run**: it logs `worker timeout spared` and looks again one timeout later. It spares
  a run when:
  - the worker is working;
  - the worker is blocked on a dialog;
  - the run or its task holds the project lock;
  - the worker's own background-shell footer has been there for less than `BGFooterMax` (180 min);
  - the worker's status is unknown: no event yet, the runtime is unreachable, or the stream ended without the
    agent being gone. An unknown state never ends a run.
- **When a result arrives**: on `result_reported` the run moves to reported and both timers are disarmed. The
  slot stays held until the task leaves running.

## Nudge ladder (AW-10, AW-11, AW-13)

The ladder counts its steps on the run (`Budget.Stalls`, `FooterWaits`, `ContinueNudges`, `SecureNudges`, through
`Tx.SetRunCounts`), so a restart keeps them. Every nudge is an embedded template (`nudge-stall`, `nudge-continue`,
`nudge-secure`). It is recorded as `worker_nudged` with the template name, then sent with
`WorkerRuntime.Prompt`. A nudge that cannot be delivered ends the run and the task `failed` with
`nudge to <agent> failed: <cause>`, and nothing is awaited after it (AW-13).

- **Stall (AW-10).** The first stall of a run gets one `nudge-stall` (`StallNudges`). The next stall hands the run
  over to a fresh run ([handover](handover.md)), or moves the task to `needs_owner` once it has used
  `max_runs_per_task` runs.
- **Silent stop (AW-11).** A runtime `idle` event on a live, unreported run climbs this ladder:
  1. The idle carries `BGFooter`, so a background footer outlasted the adapter's hold: wait again, up to
     `MaxFooterWaits` (3) times. Each wait arms an `await` timer `FooterWait` later: `stall_after / 4`, so every
     wait fits into one `stall_after`. When it fires and the worker is still idle, the ladder climbs again.
  2. One `nudge-continue`.
  3. A scan of the run's unpushed commits (`Deps.Unpushed`, AW-12). Commits found, or a scan that fails or
     lacks the branch (fail closed), get one `nudge-secure`.
  4. The task moves to `needs_owner` with `blocked <agent>: no status; unpushed: <commits|none|unknown>; tail:
     <last words>`. Its timers are disarmed, and the pane stays open.

  A run that ends without `fanin_report` or `result.json` takes the same path: its worker goes idle unreported.
- **Tail (AW-12).** Each `idle` or quiet `stalled` event stores its `Tail` on the run (`Tx.SetRunTail`), and the
  last words are logged. `GitUnpushed` runs `git -C <path> log refs/heads/<branch> --not --remotes` through the
  runner, so the scan covers only this run's branch. The branch is stored from the run's first report that names
  one (`Tx.SetRunBranch`).

## Budgets (C-BUDGET)

Each `token_usage` event checks the run's budget against the project's `budgets`:

- `max_tokens_per_run`;
- `rollover_pct` of the context;
- `fix_rounds_per_run`.

A spent budget on a live latest run hands the run over. A run that is gating returns its task to running. Once
the task has used `max_runs_per_task` runs, it moves to `paused` with `paused <agent>: <reason>; <n> of <m> runs
used` instead. The format and the fallbacks are in the [handover spec](handover.md).

## Analysis tasks (FP-3, GH-11, GH-12)

An analysis task (`fanin task add --analyze`) ends by the result its run reports:

- `issue`: the run ends succeeded, the task `done` with `analysis needs-owner <issue url>` (`#<n>` without a
  URL), and the pane is closed. A pane that cannot be closed is logged with `close it by hand`.
- `nothing`: the same, with `idle: <text>`.
- `blocked`: the task moves to `needs_owner` with `blocked <agent>: <text>` and the owner gets a note
  (`Deps.Notify`; `notified` or `notify_failed` is recorded). The pane stays open.
- `done_pr`: the task moves to `gating` with `gating #<n>` and the PR's gate is read (`gate.Watcher`). Its
  `gate_retry`, `gate_wait`, `gate_settle` and `gate_poll` timers read it again. A green PR records a
  `gate_verdict` of `ready #<n>: GREEN <title>; <note>`, where the note says whether it changes more than docs
  (`gate.DocsOnly`, unreadable files count as code). The task stays gating for the owner's go, which merges it
  (see Merge). A red PR goes to the owner as blocked; an unreadable gate as `ERROR #<n>: <cause>`.
- Any other kind (`done`, `pushed`) goes to the owner as blocked: an analysis ends with one of the four above.

Without `Deps.Gates` the verdict is `ready #<n>: gate unread, no forge reader; check the PR before the go`.

## PR tasks (GH-7 to GH-14, GT-7, GT-8, OC-7, OC-8, CF-1 to CF-3)

A PR task (the default shape) ends by the result its run reports. `issue`, `nothing` and `blocked` end it as an
analysis does; any other kind but `done_pr` goes to the owner as blocked (`reported <kind> without a PR`).

On `done_pr` the PR is read once (the head check, GH-14). It goes to the owner as blocked, never gated, when:

- it heads the base branch, or no branch: `#<n> heads the base branch <base>; push a feature branch …`;
- it heads another branch than the run's: `#<n> heads <head>, not the run's branch <branch>`;
- another live task's latest run has that branch or reported that PR: `#<n> is claimed by task <id>`.

A run without a branch takes the PR's head. The gate then starts from that same read and runs on its timers as for
an analysis. A final answer decides:

| Verdict | Then |
|---|---|
| GitHub `GREEN` | `green #<n>: GREEN <title> (auto)` and the merge starts without the owner |
| Gitea `GREEN docs` | the same with `(docs-only, auto)` |
| Gitea `GREEN code` | `ready #<n>: …; changes more than docs, the owner's go merges` |
| Gitea `NOCI` | `ready #<n>: …; no CI, the owner's go merges` |
| opencode `GREEN` | auto with `(opencode, auto)`; with `oc_merge: manual` ready with `oc_merge manual, …` |
| RED, CI | `nudge-fix`; the fix round counts against `fix_rounds_per_run` |
| RED, conflict | one `nudge-conflict` per task, from the same fix budget; a second conflict pauses the task |
| RED, review | `nudge-review`, up to 3 per run (`review_nudges`, not the fix budget); the next pauses the task |
| RED, closed or timed out | blocked to the owner |

The gate names the cause of a RED (`gate.Result.Cause`). A nudge moves the run back to `working` and the task to
`running` with the verdict as detail, arms the stall and timeout timers again and sends the gate's reasons as
`HINT`. A fix round past `fix_rounds_per_run` hands the run over, or pauses the task once `max_runs_per_task` runs
are used (see Budgets). A paused review or conflict reads `paused <agent>: #<n> RED <reasons>; review nudges
spent: 3 of 3` or `…; still conflicting after 1 nudge; merge origin/<base> by hand`.

A ready PR waits gating for the owner's go. A red re-gate under the merge (see Merge) takes the same nudges; the
go stands, so a ready PR after the fix merges without a new go; a conflict there is CF-3.

## Opencode delivery (OC-1, OC-2, OC-6, CF-4)

An opencode worker commits in the sandbox and reports `committed` with its `oc/<slug>` branch; it opens no PR.
On `committed`:

- from any worker kind but opencode: the owner gets `ERROR <branch>: COMMITTED from a <kind> worker …` (OC-2);
- without `Deps.OCDeliver` and `Deps.OCReview`: the owner gets an `ERROR`, the branch is left for a hand push;
- else, in the background, `OCDelivery.Deliver` then `OCReviewer.Review`. Either error goes to the owner as
  `ERROR <branch>: <cause>` and the gate never runs (OC-6). On success the run's result becomes `done_pr` with
  the PR, and the PR path above takes it, head check first. A task that moved on meanwhile is left alone. A stop
  during the delivery or review asks no owner; the task stays running with its `committed` result.

`OCPush` is the real delivery, all git through `runtime.Runner` in the project's checkout:

1. Refuse a branch that does not match `oc/[A-Za-z0-9._-]+` (`ErrOCBranch`) and a project whose forge is not
   GitHub (`ErrOCForge`).
2. `git remote get-url aiagent`; a missing sandbox remote is `ErrOCRemote` with the `git remote add` fix.
3. `git fetch -q aiagent +refs/heads/<b>:refs/remotes/aiagent/<b>`; a failure is `ErrOCFetch`.
4. `git push -q origin refs/remotes/aiagent/<b>:refs/heads/<b>`: fast-forward only. A refused push, a rewritten
   branch included, is `ErrOCPush` (`it is never forced`, CF-4).
5. Reuse the open PR whose head is the branch; else open one with the branch tip's subject as title and
   `OCPRBody`, which names the reviewer.

## Merge (MG-1 to MG-5, MG-8, LN-6)

The owner's go (`owner_go`) on a gating task whose latest run reported `done_pr` merges it, when `Deps.Merger` is
set. `Deps.Merger` needs `Deps.Hook` and `Deps.StateDir`, else `New` returns `ErrMergeDeps`. One merge per project
runs at a time:

- The merge takes the project lock (`store.AcquireLock`, holder the task ID, lease `merge.lock_lease`) and renews it
  every half lease. A held lock is waited for up to `merge.lock_wait`; a longer wait goes to the owner as
  `ERROR #<n>: merge lock of project <p> held for over <wait> by <holder>`. An expired lease of a dead holder is
  taken over (MG-3). A lock lost to an expired lease is only logged, on renew and on release: the merge goes on.
- Under the lock it checks the holds (`store.MergeHold`). A held merge logs `merge_held` with `paused #<n>: merge
  held (<kind> hold <id>: <reason>) (worker <agent> still open)` and waits; a `hold_lifted` of the project starts
  every held merge again. The holds are checked only under the lock, so a merge that waits for the lock is not
  paused by the hold of the hook running before it.
- After a wait for the lock, and on every retry of a held merge, the gate is read again (`gate.Watcher`): another
  merge may have changed the base. A red re-gate sends the run back to working with `re-gate #<n> <verdict>`
  and the `nudge-regate` nudge; the go stands, so the next green report merges without a new go. A pending
  re-gate reads the gate again on its timers and merges once it settles green. An unreadable re-gate, or none
  without `Deps.Gates`, goes to the owner.
- `Deps.Merger.Merge` merges the PR; `merged` is logged and the task moves to `post_merge` with `merged #<n>`.
  A failed merge goes to the owner.
- The post-merge hook (`internal/hook`) runs in `Deps.StateDir` under a merge hold. On success the run ends
  succeeded, the task `done` and the pane is closed. A failed hook leaves its hold: the PR counts as merged, the
  detail is `ERROR #<n>: post-merge hook failed: <last line>; merging held for all slots until 'fanin hold lift
  <id>'` and the owner gets a note. A restart inside the hook keeps the running hold (MG-5).
- The hook runs `sh -c <post_merge_hook>` through `runtime.Runner` with `FANIN_PROJECT` and `FANIN_PR` and no lane
  variable; an empty `post_merge_hook` runs nothing and holds nothing.

### Pre-go check (GT-8)

With `pre_go_check` set, a PR that waits for the owner's go runs `Deps.PreGo` (`internal/hook`, `hook.PreGo`)
before the `ready` verdict: fetch base and branch, a throwaway worktree under `<state dir>/prego/`, `sh -c` the
command there with `FANIN_PROJECT`, `FANIN_PR`, `FANIN_BASE`, `FANIN_BRANCH` and `FANIN_BASE_REF`, then the worktree
is removed. The check runs off the loop; `checking` marks the task and a go that arrives meanwhile is kept as the
standing go and merges after the verdict. Its stdout (lines joined with `; `, at most 600 runes) ends the ready
detail. A failed check, a fetch or worktree error, a missing `Deps.PreGo` or an empty run branch goes to the owner as
`pre-go check #<n>: ...` and never merges (FP-6). User interface: `docs/user/pre-go-check.md`.

### CI hold (MG-8)

Before a merge, `Deps.BaseCI` reads the required checks of the project's base branch. A failed or cancelled check
creates an owner task `fix red CI on <base>: <names>` and a `ci` hold naming it as the fix task, in one
transaction; the merge is then held. A `ci` hold stops every merge but its fix task's, and every fresh worker but
the fix task's: `nextTask` skips the held tasks, and a task held while its picker ran is logged with `paused: CI
held, no new worker` and stays queued. The fix task's own merge skips the base check and lifts the hold with the
merge; the project's other live workers get the `nudge-ci-fixed` nudge, and a nudge that fails is named in the
merged detail. An unreadable base CI goes to the owner; without `Deps.BaseCI` nothing is read.

## Restart (SP-10, SP-11)

`Run` first resumes every non-ended latest run of a scheduled project:

- A run with a stored agent and pane is observed again with its stored handle. Its missing timers are armed again
  and no worker is started.
- A run without a stored pane (for example, a restart between the run row and the recorded start) has unknown
  status. It keeps its slot, nothing is started or observed for it, and it is logged as `status of <agent>
  unknown, no second worker started`.

A stop by ctx is never a task failure:

- a task whose picker is still running stays queued;
- a run whose start is still running stays starting;
- a live run stays as it is.

## Limits

- `loop: fanin` is refused on a project whose host is not local (`ErrRemoteHost`). Set `loop: bash` there until
  remote hosts are scheduled.
- Picker reservations and the stopped state after a consumed pause are held in memory. A restart re-derives them
  from the store: a queued task picks again, and a project with an unconsumed pause consumes it again.

## Open

- The picked model is not persisted; a resumed run keeps its stored worker but loses the picked model.
- The MCP config path of a remote worker and a forge seam for the picker request.
- `fanind` passes no `Deps.OCDeliver` or `Deps.OCReview` yet (no forge is wired), so a `committed` asks the owner.
- A delivery cut short by a stop is not resumed on restart; push and review the branch by hand.
- Wiring `resultwatch` to `report.Service`.
- Nothing fills `context_pct` or the token counts yet; those budget checks act once the adapters write them
  through `SetRunBudget`.
- A pending pause does not stop a handover; the fresh run continues the task.
- `fanind` passes no `Deps.Gates` yet (no forge reader is wired), so a PR is never read there.
- The gate state of a gating task is held in memory; after a restart only an armed gate timer reads it again.
- `fanind` wires no `Deps.Merger`, `Deps.BaseCI`, `Deps.Hook` or `Deps.StateDir` yet, so a go merges nothing there.
- The CI hold rule is a first simple one: any failed or cancelled check on the base branch, read before each merge.
- The merging, held and standing-go state is held in memory; after a restart a held merge needs a new go.
- A store error inside a merge is logged as a failed step and the merge is not retried; the owner gives the go again.
- AP-32b: the forge's auto-merge and the wait for `MERGED`, the go as the commit status `fanin/owner-go`, the
  squash title with the PR number (MG-6), the branch, worktree and pane cleanup (MG-7), remote git through the
  host runner (FP-4). Until then a green PR merges through `Deps.Merger`, and without one it stays gating.
- A stop between the end of an analysis and its pane close leaves the pane open.
