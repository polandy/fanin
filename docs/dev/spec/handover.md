# Handover spec

Status: implemented in `internal/prompt` (`Handover.Markdown`) and `internal/sched` (`handOver`).
Direction: [concept](../concept.md) C-BUDGET. Scenarios: AW-10, WD-5, the C-BUDGET row.

A run that has spent a budget is not nudged on and on. The scheduler ends it `rolled_over` and starts a fresh run
of the same task. The fresh run's prompt is the task's worker prompt with the same hint, followed by the handover
document below.

## When a run is handed over

| Trigger | Reason in the handover | Fallback after `max_runs_per_task` |
|---|---|---|
| `total_tokens >= max_tokens_per_run` | `token budget spent: <n> of <limit> tokens` | `paused` |
| `context_pct >= rollover_pct` | `context <n>% full, roll-over at <limit>%` | `paused` |
| `fix_rounds > fix_rounds_per_run` | `fix budget spent: fix round <n> of <limit>` | `paused` |
| a working run past `worker_max_wall` (WD-5) | `wall time spent: running for <d>, limit <max>` | `paused` |
| a second stall after the stall nudge (AW-10) | `no progress for <stall_after> after the stall nudge; tail: <last words>` | `needs_owner` |

The budgets are read from `token_usage` events, which `Tx.SetRunBudget` appends when a Claude worker's status line
sends its usage ([worker contract](worker-contract.md#usage), AW-14). All limits come from the
project's `budgets` in the config.

When the task already has `max_runs_per_task` runs, no new run starts. The task moves to the fallback state with
the detail `paused <agent>: <reason>; <n> of <m> runs used` (or `blocked …` for `needs_owner`). The worker's pane
stays open.

## Format

The document is Markdown, appended after the rendered worker prompt and one blank line. Its sections always come
in this order, and a section with nothing recorded says so:

````text
## Handover from run <n>

Run <n> of this task ended: <reason>. You are its fresh run: carry on from this state on the same branch and do
not start over.

### State

<the run's last fanin_progress text, else "Last report: <kind> <text>", else "No progress was reported.">

### Open

- <PR #<n> is open; carry it on instead of opening a new one.>
- <The run was blocked: <question>>
- <The run was asked to push its commits and did not report; check for unpushed commits first.>

### Branch

`<branch>`

### Last errors

- run <run id>: <detail>

### Tail

```text
<the last 40 lines of the run's pane tail>
```
````

- **State** is the spent run's newest `progress` detail, else its last report.
- **Open** lists the PR of a `done_pr` report, the question of a `blocked` report, and a note on unpushed commits
  once the run got `NUDGE_SECURE`.
- **Branch** is the run's branch: the one stored on the run, else the branch of its first report. Without one,
  the section tells the worker to find it with `git branch` and `git worktree list`.
- **Last errors** are the newest `HandoverErrors` (5) `run_state` details of the task that start with `stalled: `,
  `spawn failed `, `worker timeout: `, `rolled over: ` or `nudge to `, oldest first.
- **Tail** is the last `HandoverTailLines` (40) lines of the tail stored on the run (AW-12). Its code fence is longer
  than any backtick run in the tail, so the tail cannot close it.

The fresh run takes over the slot, the hint and the branch, and gets a new number, agent, token and budget. The
spent worker is stopped. A failed stop is logged, and its pane stays open.

## Open questions

- The handover template of the private ADDENDUM §2.10 is not in this repo. This format is fanin's own, and its
  sections may need to follow that template once it is published.
- A run's branch is unknown until its first report names one. A run that stalls before that hands over without a
  branch.
- Gate verdicts (red checks, review findings) are not listed under Last errors yet. They will be once the gate
  records them per run.
