# context-compactor

[繁體中文](README.zh-TW.md)

`context-compactor` is an early-stage, local-first context compression tool for
coding agents. It is designed to preserve goals, constraints, decisions, and
implementation state without persisting complete prompts by default.

> Status: protocol, local SQLite journal, deterministic reducer/compiler,
> Codex and Claude hook runtime, and durable capsule-refresh handoff are
> implemented. Public Windows amd64 release distribution is available, and
> source install remains available for project-local Codex/Claude workflows.

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
Capsule refreshes are durably queued to a recoverable worker instead of using
short-lived goroutines. Refer to [SPEC.md](SPEC.md) for behavior contracts and
the installation section below for install and management instructions.

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

## Installation

### Install from release (Windows amd64)

Run this one-line installer in Windows PowerShell 5.1+:

```powershell
irm https://raw.githubusercontent.com/ivyliu1201/context-compactor/main/scripts/install-release.ps1 | iex
```

The installer downloads the release executable and `checksums.txt` from the
latest GitHub Release, verifies SHA-256, runs `self-check`, then executes
`install`, `status`, and `doctor`.

- Cyan: step in progress.
- Green: success.
- Yellow: action required.
- Red: failure.
- Raw CLI JSON is not shown to end users.
- Without an explicit `-ProjectRoot`, Codex configuration and the install
  manifest are placed under user `HOME`, and the Hook command has no fixed
  `--project-root`.
- Runtime project root is derived from each Codex Hook payload `cwd`.
- With an explicit `-ProjectRoot`, fixed project-local root behavior is
  preserved.
- The executable is installed under `%LOCALAPPDATA%\context-compactor`.
- Codex may still require `/hooks` review or trust for activation.

### Install from source (project-local, Docker-based)

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot .
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot . -AgentHost claude
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot . -AgentHost all
```

These source installs support project-local Codex or Claude setup and require
Docker.

### Remove global Codex Hook

```powershell
$m = Get-Content (Join-Path $HOME ".context-compactor\install.json") -Raw | ConvertFrom-Json; & $m.hosts.codex.executable uninstall --host codex --project-root $HOME
```

This CLI uninstall removes managed Hook definitions and manifest entries
tracked by the installer. It does not delete the installed executable.

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
