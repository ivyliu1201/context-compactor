<div align="center">

# 🧠 context-compactor

**Local-first context compression for coding agents**

Keep durable project context readable and bounded across Codex and Claude
sessions—without storing full prompts by default.

[繁體中文](README.zh-TW.md) · [One-command install](#windows-one-command-install-or-update) · [Privacy](#privacy) · [Verification](#verification)

[![Latest Release](https://img.shields.io/github/v/release/ivyliu1201/context-compactor?style=for-the-badge&color=4f46e5)](https://github.com/ivyliu1201/context-compactor/releases/latest)
[![CI](https://img.shields.io/github/actions/workflow/status/ivyliu1201/context-compactor/ci.yml?branch=main&style=for-the-badge&logo=githubactions&logoColor=white&label=CI)](https://github.com/ivyliu1201/context-compactor/actions/workflows/ci.yml)
![Python 3.9+](https://img.shields.io/badge/Python-3.9%2B-3776AB?style=for-the-badge&logo=python&logoColor=white)
![PowerShell 5.1+](https://img.shields.io/badge/PowerShell-5.1%2B-5391FE?style=for-the-badge&logo=powershell&logoColor=white)
[![Apache 2.0](https://img.shields.io/github/license/ivyliu1201/context-compactor?style=for-the-badge&color=0f766e)](LICENSE)

</div>

## Product

- Product mode: `standard` only.
- Python standard-library runtime, Python 3.9+.
- Detached Python worker for model updates.
- Readable state file: `.context-compactor/state.yaml`.
- Backup state file: `.context-compactor/state.backup.yaml`.
- Lightweight journal: `.context-compactor/events.sqlite`.
- Codex `SessionStart` injection matches `startup` only and never enqueues work
  or launches the model worker.
- A global Codex Hook resolves the project from each event payload's working
  directory. Project-local Hooks take precedence over the inherited global Hook.

## Windows one-command install or update

Windows PowerShell 5.1+, Git, Python 3.9+, and a signed-in Codex CLI are
required. Run this command from any directory for both the first installation
and every later update:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "& ([scriptblock]::Create((Invoke-RestMethod 'https://raw.githubusercontent.com/ivyliu1201/context-compactor/v3.3.0/scripts/bootstrap.ps1'))) -ProjectRoot ([Environment]::GetEnvironmentVariable('USERPROFILE'))"
```

The command loads a version-pinned bootstrap from the `v3.3.0` release tag.
That bootstrap downloads the latest public stable release from this repository,
checks the repository download URL, release tag, and source version, then runs
the idempotent source installer. Temporary download files are removed when it
finishes. Versioned source and a private venv are installed under
`%LOCALAPPDATA%\context-compactor` by default.

During the run, cyan `[1/4]`–`[4/4]` messages show progress. A green
`[OK] context-compactor ... is ready.` message confirms success, and
`[RESULT]` reports whether the project was installed, updated, or already up
to date. The installer JSON remains available through `scripts/install.ps1`
for automation but is no longer printed by the one-command bootstrap.

This command creates or updates the global Codex Hook in the current Windows
user's home directory. Afterwards, when Codex starts in any coding project,
the global Hook uses the event `cwd`; the first `UserPromptSubmit` automatically
registers that project, so you do not need to reinstall for every project.
`SessionStart` can read existing state for its event `cwd`, but does not register
a project. Multiple Codex sessions in the same project share one manifest,
state directory, and `events.sqlite` journal; a registry lock prevents duplicate
registration. A project-local Hook takes precedence when one exists. If you
intentionally run the source installer with a project root, it creates a
project-local Hook.

If PowerShell blocks the npm `codex.ps1` wrapper, use `codex.cmd`. Approve the
global Hook only if Codex asks for trust. The managed `SessionStart` Hook runs
only for `startup`; normal prompts are handled by the background memory worker.

### Source-tree management

The source installer remains available after cloning or extracting the source
tree. `install` is safe to repeat; the stricter `update` action requires an
existing source installation and installed project.

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action install -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action update -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action status -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action doctor -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action uninstall -ProjectRoot . -AgentHost codex
```

The source installer supports `AgentHost` values `codex`, `claude`, and
`all`. Install and update preserve existing project state and unrelated user
Hooks.

To use an external adapter instead, add an override such as:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action install -ProjectRoot . -AgentHost codex -ModelCommandJson '["python","C:\\path\\to\\model-adapter.py"]'
```

The external adapter must implement `context-compactor/model/v1`:

- Read one JSON request from stdin.
- Write exactly one JSON response to stdout:
  - `{"outcome":"no_change"}`
  - `{"outcome":"updated","state":{...complete state schema...}}`

## Privacy

- `state.yaml` never stores raw prompts, transcripts, or logs.
- `events.sqlite` may temporarily store only redacted prompts, max 8000 characters.
- Redacted prompts must be cleared immediately on success; retention is capped at seven days.
- Defined API-key, `Authorization: Bearer ...`, `Authorization: Basic ...`, bearer-token, password/secret assignment, and private-key patterns are redacted before persistence.
- Unknown new secret formats are not guaranteed to be detected.
- See [docs/PRIVACY.md](docs/PRIVACY.md).

## Verification

- `python -B -m unittest discover -s tests`
- Evidence: 82 tests ran successfully, including 2 environment-dependent skips.
- Benchmarks: [docs/benchmark-report-v3-2026-07-31.zh-TW.md](docs/benchmark-report-v3-2026-07-31.zh-TW.md)
- Live signed-in Codex CLI runs with `gpt-5.6-sol` and `high` reasoning effort.

```powershell
python -B scripts/benchmark_v3.py --stage release `
  --output benchmark-results/context-compactor-v3-standard-30turn-2026-07-31.json `
  --report docs/benchmark-report-v3-2026-07-31.zh-TW.md

python -B scripts/benchmark_v3.py --stage endurance `
  --output benchmark-results/context-compactor-v3-standard-60turn-2026-07-31.json `
  --report docs/benchmark-report-v3-2026-07-31.zh-TW.md `
  --stage1-result benchmark-results/context-compactor-v3-standard-30turn-2026-07-31.json
```

- Release stage (30 turns): pass, seeds `17,29,43`; turn-30 reductions `68.66%`, `68.65%`, `68.66%`; overall median `58.42%`; worst hook/background `17.021ms/20.838ms`.
- Endurance stage (60 turns): pass, same seeds; turn-60 reductions
  `81.97%`, `81.97%`, `82.15%`; cumulative reductions across checkpoints
  45 and 60 were `79.83%`–`79.91%`; worst hook/background
  `24.460ms/30.280ms`.
- These are observed input-token reductions in the deterministic benchmark
  scenario, not a guarantee of every live turn or billed-cost savings.
- Gates passed: correctness, privacy, state-budget, failed-candidate-corruption.
- v3.3.0 does not rerun the token benchmark because its runtime compression
  logic is unchanged.

## License

[Apache License 2.0](LICENSE)
