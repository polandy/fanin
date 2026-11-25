# Push channel

Status: implemented in `internal/notify`, wired in `internal/owner` (`Service.SetNotifier`) and `cmd/fanind`.
Direction: [concept](../concept.md), owner UI. Scenario: OA-7.

## Interface

`notify.Notifier.Notify(ctx, Note)` delivers a `Note{Project, TaskID, Text, To}` to the owner. `To` empty means the
owner's own number; any other value is `notify.ErrForeignRecipient` and nothing is sent.

| Adapter | Config | Effect |
|---|---|---|
| `Log` | `notify.kind: log` (default) | logs project, task and text |
| `Signal` | `notify.kind: signal` | runs `signal-cli -a <number> send -m <message> <number>` through `runtime.Runner` |

`notify.New(config.Notify, runner, host, log)` picks the adapter; an unknown kind is `notify.ErrKind`.

## Message

`<prefix> <text> <ui_url>/#/task/<id>`. The prefix is `notify.prefix`, default `[<host>/fanin]`. The whole message
is at most `notify.MaxChars` (600) characters: the text is cut with `…`, the prefix and the link stay.

## Failure and privacy

- A failed send is `notify.ErrSend`. Its message names the exit code, never the argv or stderr of `signal-cli`,
  which hold the number. The number is in no log line and no event.
- `owner.Service.Ask` stores the question first, then notifies. The outcome is an event with the project:
  `notified` (`owner notified of question <id>`) or `notify_failed`. The question and the `needs_owner` state stay
  either way, so the owner still finds it in the UI, the CLI and `fanin wait`.
