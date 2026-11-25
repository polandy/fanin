# ADR-0001: Go as the implementation language

Status: accepted

## Context

[Concept D1](../concept.md) chose Go for `fanind` and `fanin`. This ADR records the tradeoff against Rust, the
one real alternative, so the choice is not reopened without new facts.

fanin is orchestration, not computation. Its time goes to waiting on external processes and services: the
terminal multiplexer, the forge API, CI, the worker agents. It owns one SQLite store and a small HTTP API.

## Options

- **Go.** One static binary per host, goroutines and `context` for a reactor with adapters reporting over
  channels, a fast compile-test loop, and a standard library that covers HTTP, JSON, `slog` and `testing`.
  Costs: weaker type-level guarantees (no sum types), and errors and nil handling rest on discipline and lint.
- **Rust.** Stronger types for task and event states, no data races by construction, and a smaller runtime
  footprint. Costs: slower builds and iteration, an async runtime and its ecosystem choices to carry, and more
  code for the same I/O glue.

## Decision

Go.

- **I/O-bound work.** Nothing in fanin is CPU-bound, so Rust's performance edge buys nothing here.
- **Process boundary to herdr.** fanin drives the terminal multiplexer through its CLI and socket, not through a
  linked library, so a shared language gives no integration benefit.
- **Guidelines are written for Go.** [`guidelines.md`](../guidelines.md) and the CI mirror (vet, shuffle, race,
  revive, govulncheck) already encode how Go's weak spots are covered: sentinel errors, `ctx` first, `safego.Go`,
  one writer through the store.
- **Short worker loop.** The owner's other tools are Go, and a worker that picks up one task has to build,
  test and push in minutes; Go's iteration speed keeps that loop short.

## Consequences

- States and verdicts are named string constants checked by tests and lint, not by the compiler.
- If a part later turns CPU-bound or needs stronger invariants, it can be a separate Rust binary behind a process
  boundary; this ADR would then be superseded for that part only.
