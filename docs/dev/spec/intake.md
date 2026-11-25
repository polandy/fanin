# Intake

Status: implemented in `internal/intake`, `internal/api/task_routes.go` and `cmd/fanin/cmd_task.go`. Direction:
[concept](../concept.md), Intake. Every source produces the same `intake.TaskRequest`; the CLI is the first one.

## TaskRequest

`Project`, exactly one of `Task` (owner task, shape `pr`) and `Analyze` (owner question, shape `analysis`), optional
`Model`, `Worker` (worker kind) and `After` (task IDs), and an optional `ID`. Both forms set `OwnerTask`
(SCENARIOS PK-6), so no picker runs and the project loop ends after the first merge. The title is the first line of
the text, cut at 80 runes.

## Rules

`Service.Enqueue` validates and queues in one transaction; on any error nothing is queued.

- `CheckFlags` (no config needed, so the CLI runs it before any call): `task` with `analyze`, neither, or
  `--worker opencode` without a task is `ErrInvalid` (PK-7).
- `Validate`: an unknown project is `ErrUnknownProject`; an unknown worker kind, or a model the effective kind
  (the request's worker, else the project's) does not list, is `ErrInvalid` (PK-7). A kind without models takes no
  model.
- Opencode (kind named `intake.KindOpencode`) runs only GitHub projects' owner tasks: a Gitea project or an analysis is
  refused (SP-9).
- `After` tasks must exist in the same project, else `ErrInvalid`.
- An explicit `ID` that exists is `ErrExists`; without one the task gets `<project>-<n>` (`Tx.NextTaskID`).

The API maps these to 400, 404 and 409; `fanin task add` turns a 400 into a usage error (exit 2).

## Chain files

`fanin task import <file> --project P` reads one `model|task` per line. `#` and blank lines are skipped but counted.
A task's ID is `<project>-<file name without extension>-<line number>`, and each task is `After` the previous one
(CH-1). Importing a changed file again skips the IDs that exist and adds only the new lines (`intake.Import`
counts `added` and `skipped`); an edited existing line is not updated. A line without `|` or without a task is a
usage error naming the line.

## Dependencies

`Store.Ready(project)` lists the queued tasks whose `After` tasks are all done. A task that is not done, failed or
still running, holds back only the tasks that name it, directly or through a chain; the rest of the queue stays
free (CH-2).

## CLI

`fanin task add --project P (--task TEXT | --analyze TEXT) [--model M] [--worker K] [--after ID]...` prints the new
ID. `fanin task list [--project P]` lists the open tasks. `fanin task import FILE --project P` as above.
