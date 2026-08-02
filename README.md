# context-compactor

[中文說明](README.zh-TW.md)

`context-compactor` is a local-first context-compaction product for coding agents.

## Product

- Product mode: `standard` only.
- Python standard-library runtime, Python 3.9+.
- Detached Python worker for model updates.
- Readable state file: `.context-compactor/state.yaml`.
- Backup state file: `.context-compactor/state.backup.yaml`.
- Lightweight journal: `.context-compactor/events.sqlite`.
- Codex `SessionStart` injection matches `startup` only and never enqueues work
  or launches the model worker.

## Windows one-command install or update

Open PowerShell in the root of the coding project where the Hook should run.
Windows PowerShell 5.1+, Git, Python 3.9+, and a signed-in Codex CLI are
required. Run this same command for both the first installation and every
later update:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "& ([scriptblock]::Create((Invoke-RestMethod 'https://raw.githubusercontent.com/ivyliu1201/context-compactor/v3.1.0/scripts/bootstrap.ps1')))"
```

The command loads a version-pinned bootstrap from the `v3.1.0` release tag.
That bootstrap downloads the latest public stable release from this repository,
checks the repository download URL, release tag, and source version, then runs
the idempotent source installer. Temporary download files are removed when it
finishes. Versioned source and a private venv are installed under
`%LOCALAPPDATA%\context-compactor` by default.

After the command returns JSON with `"ok": true`, start Codex from that same
project directory. If PowerShell blocks the npm `codex.ps1` wrapper, use
`codex.cmd`. Approve the project Hook if Codex asks for trust. The managed
`SessionStart` Hook runs only for `startup`; normal prompts are handled by
the background memory worker.

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
`all`. Install/update removes only V2 Hook handlers exactly recorded by a
recognized V2 manifest. It preserves the manifest, legacy `context.db`,
project state, and unrelated user Hooks.

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

## Legacy migration

```
python -m context_compactor migrate preview --project-root .
python -m context_compactor migrate apply --project-root .
```

The migration reads legacy `.context-compactor/context.db` and never modifies or deletes the original database.

## Verification

- `python -B -m unittest discover -s tests`
- Evidence: 79 tests ran successfully, including 2 environment-dependent skips.
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

## License

[Apache License 2.0](LICENSE)
