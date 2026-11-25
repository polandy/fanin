# Old path: reading the worker result

Status: the fanin side is done (`fanin report`); the bash change is an owner decision and not part of that PR.
Direction: [concept](../concept.md) migration step 6, C-REPORT.

While the bash orchestrator still runs a project, a worker without MCP reports through the same
[`result.json`](../spec/worker-contract.md) the daemon reads.

## Worker side

The worker runs one command at the end of its task, with the run directory the orchestrator gave it:

```sh
FANIN_RUN_TOKEN=<token> fanin report done_pr --pr 42 --dir <run dir>
```

`fanin report <kind>` takes `--pr`, `--branch`, `--issue`, `--issue-url` and `--text`, each kind with the field
[the contract](../spec/worker-contract.md) names. It checks the report first: an unknown kind or a missing field is
exit 2 and writes nothing. It writes `<run dir>/result.json` atomically (temp file, rename), so a reader never sees a
half-written file; a failed write is exit 1 and leaves an older file as it was. With `FANIN_URL`, `FANIN_RUN_ID`
and `FANIN_RUN_TOKEN` set and no `--dir`, it sends the report to the daemon instead.

## Orchestrator side

The orchestrator reads the result in this order and stops at the first that answers:

1. `<run dir>/result.json`. Parse it as one JSON object. Take `kind` and the kind's field; ignore the file when its
   `token` is not the run's token. An unparsable file counts as no result, with a warning.
2. The pane status line (`DONE PR #<n>`, `BLOCKED: ...`), as today.

Mapping to the old statuses: `done_pr` is `DONE PR #<pr>`, `pushed` is `PUSHED`, `committed` is `COMMITTED`,
`issue` is `ISSUE`, `nothing` is `NOTHING: <text>`, `blocked` is `BLOCKED: <text>`.

Rollback: remove the file read; the pane status line is unchanged.
