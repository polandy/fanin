# Observability

Structured logs with IDs, Prometheus metrics, `fanin overview` and `fanin doctor` (concept: Observability;
scenarios OB-1 to OB-4, ST-2).

## Metrics

`internal/metrics` renders the text format at scrape time from `store` count queries (`TaskCounts`, `RunCounts`,
`EventCounts`), so a restart loses nothing and a transition raises its counter in the commit that appends its
event. A store error is a 500 with the cause; the endpoint never answers with partial numbers.

| Metric | Type | Labels | Source |
|---|---|---|---|
| `fanin_tasks` | gauge | `project`, `state` | tasks per state, every state listed |
| `fanin_runs` | gauge | `project`, `state` | runs per state of their task's project |
| `fanin_runs_started_total` | counter | `project` | `run_started` events |
| `fanin_nudges_total` | counter | `project` | `worker_nudged` events |
| `fanin_merges_total` | counter | `project` | `merged` events |
| `fanin_adapter_errors_total` | counter | `adapter` (`hook`, `notify`), `project` | `hook_failed`, `notify_failed` events |

Without any task the series carry `project=""` and the value 0. `GET /metrics` needs no token, like the snapshot;
bind `listen` to a trusted interface.

## fanin overview

Prints the snapshot's board per project: done this run (count and the newest four), next, waiting for owner,
holds (with the `fanin hold lift` command) and decisions. `fanin overview [project...]`.

## fanin doctor

`fanin doctor` (needs `--config`) prints a table `CHECK SUBJECT STATUS DETAIL` and exits 1 on any `FAIL` row.

| Check | Subject | How |
|---|---|---|
| `config` | | `config.Load`; an invalid config fails every dependent row |
| `store` | | the file exists and opens with its migrations |
| `herdr` | | `herdr workspace list`; `skip` when no worker kind runs in herdr |
| `forge-auth` | project | `gh auth status`, or an authenticated `GET /api/v1/user` on a Gitea forge |
| `branch-protection` | project | the base branch requires `fanin/review` (`gh api`, or Gitea's `branch_protections`) |
| `systemd` | `fanind` | `systemctl is-active fanind` is `active` |

Every call goes through `runtime.Runner`. The Gitea token goes to `curl` on stdin, never into argv. Every detail is
scrubbed of the configured token values and of anything that looks like a credential. A project on a remote host
is a `skip` with its reason: run `fanin doctor` there.

## Logs

Every task-bound line carries `project`, `task_id` and `run_id` (`sched.logFor`); `internal/sched/logids_test.go`
runs the dispatch, spawn failure, timeout and nudge paths and checks every line.
