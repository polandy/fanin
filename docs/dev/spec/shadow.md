# Shadow importer

Status: implemented in `internal/shadow`, wired into `cmd/fanind` (`--shadow DIR`). Direction:
[concept](../concept.md), migration step 3; scenarios SH-1 to SH-6.

While the bash orchestrator still runs the projects, `fanind --shadow DIR` follows its state directory and mirrors
it into the store, so `fanin status`, the snapshot and the UI show the bash state. The importer only reads `DIR`:
it never writes, renames, locks or removes a file there (SCENARIOS SH-5).

## Schedule

`fanind` imports once before it serves; a first import that fails stops it with the error and exit 1. Then it
imports every `shadow.Interval` (30 s) on the injected clock. A later failed import is logged at error level and
changes nothing in the store; the store keeps the last good import and the next tick tries again.

## What is read

| File in `DIR` | Becomes |
|---|---|
| `<label>.md` | the tasks and runs of project `<label>` |
| `.<label>.merge-hold` | an active `merge` hold; reason: line 1 |
| `.<label>.ci-hold` | an active `ci` hold; reason: line 1, fix task: `shadow-<agent>` of the last `ci-fix: <agent>` line |
| `.<label>.merge.lock` | the project lock while a process holds a `flock` on it; content `<lane> <pid> <pr> <since>` |

Other files (`.tail`, `.watchdog`, `.beat`, the loop and state locks, `archive/`) are ignored. The flock check reads
`/proc/locks` and matches the lock file's inode.

Unreadable state is `shadow.ErrUnreadable`, never an empty status (SCENARIOS SH-4): a missing or unreadable
directory or file, a state file that is not UTF-8, a held merge lock whose content does not parse, an unreadable
`/proc/locks`. One error fails the whole import.

## State file lines

An event line is `YYYY-MM-DD HH:MM <event>`, with an optional lane tag ` [lane x]` at its end. Other lines (headers,
notes without a time) are skipped, like `status.sh` does. A worker is an agent named `<label>(-<lane>)?-<n>`
(label lowercased); its lane is `a` without a lane letter. The importer replays the lines in order:

| Line | Task of the agent (or of the PR) |
|---|---|
| `spawned <agent> (<model>…): <prompt>` | new task `shadow-<agent>`, `running`, with run 1 `working`; in the same lane an earlier task with the owner's go becomes `done`, a `running`, blocked or `paused` one `canceled` |
| `pr #<n> <agent> <branch>` | `gating`, the task now owns PR `n` |
| `blocked <agent>: <reason>` | `needs_owner` |
| `ready #<n> …` | `needs_owner` (waits for the owner's go) |
| `owner go for #<n>…` | `gating` (the merge runs) |
| `paused #<n> …` | `paused` |
| `merged #<n> …` | `done`, run `succeeded`; without a `pr #<n>` line (older files) the lane's newest `running` or `gating` task without a PR |
| `idle <agent>: …` | `done` (the worker found nothing to do), run `succeeded` |

The lane of a line is its lane tag, `a` without one. `done` and `canceled` are final. Lines that name an unknown
agent or PR (a helper session, a line whose spawn was rotated away) are skipped. The model of a spawn line is the task's picked model; a prompt starting with
`owner task:` marks an owner task.

## Writing to the store

An import reads the whole directory first; nothing is written when a read fails (SCENARIOS SH-4). Tasks, runs and
holds of every project are then written in one transaction, so a partial import never commits; the merge locks
follow, through the store's lock calls.

- A task the store lacks is created `queued`; a task whose state differs is moved along the shortest path the
  model's transition table allows, each step's event detail being the bash line that set the state. A task the store
  already has in a state no path leaves (it is ahead of the file) is skipped with a warning.
- Runs follow their task: `working` while the task is open, `succeeded` on `done`, `canceled` on `canceled`.
- Holds: an active hold of the same kind, reason and fix task stays; a changed one is lifted and set again; one
  whose file is gone is lifted. A label is a project while any of its files is in `DIR`; the holds of a label with
  no file left stay as they are. A `ci` hold without a `ci-fix:` line has the fix task `shadow-pending-fix`.
- The merge lock: while the flock is held the project lock is taken (or renewed) for `shadow.LockLease` (3 ×
  `Interval`, so a stopped `fanind` lets it expire). The holder is the task that owns the PR, else `lane <x> #<pr>`.
  A free flock releases the row.

An import of an unchanged directory writes no task, run, hold or event (SCENARIOS SH-5).

Times in the store are import times; the bash time stays visible in the event details.
