# Intake

Status: implemented in `internal/intake`, `internal/api/task_routes.go`, `internal/api/webhook_routes.go`,
`cmd/fanin/cmd_task.go` and `cmd/fanind/intake.go`. Direction: [concept](../concept.md), Intake. Every source
produces the same `intake.TaskRequest`: the CLI, the signed webhooks, cron and release deploys
(`triggers.yaml`, see [config](config.md#triggersyaml)). Scenarios: IN-1 to IN-5, DR-1, DR-2.

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

## Webhooks

`POST /api/v1/webhooks/{name}` serves each webhook of `triggers.yaml` ([api](api.md)). It takes no owner token;
the body's HMAC-SHA256 under the secret in `secret_env` proves the sender: `X-Hub-Signature-256: sha256=<hex>`
(GitHub, and any alert sender that signs that way) or `X-Gitea-Signature: <hex>`. `intake.NewWebhooks` reads every
secret at start; an unset one is `ErrNoSecret` and `fanind` does not start. A missing or wrong signature is
`ErrSignature` (401) and queues nothing (IN-1). The event comes from `X-GitHub-Event` or `X-Gitea-Event`.

- `forge` (IN-2): an `issues` or `issue_label` event with action `labeled`, `label_updated`, `opened` or `edited`
  on an issue (not a pull request) that carries `label` queues task `<project>-issue-<n>` with title, body and URL
  as the text. The label is the owner's gate: only someone who may label issues starts work; the issue text itself
  is untrusted input to the worker.
- `alert` (IN-3): a `firing` Alertmanager-style payload queues an analysis `<project>-alert-<hash>`, the hash over
  the group key and the earliest start of the firing alerts, so a repeated notification of the same firing is a
  duplicate.
- `release` (IN-5): a `release` event with action `published` hands `{project, repo, tag}` to the release queue and
  answers 202 `deploying`; a full queue (`intake.ReleaseQueueSize`) is `ErrBusy` (503) and the sender redelivers.

Anything else is `ignored` (200). An existing task ID is `duplicate` (200), so redeliveries queue nothing more.

## Cron

Each `cron` entry is a timer row (`store.Timer`, kind `cron:<name>`). `Cron.Arm` at start arms one interval after
now for each entry without a timer and disarms those of removed entries. `Cron.Fire` queues task
`<project>-cron-<name>-<due unix>` for each due timer, then disarms it and arms the next due date on the grid after
now: dates missed while `fanind` was down fire once, and a refire after a crash finds the task ID and queues
nothing (IN-4). `Cron.Run` waits on the injected clock until the earliest timer; a store or intake error is logged
and retried after `intake.CronRetry`.

## Release deploys

`Deployer.Deploy` turns a published release of a GitHub project into one bump task on the config repo project
(`release.project`), whose owner-go gate then applies like on any task (DR-1). The steps:

1. Fetch the tags and find the tag as given, with and without `v`, or as `<repo>-v<version>`:
   `intake.ReleaseLookups` (10) tries, `intake.ReleaseLookupWait` (30 s) apart on the injected clock.
2. At the tag, find the workflows under `.github/workflows` that run `docker/build-push-action` or `docker push ` (or call one),
   and the run of one of them on the tag's commit (same retries); `gh run watch --exit-status` waits for it.
3. Read the digest of `<registry>/<owner>/<repo>:<version>` with `docker buildx imagetools inspect`.
4. Fetch the config repo's base and find exactly one module matching `compose_glob` that runs the image.
5. If every image line of that module already is `<image>:<version>@<digest>`, the outcome is `nothing`.
   Otherwise queue task `<config project>-release-<repo>-<version>` with model `release.model`.

A malformed repository or tag, no tag, no workflow, no run, a failed run, no digest, or zero or several
modules is `ErrRelease` (or the
forge error) and queues nothing (DR-2). The probe runs `git` and `docker` on the control host, so both projects
must be on host `local`. The release queue runs one deploy at a time; a release still queued when `fanind` stops
is lost, and the sender's redelivery starts it again.

## CLI

`fanin task add --project P (--task TEXT | --analyze TEXT) [--model M] [--worker K] [--after ID]...` prints the new
ID. `fanin task list [--project P]` lists the open tasks. `fanin task import FILE --project P` as above.
