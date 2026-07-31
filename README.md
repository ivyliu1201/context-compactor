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

## Windows source installer

- Uses `scripts/install.ps1` (PowerShell 5.1+, Windows only).
- Source is copied into a private venv under `%LOCALAPPDATA%\context-compactor` by default.
- Supports `install`, `update`, `status`, `doctor`, and `uninstall`.
- Supports `AgentHost` values `codex`, `claude`, and `all`.
- `install` and `update` require `-ModelCommandJson`.

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 `
  -Action install -ProjectRoot . -AgentHost all `
  -ModelCommandJson '["python","C:\\path\\to\\model-adapter.py"]'

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 `
  -Action update -ProjectRoot . -AgentHost all `
  -ModelCommandJson '["python","C:\\path\\to\\model-adapter.py"]'

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action status -ProjectRoot . -AgentHost all

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action doctor -ProjectRoot . -AgentHost all

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action uninstall -ProjectRoot . -AgentHost all
```

No bundled provider adapter is included. Use an external adapter that implements `context-compactor/model/v1`:

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
- Evidence: 68 tests ran successfully, including 2 opt-in skips.
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
- Endurance stage (60 turns): pass, same seeds; turn-60 reductions `81.97%`, `81.97%`, `82.15%`; overall median `79.83%`; worst hook/background `24.460ms/30.280ms`.
- Gates passed: correctness, privacy, state-budget, failed-candidate-corruption.

## License

[Apache License 2.0](LICENSE)
