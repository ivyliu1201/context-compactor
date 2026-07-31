# context-compactor v3.0.0 (Draft)

This is the proposed next major release draft. It is not tagged, not published, and
does not modify any package version.

## Highlights

- Shift to a single `standard` production mode.
- Adopt a Python 3.9+ standard-library runtime with a detached worker process.
- Use readable repository state files and lightweight journaling:
  `.context-compactor/state.yaml`, `.context-compactor/state.backup.yaml`, and
  `.context-compactor/events.sqlite`.
- Use a source-only Windows PowerShell 5.1+ installation flow with private venv at
  `%LOCALAPPDATA%\context-compactor`.
- Replace native executable/install-release and provider-wrapper flow with a
  source and external-adapter workflow.
- Remove legacy native and Go-dependent release behavior.
- See [README](../../README.md), [PRIVACY](../PRIVACY.md), and
  [benchmark report](../benchmark-report-v3-2026-07-31.zh-TW.md).

## Changes

- Added `state.backup.yaml` plus a masked, bounded `events.sqlite` journal.
- Added installer actions for `install`, `update`, `status`, `doctor`, and
  `uninstall`.
- Added read-only migration support for `.context-compactor/context.db` with
  explicit apply.
- Added formal benchmark evidence collection for 30-turn release and 60-turn
  endurance runs.

## Breaking Changes

- Native executable artifacts are removed from releases; source-only distribution is
  the distribution model.
- Provider wrappers are removed; install/update require an external adapter via
  `ModelCommandJson`.
- The legacy `context.db` path is now handled through read-only migration preview
  plus explicit apply.

## Migration

1. Install from source (PowerShell 5.1+):

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 `
  -Action install -ProjectRoot . -AgentHost all `
  -ModelCommandJson '["python","C:\\path\\to\\model-adapter.py"]'
```

2. Migrate from legacy DB (read-only preview first):

```powershell
python -m context_compactor migrate preview --project-root .
```

3. Apply migration after review:

```powershell
python -m context_compactor migrate apply --project-root .
```

The migration preview is read-only, and apply creates the new standard state without modifying or deleting the original `.context-compactor/context.db`.

## Verification

- 68 tests ran successfully, including 2 opt-in skips, in the combined standard verification set.
- Fresh-project and privacy E2E tests included.
- Migration tests and management action tests included.
- Python AST syntax and PowerShell syntax checks included.
- 30-turn benchmark (seeds 17/29/43, signed-in Codex CLI `gpt-5.6-sol/high`):
  68.66%, 68.65%, 68.66% token reduction.
- 60-turn benchmark (same seeds and model):
  81.97%, 81.97%, 82.15% token reduction.
- Correctness, privacy, state-budget, and failed-candidate-corruption gates passed.

## Known Issues

- Windows-only source installer path; no other platform installer is documented in this
  release.
- Install/update require an external ModelCommandJson adapter for worker model updates; status, doctor, and uninstall do not.
- Secret masking covers the defined API-key/Bearer/token/password/private-key
  patterns; unknown formats are not guaranteed to be detected.
- Formal benchmark reruns require live signed-in Codex CLI calls.
