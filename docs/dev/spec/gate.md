# Gate spec

`internal/gate` reads a PR from its forge and answers GREEN, PENDING or RED with reasons, or an error. It decides
nudges, readies and waits, never the merge: the forge's branch protection merges (concept D4). An unreadable
answer is an error wrapping `gate.ErrUnreadable`, never a verdict (SCENARIOS FP-6).

## Reads

`gate.Read(ctx, reader, target)` reads one PR once. The target names the forge kind, the repo, the PR number and
the base the PR must aim at. A target without repo or with a PR number below 1 is `forge.ErrInvalidRequest`
before any forge call; a worker's PR number goes through `forge.ParseNumber` first (GT-6).

## GitHub verdicts (SCENARIOS GH-1 to GH-5)

The review is a commit status `fanin/review` (`gate.ReviewContext`) on the PR head (concept D5): the forge reads
the head's checks only, so a review of an older head is no review. Only the newest `fanin/review` status counts;
its description carries the review's reasons. Every other check is CI. The first rule that matches wins:

| Rule | Verdict |
|---|---|
| PR not open | RED `PR is MERGED, not OPEN` |
| `CONFLICTING` | RED `CONFLICTING` |
| A failed or cancelled check (any state but success, skipped or pending) | RED `failed: …`, review reasons appended |
| A check pending | PENDING `checks running` (review findings wait until CI settles) |
| No CI check | PENDING `no checks` |
| Mergeable not `MERGEABLE` | PENDING `mergeable UNKNOWN` |
| No review on the head | RED `no review for head <sha>`, cause `no_review`, `Result.Head` the head |
| Review pending or skipped | RED `review has no verdict` |
| Review failed | RED `review: not ready: <description>` |
| Otherwise | GREEN `<title>` |

A PR that cannot be read (GH-5), including a review status without a readable time, is an error.

## Gitea verdicts (SCENARIOS GT-3 to GT-6)

All RED reasons are given together: the PR is not open, it aims at another base, `CONFLICTING`, failed checks. A
conflict thus wins over running checks. Without a RED reason: running checks are PENDING `checks running`, no
status is PENDING `no checks`, an unknown mergeable is PENDING. A `fanin/review` status is one more check here,
so a review still running keeps the gate PENDING. Otherwise the files decide: docs-only is
`GREEN docs`, anything else `GREEN code`.

An unreadable PR is an error. Unreadable statuses (`forge.ErrChecksUnreadable`) are PENDING
`statuses unreadable`; unreadable files are PENDING `files unreadable`; neither is ever GREEN.

## Task scope (SCENARIOS SC-1, SC-2)

`internal/scope` owns a task's `allowed_paths`. `scope.Validate` takes at most `scope.MaxPaths` (20) entries, each
repo-relative: none empty, absolute, with a drive prefix, a backslash, a `..` segment, a control character or a
malformed glob (`scope.ErrInvalid`). An entry ending in `/` holds everything below it, `*` stays inside one
directory and a `**` segment spans any number of directories.

`scope.Post` posts the commit status `fanin/scope` (`scope.Context`) on the PR head: failure with the files outside
the scope (cut to 140 runes) when the PR's changed files (its diff against the base) leave it, success otherwise or
when the task has no scope (its files are then not read). A file list that cannot be read posts nothing and is an
error, as is a failed post. The gate reads `fanin/scope` as one more CI check, so a failure is RED cause `ci`;
a branch protection can require it like `fanin/review`. It never counts as the project's CI: a head whose only
check is `fanin/scope` is PENDING `no checks` (Gitea: on the way to `NOCI`).

## Causes

Every RED carries its cause (`gate.Cause`): `closed` (not open, or another base), `conflict`, `ci` (failed
checks; on Gitea any other RED), `no_review` (a GitHub head without any review, every other check green), `review`
(none with a verdict, or a failed one) or `timeout` (a wait or poll limit). A conflict wins over failed checks. The scheduler picks the nudge by it.

## Docs-only (SCENARIOS GH-12)

`gate.DocsOnly(files)`: the list is non-empty and every path starts with `docs/` or ends in `.md`, except any
`CLAUDE.md` and anything under a `.claude/` directory, which count as code.

## Waits (SCENARIOS GH-6, GH-7, GT-4)

`gate.NewWatcher(reader, clock, limits, project, task, target)` runs one task's gate with the `gate` limits of the
project config. `Turn(ctx, timers, state)` reads once and returns a `Step`: a final result, a final error, or a
timer row it armed through `timers` (a `store.Tx`) to come back. A timer that cannot be armed is an error. The
`State` counts what the episode used up; the caller keeps it with the task, and a new head starts from the zero
state. A webhook may re-gate before the
timer is due; every turn counts.

| Case | Timer | Limit | Then |
|---|---|---|---|
| Read failed | `gate_retry`, 60 s | 3 failed reads in a row | `gate unreachable` (`gate.ErrUnreachable`), no verdict, nobody nudged |
| GitHub PENDING | `gate_wait`, `watch_timeout` | `max_waits` | RED `still <reason> after <n> waits` |
| Network failure after a GitHub wait | `gate_retry`, 60 s, uses up no wait | `max_watch_failures` | `gate unreachable` |
| GitHub verdict after a wait | `gate_settle`, `settle`, once | | the next read is final |
| Gitea `no checks` | `gate_poll`, `poll_interval` | `noci_after_polls` | `NOCI` |
| Gitea other PENDING | `gate_poll`, `poll_interval` | `max_polls` | RED `still <reason> after <n> polls` |

An invalid target is final at once with `forge.ErrInvalidRequest`.
