# Forge spec

`internal/forge` is the seam to a code forge (concept D4): fanin opens PRs, sets the merge intent, posts commit
statuses and watches; the forge's branch protection decides green and the forge merges. `internal/forge/github`
implements it over `gh` and `gh api`, called through `runtime.Runner`. `gh` owns the authentication, so fanin
never reads or stores a GitHub token. A Gitea client follows with the Gitea gate.

## Interface

`forge.Forge` takes the repo as `owner/name`:

| Method | GitHub call |
|---|---|
| `PR(repo, n)` | `gh pr view n --repo R --json number,title,url,state,mergeable,headRefName,headRefOid,baseRefName,statusCheckRollup` |
| `Files(repo, n)` | `gh api --paginate repos/R/pulls/n/files --jq '.[].filename'` (not capped at 100 like `gh pr view`) |
| `ListOpen(repo)` | `gh pr list --repo R --state open --limit 1000 --json …` (no checks, no mergeable) |
| `Open(req)` | `gh pr create --repo R --head H --base B --title T --body-file -`, body on stdin; returns the PR number |
| `EnableAutoMerge(req)` | `gh pr merge n --repo R --squash --auto --subject S --body B --match-head-commit SHA` |
| `PostStatus(st)` | `gh api --method POST repos/R/statuses/SHA -f state= -f context= -f description= [-f target_url=]` |
| `SquashMerge(req)` | view, then `gh pr merge n --repo R --squash --subject S --body B --match-head-commit SHA` |
| `DeleteBranch(repo, b)` | `gh api --method DELETE repos/R/git/refs/heads/b` |

Checks are normalised to `pending`, `success`, `failure`, `cancelled` and `skipped`. A check run that is not
`COMPLETED` is pending; `NEUTRAL` counts as success; `TIMED_OUT`, `ACTION_REQUIRED`, `STARTUP_FAILURE` and
`STALE` count as failure. A commit status (`StatusContext`, such as the review verdict of D5) maps `EXPECTED` and
`PENDING` to pending and `ERROR` to failure. `Check.UpdatedAt` is the completion time, the start time while a run
is pending, and the post time of a status.

## Unreadable answers (SCENARIOS GH-5, GH-14)

An answer that cannot be read is an error wrapping `forge.ErrUnreadable`, never a PR or a verdict: empty or broken
JSON, trailing data, another PR number, an unknown state, mergeable, check type, conclusion or status state, a
missing head branch, head SHA or base branch, a completed check without a readable completion time, and a status
without a readable post time (the review time). `Files` rejects an empty path line.

## Errors

`gh` exits 1 for every failure, so the client sorts by stderr: transport failures (`error connecting to`,
`dial tcp`, `i/o timeout`, `HTTP 502/503/504`, …) wrap `forge.ErrNetwork`; `HTTP 404`, `Could not resolve to a`,
`no pull requests found` and `Reference does not exist` wrap `forge.ErrNotFound`; anything else is a plain error
that points at `gh auth status`. A request fanin must not send (a merge without head SHA, a PR without a head) is
`forge.ErrInvalidRequest` before `gh` runs. A shutdown returns the context error.

## Squash merge (SCENARIOS MG-6)

- `forge.NewSquash(pr, trailers)` builds the request: subject `<title> (#n)`, a body of `Key: Value` trailers only,
  and the head SHA. A PR without title, head branch or head SHA is `ErrInvalidRequest`.
- `SquashMerge` reads the PR first. An unreadable view (such as one without a head branch) stops before the merge;
  a merged PR is success; a closed one is `forge.ErrNotOpen`.
- A merge that fails with `ErrNetwork` is retried after 10, 30 and 60 s on the injected `Clock`. Before every
  retry the PR is read again, and a PR merged meanwhile ends the loop without a second merge. After the last retry
  the network error is returned; any other error is returned at once.

## Tokens (SCENARIOS GT-10)

`forge.Redact` replaces GitHub tokens (`gh?_…`, `github_pat_…`), `Authorization` header values, `token=` style
parameters and URL credentials with `[REDACTED]`. The GitHub client redacts the `gh` stderr it puts into errors
and never wraps the `runtime.ExitError` (its argv and stderr would reach callers raw); other runner errors are
wrapped by `forge.RedactError`, which redacts the text and keeps the chain for `errors.Is`. Retry log lines carry
only the redacted error.
