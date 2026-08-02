# context-compactor v3.0.0

## Highlights

- Shift to a single `standard` production mode.
- Adopt a Python 3.9+ standard-library runtime with a detached worker process.
- Use readable repository state files and lightweight journaling:
  `.context-compactor/state.yaml`, `.context-compactor/state.backup.yaml`, and
  `.context-compactor/events.sqlite`.
- Bundle a Codex CLI adapter so install and update need no adapter path by
  default; `ModelCommandJson` remains an optional external override.
- Use a source-only Windows PowerShell 5.1+ installation flow with private venv at
  `%LOCALAPPDATA%\context-compactor`.
- Remove legacy native and Go-dependent release behavior.
- See [README](../../README.md), [PRIVACY](../PRIVACY.md), and
  [benchmark report](../benchmark-report-v3-2026-07-31.zh-TW.md).

## Changes

- Added `state.backup.yaml` plus a masked, bounded `events.sqlite` journal.
- Added installer actions for `install`, `update`, `status`, `doctor`, and
  `uninstall`.
- Added managed Codex `SessionStart` matching for `startup` only. Session start
  is read-only and never creates a journal job or launches the model worker.
- Added exact cleanup of V2 Hook handlers recorded by a recognized V2 manifest;
  unrelated user Hooks, the V2 manifest, project data, and `context.db` remain.
- Added read-only migration support for `.context-compactor/context.db` with
  explicit apply.
- Added formal benchmark evidence collection for 30-turn release and 60-turn
  endurance runs.

## Breaking Changes

- Native executable artifacts are removed from releases; source-only distribution is
  the distribution model.
- Native provider wrappers are removed. The bundled Codex CLI adapter is the
  default, while `ModelCommandJson` explicitly selects an external adapter.
- The legacy `context.db` path is now handled through read-only migration preview
  plus explicit apply.

## Migration

1. From a cloned or extracted v3.0.0 source tree, install for Codex:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action install -ProjectRoot . -AgentHost codex
```

   This replaces only V2 Hook handlers exactly owned by a recognized V2
   manifest. It does not delete that manifest or the old database.

2. Preview legacy database migration before applying it:

```powershell
python -m context_compactor migrate preview --project-root .
```

3. Apply migration after reviewing the preview:

```powershell
python -m context_compactor migrate apply --project-root .
```

The preview is read-only. Apply creates the new standard state without modifying
or deleting the original `.context-compactor/context.db`.

## Verification

- 77 tests ran successfully, including 2 environment-dependent skips, in the
  combined standard verification set.
- Fresh-project and privacy E2E tests included.
- Migration tests and management action tests included.
- Python AST syntax and PowerShell syntax checks included.
- Live synthetic requests through the signed-in Codex CLI passed both
  `no_change` and complete `updated` output validation with
  `gpt-5.6-sol/high`.
- The fresh-project foreground Hook Gate passed with a 5.5-second cold-start
  limit and a 5.0-second limit for subsequent invocations; background model
  completion is measured separately.
- 30-turn benchmark (seeds 17/29/43, signed-in Codex CLI `gpt-5.6-sol/high`):
  68.66%, 68.65%, 68.66% token reduction.
- 60-turn benchmark (same seeds and model): turn-60 reductions were 81.97%,
  81.97%, and 82.15%; cumulative reductions across checkpoints 45 and 60 were
  79.83%–79.91%.
- Benchmark percentages are observed input-token reductions for the fixed
  deterministic scenario, not guarantees of per-turn or billed-cost savings.
- Correctness, privacy, state-budget, and failed-candidate-corruption gates passed.

## Known Issues

- Windows-only source installer path; no other platform installer is documented in this
  release.
- The default adapter requires a signed-in Codex CLI and access to
  `gpt-5.6-sol`; `ModelCommandJson` remains available for an external adapter.
- Foreground Hook cold-start latency depends on the local PowerShell, Python,
  filesystem, and security-scan environment.
- Secret masking covers the defined API-key/Bearer/token/password/private-key
  patterns; unknown formats are not guaranteed to be detected.
- Formal benchmark reruns require live signed-in Codex CLI calls.
