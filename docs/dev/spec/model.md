# Data model

Status: implemented in `internal/model`. Decision: [concept D6](../concept.md#d6-the-data-model-follows-the-symphony-spec).

fanin's domain types follow OpenAI's Symphony SPEC, pinned at commit
[`be10a1b`](https://github.com/openai/symphony/blob/be10a1b79df723d6d7612b5651c8522704dafb2e/SPEC.md)
(2026-09-15). Section numbers below refer to that version. Rows marked **fanin** have no counterpart in the SPEC.

## Entities

| fanin (`internal/model`) | Symphony SPEC | Notes |
|---|---|---|
| `Task` | §4.1.1 Issue | fanin owns its tasks in the store instead of reading a tracker. `ID` is the dispatch identity (`id`); `Title`, `Prompt` map to `title`, `description` |
| `Task.After` | §4.1.1 `blocked_by` | Hard dependencies: a task is dispatched only after every listed task is `done` (SCENARIOS CH-1) |
| `Task.Shape` (`pr`, `analysis`, `decision`) | — | **fanin**: how a task ends (a PR, a report, an owner decision) |
| `Task.OwnerTask` | — | **fanin**: a task the owner named; its project loop ends after its first merge (SCENARIOS PK-6) |
| `Task.Model()` | — | Owner model, else project model, else picked model (SCENARIOS PK-5); **fanin**. Worker kinds without a model choice (opencode) ignore it in the runtime adapter |
| `Run` | §4.1.5 Run Attempt | `Number` counts from 1 (the SPEC's `attempt` is `null` for the first run) |
| `Run.Slot` | §8.3 concurrency slot | Anonymous slot per project and host (concept C-SLOTS) |
| `Run.Token` | — | **fanin**: the run's report token; a `fanin_report` counts only with the current run's token. Prints and logs as `[redacted]` |
| `Run.Budget` | §4.1.6 token counters | Input, output and total tokens as in the SPEC, plus **fanin** fix rounds, review nudges and stalls (concept C-BUDGET) |
| `Result` | §10.4 `turn_completed` / `turn_input_required` | **fanin**: the structured worker report (below) |
| `Event` | §10.4 emitted runtime events | fanin's events are durable store rows, appended in the state change's transaction (concept D3) |
| `Verdict` | — | **fanin**: forge gates (below) |
| `Hold` | — | **fanin**: project holds (below) |
| `OwnerQuestion` | §10.5 user input policy | **fanin**: the SPEC fails or auto-answers input requests; fanin parks the task as `needs_owner` |

## Task states

Symphony §7.1 has claim states only (`Unclaimed`, `Claimed`, `Running`, `RetryQueued`, `Released`) and leaves the
rest to the tracker. fanin keeps the whole lifecycle in one table, because it owns the gates and the merge.

| fanin `TaskState` | Symphony | Allowed next states |
|---|---|---|
| `queued` | Unclaimed, RetryQueued | `running`, `canceled` |
| `running` | Claimed + Running | `gating`, `queued`, `needs_owner`, `paused`, `done`, `failed`, `canceled` |
| `gating` | — **fanin** (forge gates) | `running`, `post_merge`, `done`, `needs_owner`, `paused`, `failed`, `canceled` |
| `post_merge` | — **fanin** (post-merge hooks) | `done` |
| `needs_owner` | — **fanin** | `queued`, `running`, `done`, `canceled` |
| `paused` | — **fanin** (budget spent) | `queued`, `canceled` |
| `failed` | Released (error) | `queued`, `canceled` |
| `done` | Released (terminal) | — |
| `canceled` | Released (terminal) | — |

- `running -> queued` is the SPEC's continuation retry: a spent run hands over to a fresh run.
- `gating -> running` is a fix round: CI or review is red and the worker is nudged.
- `gating -> done` is a merge without a post-merge hook; `post_merge -> done` follows the hook whether it
  passes or fails, since a failed hook leaves a `merge` hold, not a failed task (SCENARIOS MG-4).
- Any change not in the table returns an error wrapping `model.ErrTransition`.

## Run states

| fanin `RunState` | Symphony §7.2 | Allowed next states |
|---|---|---|
| `starting` | PreparingWorkspace … InitializingSession | `working`, `failed`, `canceled` |
| `working` | StreamingTurn | `reported`, `stalled`, `failed`, `timed_out`, `rolled_over`, `canceled` |
| `stalled` | Stalled (non-terminal in fanin) | `working`, `failed`, `timed_out`, `rolled_over`, `canceled` |
| `reported` | Finishing | `working`, `succeeded`, `rolled_over`, `canceled` |
| `succeeded` | Succeeded | — |
| `failed` | Failed | — |
| `timed_out` | TimedOut | — |
| `rolled_over` | — **fanin** | — |
| `canceled` | CanceledByReconciliation | — |

- A stall is not terminal in fanin: one nudge may bring the worker back (SCENARIOS AW-10).
- `reported` means the worker has reported and stays open; a red gate nudges it back to `working`.
- `rolled_over` ends a run whose budget is spent; the next run of the task gets a handover (concept C-BUDGET).

## Worker results

The bash statuses become `ResultKind` values; each kind requires one field (`Result.Validate`).

| Kind | Bash status | Required field |
|---|---|---|
| `done_pr` | `DONE PR #n` | `PR` |
| `pushed` | `PUSHED <branch>` | `Branch` |
| `committed` | `COMMITTED <branch>` | `Branch` |
| `issue` | `ISSUE #n` | `Issue` |
| `nothing` | `NOTHING: …` | `Text` |
| `blocked` | `BLOCKED: …` | `Text` |

`Result.Normalize` joins every whitespace run of `Branch` and `Text` into one space and cuts them to
`model.FieldCap` (400) characters (SCENARIOS AW-2). The optional `IssueURL` of an `issue` must be an absolute
`http(s)` URL of at most `FieldCap` characters; it is refused, never cut.

## fanin additions

- **Gate verdicts** (`green`, `pending`, `red`): one gate's answer for one PR head. An unreadable gate is an
  error, never a verdict (SCENARIOS FP-6).
- **Holds** (`merge`, `ci`): a `merge` hold is written before a post-merge hook runs and stays when the hook
  fails or its outcome is unknown (SCENARIOS MG-4, MG-5). A `ci` hold names its `FixTask`, whose merge lifts it
  (SCENARIOS MG-8).
- **Post-merge hooks**: the `post_merge` task state and the `hook_*` events. The SPEC's workspace hooks (§9.4)
  run around a run, not after a merge.
- **Owner questions** (`needs_owner`): a question that stands on its own, 2 to 4 options with distinct labels,
  and the index of the recommended option (`OwnerQuestion.Validate`). Its `Kind` is `dialog` or `transcript`;
  only a transcript question may allow free text. Owner commands log `owner_go`, `project_paused`,
  `project_resumed` and `pause_consumed`, see [owner](owner.md).

## Rules

- Stored values are the lowercase constants. `Parse*` turns a stored string back into its type and returns
  `model.ErrUnknownValue` for anything else, so an unknown value from the store is an error, never a state.
- No package outside `internal/model` spells a task or run state as a string literal;
  `TestNoStateLiteralsOutsideModel` enforces it.
