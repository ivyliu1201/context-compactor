# context-compactor

[繁體中文](README.zh-TW.md)

`context-compactor` is an early-stage, local-first context compression tool for
coding agents. It is designed to preserve goals, constraints, decisions, and
implementation state without persisting complete prompts by default.

> Status: protocol foundation, local SQLite journal, and deterministic memory
> reducer implemented. There is no published binary or stable CLI yet.

## Design goals

- Keep critical requirements and negative constraints available across long
  sessions, compaction, and resume boundaries.
- Treat repository files and explicit user instructions as sources of truth;
  compact memory is a derived, rebuildable view.
- Store structured memory mutations instead of letting a model overwrite an
  entire state document.
- Default to balanced privacy: no complete prompt persistence, bounded redacted
  evidence spans, and local-only storage.
- Support Windows, macOS, and Linux from one Go codebase.
- Evaluate both token reduction and task-resume quality at 10, 30, and 50 turns.

## Current scope

The repository currently contains the `context-compactor/v1` protocol types,
deterministic validation rules, a repository-local SQLite event journal, and a
deterministic memory reducer. The reducer applies lifecycle operations, marks
duplicates, separates record priority from derived conflict impact, detects
advisory and blocking contradictions, and rebuilds a digest-verified
materialized view. Context selection, agent adapters, and distribution remain
tracked in [TODO.md](TODO.md).

## Development

Requirements:

- Go 1.26 or newer

Run the current verification suite:

```sh
go test ./...
go vet ./...
```

See [SPEC.md](SPEC.md) for the behavioral contract and benchmark gates.

## Privacy model

The planned modes are:

- `strict`: structured facts only; no evidence text.
- `balanced`: structured facts plus short, redacted evidence spans. This is the
  planned default.
- `audit`: explicit opt-in retention for users who need deeper traceability.

Complete prompts are transient input in the default design, not durable memory.

## License

Licensed under the [Apache License 2.0](LICENSE).
