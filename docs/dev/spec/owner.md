# Owner channel

Status: implemented in `internal/owner` (service), `internal/api/owner_routes.go` and `internal/api/wait.go`
(routes), `internal/api/client/owner.go` (client) and `cmd/fanin/cmd_{answer,go,hold,pause,wait}.go` (CLI).
Direction: [concept](../concept.md), C-LOOP, migration step 4.

## Questions

- A question is an `owner_questions` row ([store](store.md)): the task moves to `needs_owner` in the same
  transaction, and `question_asked` is logged. `owner.Service.Ask` takes a `model.OwnerQuestion`;
  `AskDialog` turns a worker's dialog (text and its numbered options) into one (SCENARIOS AW-9). A task already in
  `needs_owner` keeps its state.
- Kinds: `dialog` (a worker's numbered menu; only an option number is a valid answer) and `transcript` (a
  question in the worker's output; free text too when `allow_text` is set).

`Service.SetNotifier` sets the push channel ([notify](notify.md)). After the question is stored, `Ask` sends one
note and logs `notified` or `notify_failed` with the project; a failed send never undoes the question
(SCENARIOS OA-7).

## Answers

`Service.Answer(ctx, task, raw)` takes the newest open question of the task:

1. Parse: a single digit 1–9 that names an option picks it; free text only when the question allows it. Anything
   else is `owner.ErrInvalidAnswer` with the options listed (`1) ship, 2) wait`); nothing is sent or recorded
   (SCENARIOS OA-1).
2. Deliver through the `owner.Deliverer`: a key press for a dialog, a prompt for a transcript answer. A failed
   delivery is `owner.ErrDelivery` and records nothing (SCENARIOS OA-3).
3. Record: in one transition the question is answered (`question_answered` with `owner answer for <run>: <answer>`,
   SCENARIOS OA-2) and the task leaves `needs_owner`: to `running` when the question names a run, else `queued`.

Answers are serialised, so two answers to one question cannot both be delivered. `fanind` uses
`owner.NoDeliverer` until the scheduler delivers to workers: answers fail closed there.

## Commands

| Call | Effect |
|---|---|
| `Go(task)` | the task must be in `gating`, else `ErrNotReady`; logs `owner_go` (SCENARIOS OA-6) |
| `LiftHold(id)` | lifts an active hold (SCENARIOS MG-4) |
| `Pause(project)` | sets the one-shot pause flag; the scheduler's `Tx.ConsumePause` stops before the next worker and clears it (SCENARIOS FP-2, CH-3). Returns false when already set |
| `Resume(project)` | clears the flag |

## Waiting

`GET /api/v1/wait?owner=true&after=<seq>` returns one `wire.Event` and ends (SCENARIOS WA-2). Without `after` (or
`Last-Event-ID`) it waits for events after the newest one. With `owner=true` only the events in
`owner.AttentionEvents` count: `question_asked`, `result_reported`, `hold_set`, `hook_failed`,
`pause_consumed`, `merged`. It waits on `Store.Changed()` and the request context; nothing polls.
