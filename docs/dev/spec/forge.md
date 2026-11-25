# Forge spec

`internal/forge` is the seam to a code forge (concept D4): fanin opens PRs, sets the merge intent, posts commit
statuses and watches; the forge's branch protection decides green and the forge merges. `internal/forge/github`
implements it over `gh` and `gh api`, called through `runtime.Runner`. `gh` owns the authentication, so fanin
never reads or stores a GitHub token. `internal/forge/gitea` implements it for Gitea and Forgejo over the REST API
with `net/http` (see Gitea below).

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
is pending, and the post time of a status. `Check.Description` is a status's description, such as the review's
reasons; a check run has none.

A PR number a worker reports goes through `forge.ParseNumber` (`42` or `#42`); anything else is
`ErrInvalidRequest` before any forge call (SCENARIOS GT-6).

## Resolver (SCENARIOS GT-2)

`forge.NewResolver(forges, projects)` takes the configured forges by name (kind `github` or `gitea`, Forgejo uses
`gitea`, plus the client) and the forge name of each project. A forge without a client or with an unknown kind,
and a project that names a missing forge, is `ErrInvalidRequest`. `For(project)` returns the client and kind;
`Require(project, kind)` refuses a project of another kind with `ErrWrongForge`, such as
`beta is not a gitea project`. An unknown project is `ErrUnknownProject`.

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

## Gitea and Forgejo

`gitea.New(httpClient, url, token, log)` talks to `<url>/api/v1`. The token is a `forge.Secret`, read from the
variable that `token_env` names (`forge.SecretFromEnv`), and goes only into `Authorization: token …`. A URL with
credentials, query or fragment and an empty token are refused.

| Method | Gitea call |
|---|---|
| `PR(repo, n)` | `GET repos/R/pulls/n`, then `GET repos/R/commits/SHA/status` (paged) |
| `Files(repo, n)` | `GET repos/R/pulls/n/files` (paged) |
| `ListOpen(repo)` | `GET repos/R/pulls?state=open` (paged) |
| `Open(req)` | `POST repos/R/pulls {head, base, title, body}` |
| `EnableAutoMerge(req)` | `POST repos/R/pulls/n/merge {do: squash, …, head_commit_id, merge_when_checks_succeed: true, delete_branch_after_merge: true}` |
| `PostStatus(st)` | `POST repos/R/statuses/SHA {state, context, description[, target_url]}` |
| `SquashMerge(req)` | `GET repos/R/pulls/n`, then the merge call without `merge_when_checks_succeed` |
| `DeleteBranch(repo, b)` | `DELETE repos/R/branches/b` |

- Every list is read in pages of 50; more than 20 pages is `ErrUnreadable`, never a truncated list.
- State `open` is open, `closed` with `merged` is merged, `closed` alone is closed. `mergeable: false` counts as
  conflicting, as in `gitea-gate.sh`; a missing `mergeable`, head branch, head SHA or base branch is unreadable.
- Status states: `pending` and `running` are pending, `success` success, `skipped` skipped; `failure`, `error`
  and `warning` count as failure. A status needs a context and a readable `updated_at` (or `created_at`).
- `PR` fails with the plain error when the PR is unreadable. When only the statuses are unreadable it wraps
  `forge.ErrChecksUnreadable`; the gate reads that, and an error from `Files`, as pending, never green
  (SCENARIOS GT-6).
- HTTP 404 wraps `ErrNotFound`; transport failures and HTTP 429, 502, 503 and 504 wrap `ErrNetwork`; 401 and
  403 point at the token. Gitea merges are not retried (MG-6 retries on GitHub only); the merge always sets
  `delete_branch_after_merge`.

`OpenBranch(repo, base, branch)` handles a `PUSHED <branch>` (SCENARIOS GT-1): a branch outside
`[A-Za-z0-9._/-]`, with `..`, a leading `-` or `/`, a trailing `/`, or the base itself is refused before any call.
An open PR whose head is the branch is reused. If the open PRs cannot be listed, it is an error and no PR is
opened. Otherwise the commits come from `GET repos/R/compare/base...branch` (Gitea 1.22 or later; it lists them
newest first); the title is the oldest commit's subject, the body lists every subject with its body below, and
ends with `Opened by fanin.`. No commits beyond the base is `ErrInvalidRequest`.

## Tokens (SCENARIOS GT-10)

`forge.Redact` replaces GitHub tokens (`gh?_…`, `github_pat_…`), `Authorization` header values, `token=` style
parameters and URL credentials with `[REDACTED]`. The GitHub client redacts the `gh` stderr it puts into errors
and never wraps the `runtime.ExitError` (its argv and stderr would reach callers raw); other runner errors are
wrapped by `forge.RedactError`, which redacts the text and keeps the chain for `errors.Is`. Retry log lines carry
only the redacted error.

`forge.Secret` prints, logs and marshals (JSON, text, YAML) as `[REDACTED]`, and an unexported `Secret` field
prints as a pointer, so neither logs nor config dumps carry the Gitea token. The Gitea client scrubs the literal
token and every `Redact` pattern from answer bodies and transport errors before they enter an error or a log
line.
