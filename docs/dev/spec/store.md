# Store

Status: implemented in `internal/store`. Decision: [concept D3](../concept.md#d3-sqlite-is-the-single-source-of-truth),
tags C-STORE and C-LOCK.

## Database

- One SQLite file, opened with `store.Open(ctx, path, clock)` through `modernc.org/sqlite` (pure Go).
- Pragmas on every connection: `journal_mode=WAL`, `busy_timeout` = `store.BusyTimeout` (5 s), `foreign_keys=ON`.
  Write transactions start with `BEGIN IMMEDIATE`, so two writers never deadlock on a lock upgrade.
- `Open` refuses a file whose journal mode does not become `wal` (for example on a file system without shared
  memory), and a path with `?` or `#`.
- The store holds one connection; `fanind` is the only writer. Inside `Transition`, read and write through the
  `Tx` only: a `Store` method called there waits for the connection the transaction holds.
- Times are stored as Unix nanoseconds in UTC; `0` means unset. They come from the injected `clock.Clock`, never
  from the wall clock.

## Migrations

- Embedded from `internal/store/migrations/NNNN_slug.sql`, numbered from `0001` without gaps.
- `schema_migrations` records every applied version. `Open` applies the pending ones in order, each in its own
  transaction: a failed migration rolls back completely, keeps the earlier ones and fails `Open` with
  `store.ErrMigration`.
- A database with a higher version than the build knows is refused (`ErrMigration`): an older `fanind` never
  writes to a newer schema.
- A migration is never edited after it shipped; a change is a new file.

## Tables

| Table | Key | Holds |
|---|---|---|
| `tasks` | `id` | `model.Task`; `after_ids` is a JSON array, `worker_kind` (0003) the owner's worker kind or empty |
| `runs` | `id`, unique (`task_id`, `number`) | `model.Run` with its budget columns; `result` is the JSON report or empty |
| `events` | `seq` (autoincrement) | the append-only `model.Event` log; `task_id` is empty for project events |
| `project_locks` | `project` | holder, `acquired_at`, `expires_at` |
| `holds` | `id` | `model.Hold`; `lifted_at = 0` while active |
| `timers` | `id` | project, task, kind, `due_at`; `disarmed_at = 0` while armed |
| `owner_questions` | `id` | `model.OwnerQuestion`; `options` is JSON, `kind` and `allow_text` (0002), `answered_at = 0` while open |
| `project_pauses` | `project` | the one-shot pause flag and its `requested_at` (0002) |

Enum columns hold the lowercase `internal/model` constants. Reading a value that `model.Parse*` does not know, or
JSON that does not decode, is `store.ErrCorrupt`, never a state.

## Transactions

`Store.Transition(ctx, fn)` runs `fn` with a `*store.Tx` in one write transaction: it commits when `fn` returns
nil and rolls back every row and event otherwise. Each `Tx` write appends its event in the same transaction:

| `Tx` method | Event |
|---|---|
| `CreateTask` | `task_created` |
| `SetTaskState` (checked by `model.TaskState.CanTransition`) | `task_state` |
| `CreateRun` | `run_started` |
| `SetRunState` (checked by `model.RunState.CanTransition`; a terminal state sets `ended_at`) | `run_state` |
| `SetRunResult` (normalised and validated) | `result_reported` |
| `SetRunBudget` | `token_usage` |
| `SetHold` / `LiftHold` | `hold_set` / `hold_lifted` |
| `AskQuestion` (validated) / `AnswerQuestion` | `question_asked` / `question_answered` |
| `RequestPause` / `Resume` / `ConsumePause` | `project_paused` / `project_resumed` / `pause_consumed` |
| `AppendEvent` | any other event type, such as `notified` and `notify_failed` |

`Tx.Run`, `Tx.LatestRun` (the task's run with the highest number) and `Tx.Task` read inside the transaction, so a
check and the write it guards see the same rows. `ArmTimer` and `DisarmTimer` write no event; they ride in the transaction of the state change they belong to.

## Feed for the API

- `Changed()` returns a channel that closes after the next committed `Transition`; take it before reading, then wait
  on it. A rolled-back transaction does not close it.
- `EventsAfter(ctx, seq, limit)` reads events above `seq` in append order; `LastEventSeq` is the newest `seq`
  (0 when empty); `AllHolds` lists the active holds of every project; `Locks` lists every lock row, expired leases
  included; `LatestEvents` returns the newest event of each project. `OpenTaskQuestion` is the newest open question of a
  task; `Paused` reads a project's pause flag. The SSE stream and the snapshot build on them
  ([api](api.md)).

## Project lock (C-LOCK)

One row per project in `project_locks`, with a holder (a task or run ID) and a lease.

- `AcquireLock(ctx, LockRequest{Project, Holder, Lease})` takes a free or expired lock. A live lease of any
  holder, the same holder included, is a `*store.LockHeldError` matching `store.ErrLockHeld` (SCENARIOS MG-1,
  FP-1).
- `WaitLock(ctx, req, timeout)` retries until the lock is free. A release by the same store wakes it at once;
  a release by another process or an expired lease is seen within `store.LockPoll` (1 s). After `timeout` on the
  store clock it returns a `*LockHeldError` with the holder and the time waited, matching `ErrLockHeld` and
  `ErrLockTimeout` (SCENARIOS MG-2).
- A lease that is not renewed in time frees the lock: a holder that died stops renewing (SCENARIOS MG-3).
  `RenewLock` extends a live lease; `ReleaseLock` frees it, and both return `store.ErrLockNotHeld` for a holder
  that does not hold it (the scheduler releases a run it knows is dead the same way).

## Holds

- `Holds(ctx, project)` lists the active holds.
- `MergeHold(ctx, project, task)` returns the oldest hold that stops `task` from merging: every `merge` hold, and a
  `ci` hold unless `task` is its `FixTask` (SCENARIOS MG-4, MG-8).
- `DispatchHold(ctx, project, task)` returns the oldest `ci` hold that stops a fresh worker for `task`; its fix
  task is let through (SCENARIOS MG-8).
- `LiftHold` lifts one hold by ID; the project's other holds stay.

## Restart

A restart opens the same file and reads:

- `OpenTasks(ctx)`: every task in a non-terminal state (`model.TaskState.Terminal`), oldest first, to re-attach
  to its run.
- `Ready(ctx, project)`: the queued tasks whose `After` tasks are all done. A task that is not done holds back only
  the tasks that name it (SCENARIOS CH-2). `Tx.NextTaskID` gives the next free `<project>-<n>`.
- `ArmedTimers(ctx)` to re-arm every timer, and `DueTimers(ctx)` for those already due at the clock's now.
- `OpenQuestions(ctx)` for the questions still waiting for the owner.
- `ProjectTasks(ctx, project)`, `ProjectQuestions(ctx, project)` and `Projects(ctx)` for the per-project Markdown render
  (`docs/dev/spec/render.md`); they return every row, whatever its age or state.

## Retention

The store keeps everything. Tasks, runs, events, lifted holds, disarmed timers and answered questions are never
deleted or rewritten: the event log is the audit trail that status, overview and the per-project Markdown log
are rendered from. Rows are small, so one file holds years of history. Archiving or pruning, if it is ever
needed, is a separate decision with its own ADR and runs as an explicit owner command, never automatically.
