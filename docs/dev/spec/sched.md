# Scheduler spec

Status: implemented in `internal/sched` and wired into `cmd/fanind` for every project with `loop: fanin`.
Direction: [concept](../concept.md) D1, C-LOOP and C-SLOTS. Scenarios: PK-3 to PK-6, SP-8, SP-10, SP-11, LN-1 to
LN-5, WD-1 to WD-3, FP-6.

`fanind` is the event loop. The owner session only answers. The bash orchestrator keeps every project with
`loop: bash` (the default), and `fanind --shadow` keeps importing the bash state of those projects only.

## Reactor

`Scheduler.Run` reads the event cursor, resumes the live runs, then repeats one step until ctx ends. A step:

1. reads the store events after the cursor in batches of `EventBatch` (256);
2. applies the runtime events the observers sent (`WorkerRuntime.Observe`);
3. fires the due `stall` and `worker_timeout` timers;
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
(SP-8). A worker kind whose runtime is not herdr is refused with `ErrWorkerKind`. A started worker that cannot be
recorded is logged as having unknown status and keeps its slot: no second worker is started.

## Timers (WD-1, WD-2, WD-3, FP-6)

The timers are store rows armed on the injected clock, so they survive a restart. The newest sign of work is the
run's start, a `progress` event, or a runtime `working` event.

- **`stall`**: once `stall_after` has passed without a sign of work, a working run becomes stalled. A `progress`
  event or a `working` event moves it back.
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
- The MCP config path of a remote worker, the opencode sandbox (SP-9) and a forge seam for the picker request.
- Wiring `resultwatch` to `report.Service`.
- Arming the timers again for a fix round after a red gate.
