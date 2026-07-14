# ADR 0001: SQLite event journal

## Status

Accepted for the local event journal milestone.

## Context

`context-compactor` needs a cross-platform, local-first event journal that can
be shipped in a single Go binary. The default balanced privacy mode must not
persist complete prompts. Journal writes also need deterministic migrations,
idempotency, crash-safe transactions, and bounded retention.

## Decision

- Use `modernc.org/sqlite` through Go's `database/sql` package. It does not
  require CGO, which keeps Windows, Linux, and macOS builds aligned with the
  single-binary distribution goal.
- Pin the driver in `go.mod`. Its matching transitive `modernc.org/libc`
  version is controlled by the driver's module requirements and must not be
  upgraded independently.
- Store one database per repository at
  `.context-compactor/context.db` unless callers explicitly provide another
  path.
- Apply forward-only, checksum-verified migrations. Schema v1 uses SQLite
  `STRICT` tables.
- Require `journal_mode=WAL`, `synchronous=FULL`, `foreign_keys=ON`, and a
  5-second busy timeout. Opening the store fails when WAL cannot be enabled.
- Limit the SQL connection pool to one connection. This matches SQLite's
  single-writer behavior and makes write ordering deterministic for the first
  release.
- Persist event identity, timing, working-directory, content digest, content
  byte length, and redaction count. Do not define durable prompt, content,
  transcript, or arbitrary metadata columns.
- Persist validated mutation operations in the same transaction as their
  source event. Extraction happens while transient content is available to the
  adapter; the journal never needs the complete prompt later.
- Persist structured resume checkpoints containing current progress, last
  verification, suggested next action, and repository fingerprint. The static
  confirmation question is adapter output and is not stored.
- Treat an exact repeated event or operation identifier as an idempotent no-op.
  Reusing an identifier with different data is a conflict and rolls back the
  complete write.
- Retention may delete only processed, unreferenced events. Events newer than
  any consumer cursor and events referenced by mutation operations are kept.
  With no consumer cursor, event pruning is disabled. The latest 20 resume
  checkpoints are kept by default.

## Schema v1

The first migration creates:

- `schema_migrations`: migration version, checksum, and application time.
- `events`: immutable event metadata without complete transient content.
- `memory_operations`: validated incremental memory operations.
- `consumer_cursors`: monotonic per-consumer processing positions.
- `resume_checkpoints`: bounded structured handoff and repository state.

Audit-mode raw content retention is intentionally outside this milestone. A
future opt-in design must use separate storage and an explicit retention
policy; it must not weaken the default schema.

## Consequences

- The project gains a pure-Go SQLite dependency and its pinned transitive
  dependencies.
- `synchronous=FULL` favors durability over maximum write throughput.
- A single pooled connection serializes local writes. This is acceptable for a
  hook-driven journal and can be revisited only with concurrency benchmarks.
- The database can support reconstruction and resume without becoming an
  authoritative replacement for repository inspection.
