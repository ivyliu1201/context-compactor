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

Run the formal benchmark with foreground model checks:

```sh
export OPENAI_API_KEY="..."
docker run --rm -e OPENAI_API_KEY -v "$PWD:/workspace" -w /workspace \
  -e GOCACHE=/workspace/.cache/go-build \
  -e GOMODCACHE=/workspace/.cache/go-mod \
  golang:1.26 go run ./cmd/context-compactor benchmark --matrix formal \
  --model-command /usr/bin/python3 \
  --model-arg /workspace/scripts/foreground_model_openai.py
```

Set `OPENAI_FOREGROUND_MODEL` to override the default foreground model. When no
model command is configured, the benchmark still reports token and deterministic
gates, but model-dependent gates remain `not_evaluated`.

Install from this checked-out source (Windows PowerShell 5.1+):

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot .
```

The installer defaults `AgentHost` to `codex`, and supports `-AgentHost claude` or
`-AgentHost all`. It builds `amd64` with Docker (`golang:1.26`), installs to
`%LOCALAPPDATA%\context-compactor` with a SHA-256-first-12 filename, then runs
`self-check`, `install`, `status`, and `doctor`. This is still source-stage
installation; there is no public GitHub Release or public binary download path yet.

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot . -AgentHost claude
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot . -AgentHost all
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
