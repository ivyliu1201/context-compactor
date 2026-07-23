# context-compactor

[繁體中文](README.zh-TW.md)

`context-compactor` is an early-stage, local-first context compression tool for
coding agents. It is designed to preserve goals, constraints, decisions, and
implementation state without persisting complete prompts by default.

> Status: the protocol, local SQLite journal, deterministic reducer/compiler,
> Codex and Claude hook runtime, and durable capsule-refresh handoff are
> implemented. Project-local install management is available from source, but
> there is no published binary yet.

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
- Evaluate token reduction and task-resume quality in one 60-turn run, with
  checkpoints at turns 10, 30, 50, and 60.

## Current scope

The repository currently contains the `context-compactor/v1` protocol types,
deterministic validation rules, a repository-local SQLite event journal,
deterministic reducer/compiler, Codex and Claude hook adapters, and an
executable local runtime. Hook invocations atomically append validated memory
operations and rebuild the materialized view before emitting bounded context.
Capsule refreshes are durably queued for a recoverable worker instead of being
left in a short-lived goroutine. Installation and distribution remain tracked
in [TODO.md](TODO.md).

## Development

Requirements:

- Go 1.26 or newer

Run the current verification suite:

```sh
go test ./...
go vet ./...
```

See [SPEC.md](SPEC.md) for the behavioral contract and benchmark gates.

Build and invoke the source-stage executable:

```sh
go build -o context-compactor ./cmd/context-compactor
context-compactor install --host all --project-root /path/to/repository
context-compactor status --host all --project-root /path/to/repository
context-compactor doctor --host all --project-root /path/to/repository
context-compactor refresh-worker --project-root /path/to/repository
context-compactor uninstall --host all --project-root /path/to/repository
```

The hook reads exactly one host payload from standard input and reserves
standard output for the host JSON response. Installation merges only the five
context-compactor lifecycle hooks into project-local configuration and records
their exact commands in the ignored `.context-compactor/install.json` file.
Uninstall removes only exact managed entries and refuses ambiguous, user-edited
definitions.

Codex definitions are written to `.codex/hooks.json`. Codex requires reviewing
and trusting new or changed project hooks through `/hooks`, so status reports
`awaiting_manual_trust` instead of claiming that compression is active. Claude
definitions are written to `.claude/settings.local.json`. Doctor executes the
installed binary's bounded self-check and verifies every managed definition;
host activation remains unknown when external trust or enterprise policy
cannot be inspected. Refresh-worker scheduling is still configured manually.

Ordinary prompt text remains transient. The built-in deterministic extractor
persists only explicit directives such as:

```text
[context-compactor] goal: Ship the bounded hook runtime.
[context-compactor] task: Verify the durable refresh worker.
[context-compactor] resolve: record-id
```

Supported record names are `goal`, `acceptance_criterion`, `constraint`,
`decision`, `blocker`, `question`, `task`, `file`, and `test_result`; lifecycle
directives are `resolve` and `expire`.

## Privacy model

The planned modes are:

- `strict`: structured facts only; no evidence text.
- `balanced`: structured facts plus short, redacted evidence spans. This is the
  planned default.
- `audit`: explicit opt-in retention for users who need deeper traceability.

Complete prompts are transient input in the default design, not durable memory.

## License

Licensed under the [Apache License 2.0](LICENSE).
