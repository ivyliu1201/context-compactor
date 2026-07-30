# context-compactor

[繁體中文](README.zh-TW.md)

`context-compactor` is an early-stage, local-first context compression tool for
coding agents. It is designed to preserve goals, constraints, decisions, and
implementation state from ordinary language while keeping prompt retention
bounded, secret-redacted, local, and temporary.

> Status: protocol, local SQLite journal, deterministic reducer/compiler,
> Codex and Claude hook runtime, background natural-language memory decisions,
> and detached capsule publication are implemented. Public Windows amd64
> release distribution is available, and source install remains available for
> project-local Codex/Claude workflows.

## Design goals

- Keep critical requirements and negative constraints available across long
  sessions, compaction, and resume boundaries.
- Treat repository files and explicit user instructions as sources of truth;
  compact memory is a derived, rebuildable view.
- Store structured memory mutations instead of letting a model overwrite an
  entire state document.
- Use one standard privacy policy: prompt jobs are secret-redacted, limited to
  8,000 Unicode characters, retained for at most seven days, capped at 500 per
  repository, and stored locally.
- Support Windows, macOS, and Linux from one Go codebase.
- Evaluate token reduction and task-resume quality in one 60-turn run, with
  checkpoints at turns 10, 30, 50, and 60.

## Current scope

The repository currently contains the `context-compactor/v1` protocol types,
deterministic validation rules, a repository-local SQLite event journal,
deterministic reducer/compiler, Codex and Claude hook adapters, and an
executable local runtime. A user-prompt hook returns without waiting for model
work, while the detached repository worker asks the signed-in host CLI for
either `no_change` or a typed memory update. Accepted operations rebuild the
materialized view and queue capsule publication only when memory changed. The
next supported hook can inject the published bounded context. Refer to
[SPEC.md](SPEC.md) for behavior contracts and the installation section below
for install and management instructions.

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

### Install or update from release (Windows amd64)

Run this one-line installer in Windows PowerShell 5.1+ to install the latest
release or update an existing managed installation:

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

## Natural-language memory

No special prefix or “remember” phrase is required. For example:

```text
This project must use UTC timestamps.
Finish the detached worker integration before changing the benchmark flow.
The previous Windows-only constraint is resolved.
```

The hook stores a bounded, secret-redacted extraction job and launches the
existing detached worker automatically. The background model may propose
typed goals, acceptance criteria, constraints, decisions, blockers, questions,
tasks, files, or verified test results. Deterministic validation runs before
anything reaches memory. Explanation-only, translation, general-knowledge, and
other prompts without project impact complete as `no_change` and do not
publish a new capsule.

Codex uses the signed-in `codex` CLI with `gpt-5.4-mini` for routine decisions
and `gpt-5.4` for repair. Claude uses the signed-in `claude` CLI with `haiku`
and `sonnet`. The executable, model names, and Codex reasoning effort can be
overridden with:

- `CONTEXT_COMPACTOR_CODEX_COMMAND`
- `CONTEXT_COMPACTOR_CLAUDE_COMMAND`
- `CONTEXT_COMPACTOR_CODEX_ROUTINE_MODEL`
- `CONTEXT_COMPACTOR_CODEX_REPAIR_MODEL`
- `CONTEXT_COMPACTOR_CLAUDE_ROUTINE_MODEL`
- `CONTEXT_COMPACTOR_CLAUDE_REPAIR_MODEL`
- `CONTEXT_COMPACTOR_CODEX_REASONING`
- `CONTEXT_COMPACTOR_USE_ANTHROPIC_API_KEY=1`

## Privacy model

Production exposes one `standard` policy. Its version 1 wire value remains
`balanced` for stored-data compatibility; legacy `strict` and `audit` values
remain readable but cannot be selected for new production runs. The bounded
prompt field exists only in the extraction-job table and is never rendered
directly; only validated durable facts and short evidence may reach compiled
context. See [docs/PRIVACY.md](docs/PRIVACY.md) for retention and
provider-boundary details.

## License

Licensed under the [Apache License 2.0](LICENSE).
