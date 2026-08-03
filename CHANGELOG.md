# Changelog

All notable changes to this project will be documented in this file.

The project follows [Semantic Versioning](https://semver.org/) after its first
public release.

## [Unreleased]

## [3.3.2] - 2026-08-03

### Fixed

- Resolved Windows App Execution Alias launchers through the interpreter's
  reported `sys.executable`, avoiding `WinError 1920` when
  `WindowsApps\python.exe` can run but cannot be strictly resolved.
- Added actionable failures for inaccessible project, install, or temporary
  locations and for GitHub release API or archive download failures.

### Changed

- The Windows bootstrap now probes `py`, the configured Python command, and
  `python3` as applicable; requires Python 3.9+ with `venv` and pip support;
  and validates Git and the selected agent CLI before downloading.
- A missing Codex CLI remains an installation error. An unconfirmed
  `codex login status` now produces a warning and allows installation to
  continue so authentication can be completed later with `codex login`.
- Updated both READMEs to pin the one-command bootstrap to `v3.3.2` and keep
  the complete copyable command on one physical line.

### Breaking Changes

None.

### Verification

- The complete Python suite passed 84 tests with 2 environment-dependent skips.
- All 13 management and PowerShell integration tests passed, including
  WindowsApps alias, prerequisite warning, network, and download failures.
- The isolated fresh-project end-to-end test passed.
- All 3 PowerShell scripts passed syntax parsing.

## [3.3.1] - 2026-08-03

### Fixed

- Reproduced the long-`%TEMP%` extraction failure from public v3.3.0, then
  shortened the bootstrap temporary workspace and extraction directory names to
  keep normal long-TEMP archive extraction within the Windows PowerShell 5.1
  legacy path limit. The bootstrap workspace now uses `cc-<guid>` and its
  extraction directory uses `s`.
- Kept the repository download URL validation, release and source-version
  verification, installer behavior, human-readable output, machine-readable
  installer JSON interface, and cleanup safety checks unchanged.

### Changed

- Updated both READMEs to pin the documented one-command bootstrap entry point
  to the `v3.3.1` tag.

### Breaking Changes

None.

### Verification

- The complete Python suite passed 82 tests with 2 environment-dependent skips.
- The archive-backed PowerShell integration test used an 80-character temporary
  directory and passed `Installed`, `Updated`, and `Already up to date`, with
  the temporary directory empty afterwards.
- The exact v3.3.1 candidate passed `Installed` then `Already up to date` using
  the same long-TEMP length that reproduced the public v3.3.0 failure and an
  archive entry containing 70 characters. It also verified the global
  `USERPROFILE` owner and temporary-directory cleanup.
- Python AST and all PowerShell parser checks passed.

## [3.3.0] - 2026-08-03

### Added

- Added cwd-aware global Codex Hook registration: the first `UserPromptSubmit`
  for a project registers that event working directory without requiring a
  per-project reinstall.
- Added shared project registration protection so concurrent Codex sessions
  use one manifest, state directory, and `events.sqlite` journal without
  duplicate registry entries.

### Changed

- Global Hook execution now resolves the project from every event payload's
  `cwd`. `SessionStart` can read existing project state but does not register a
  new project.
- Project-local Hooks take precedence over inherited global Hooks. Removing a
  global owner removes only its inherited registrations and preserves local
  project data and Hooks.
- The Hook wrapper temporarily provides the managed project manifest to the
  Python Hook and restores the environment afterwards.
- Corrected one-command bootstrap results so they accurately distinguish
  `Installed`, `Updated`, and `Already up to date`, while keeping the
  machine-readable `scripts/install.ps1` JSON interface unchanged.
- Updated both READMEs for the current global Hook architecture and pinned the
  documented one-command entry point to the `v3.3.0` tag.

### Breaking Changes

None.

### Verification

- The complete Python suite passed 82 tests with 2 environment-dependent skips.
- Core global Hook, event-cwd selection, multi-session journal, and bootstrap
  install/update/already-current tests passed.
- Python AST and PowerShell parser checks passed, as did `git diff --check`.
- Isolated Windows-profile validation confirmed global installation and that
  three concurrent `UserPromptSubmit` events for one other cwd create one
  manifest and registry entry while sharing one `events.sqlite` journal.

## [3.2.0] - 2026-08-02

### Changed

- Changed the one-command bootstrap from raw installer JSON to color-coded
  progress, success, result, project path, install location, and next-step
  messages.
- Kept `scripts/install.ps1` machine-readable JSON output unchanged for
  existing automation and direct source-tree management.
- Restyled the English and Traditional Chinese README headers with centered
  navigation and colored release, CI, runtime, and license badges.
- Pinned the documented one-command entry point to the `v3.2.0` release tag.

### Verification

- The complete Python suite passed 79 tests with 2 environment-dependent skips.
- The isolated archive-backed PowerShell test passed first installation and a
  repeated no-op update while confirming that bootstrap output contains no raw
  installer JSON and temporary downloads are removed.
- The unexpected-download-host PowerShell test continued to reject an
  untrusted release URL.
- PowerShell syntax parsing passed for the updated bootstrap.

## [3.1.0] - 2026-08-02

### Added

- Added `scripts/bootstrap.ps1`, a Windows one-command entry point that
  downloads the latest public stable GitHub Release and runs the source
  installer for the current project.
- Added PowerShell coverage that executes the documented remote-script command
  twice, proving first install and repeat update behavior with the same command.

### Changed

- Documented `install` as the idempotent action used by the bootstrap for both
  initial installation and later updates; the strict `update` action still
  requires an existing installation.
- Documented `codex.cmd` as the PowerShell-safe launcher when the npm
  `codex.ps1` wrapper is blocked by local execution policy.
- Pinned the documented bootstrap entry point to the `v3.1.0` release tag;
  the pinned bootstrap continues to select the latest public stable release.

### Verification

- The bootstrap PowerShell parser check passed.
- An isolated archive-backed PowerShell test passed the same documented
  command twice and removed its temporary downloads.
- A live GitHub `releases/latest` run downloaded public v3.0.0 source, installed
  it into a clean isolated project, repeated the same bootstrap successfully,
  selected the bundled Codex adapter, and removed the bootstrap temporary files.

## [3.0.0] - 2026-08-02

### Added

- Added a standard-mode-only model compaction flow with a Python 3.9+ standard-library
  worker process.
- Added repository-readable state files (`.context-compactor/state.yaml`,
  `.context-compactor/state.backup.yaml`) and a lightweight
  `.context-compactor/events.sqlite` journal.
- Added a Windows PowerShell 5.1+ source installer (`scripts/install.ps1`) with
  project actions for `install`, `update`, `status`, `doctor`, and
  `uninstall`.
- Added a bundled Codex CLI adapter that uses saved CLI authentication,
  ephemeral execution, a read-only sandbox, and strict structured output.

### Changed

- Switched runtime, install, and storage behavior from the native executable
  model to a source-only, Python-detached worker model.
- Changed install/update to use the bundled Codex adapter when
  `ModelCommandJson` is omitted; explicit external adapters implementing
  `context-compactor/model/v1` remain supported.
- Changed private installation root defaults to
  `%LOCALAPPDATA%\context-compactor`, which contains the managed virtual
  environment.
- Changed managed Codex `SessionStart` Hooks to match `startup` only. Session
  start remains read-only and never enqueues work or launches the model worker.
- Changed install/update to remove only V2 Hook handlers exactly recorded by a
  recognized V2 manifest while preserving the manifest, legacy database,
  project state, and unrelated user Hooks.

### Removed

- Removed the legacy Go runtime and native executable/release distribution flow.
- Removed the legacy provider wrapper scripts and native CI release path.

### Breaking Changes

- Standard releases are source-only and no longer ship native executable assets.
- Native provider wrappers are removed; the bundled Codex CLI adapter is the
  default, and `ModelCommandJson` is now an optional external override.
- Legacy data migration to v3 behavior is read-only preview then explicit apply, and
  the original `.context-compactor/context.db` is never modified.

### Migration

- Run read-only migration preview before apply for existing
  `.context-compactor/context.db`; do not modify or delete the original DB.
- Re-run install or update to replace only manifest-owned V2 Hook handlers with
  the V3 project Hook definitions.
- `state.yaml` no longer stores raw prompt/transcript/log text.
- `events.sqlite` stores masked prompt text only, limited to 8,000 Unicode chars,
  and clears prompt text immediately on success with additional scrubbing within
  seven days.

### Verification

- Formal combined-standard observed input-token evidence:
  - 30-turn release runs on seeds 17/29/43 (signed-in Codex CLI `gpt-5.6-sol/high`)
    reduced tokens by 68.66%, 68.65%, 68.66%.
  - 60-turn endurance runs on seeds 17/29/43 reduced tokens by 81.97%, 81.97%, 82.15%.
  - Correctness, privacy, state-budget, and failed-candidate-corruption gates passed.
- Windows CI now covers Python 3.9/3.13 syntax and tests plus PowerShell syntax
  checks.
- The final local suite ran 77 tests successfully with 2 environment-dependent
  skips, including bundled-adapter, repeated-SessionStart, management, migration,
  privacy, and fresh-project E2E coverage.

### Known Issues

- Formal benchmark reruns require signed-in Codex CLI calls.
- The default bundled adapter requires a signed-in Codex CLI and access to its
  configured model; an explicit external adapter remains available.
- Secret masking covers the defined API-key/Bearer/token/password/private-key
  patterns; unknown formats are not guaranteed to be detected.

## [2.0.2] - 2026-07-30

### Fixed

- Start detached Windows repository workers with
  `CREATE_BREAKAWAY_FROM_JOB` so Hook process cleanup does not terminate the
  worker or its nested host-model command.
- Constrain Codex memory-model responses with a strict JSON Schema, preventing
  extra top-level fields from invalidating otherwise usable extraction output.

### Migration

- No schema, protocol, configuration, or dependency changes are required.
- Re-run the release installer to replace the executable and refresh managed
  Hook definitions.

### Verification

- Windows runtime and journal test executables passed.
- `go vet ./...` passed.
- A live Codex Hook run completed one memory job and one refresh job on their
  first attempts, produced two operations and two records, published one
  verified capsule, and injected it on the next `SessionStart`.

### Known Issues

- A capsule built from one short prompt can be larger than that prompt because
  source, protocol, and verification framing have fixed cost. This
  representation ratio is not provider billing savings.

## [2.0.1] - 2026-07-30

### Fixed

- Canonicalize event working directories with the same symbolic-link and
  junction resolution used for project roots, preventing valid Windows paths
  from being rejected as outside the project.

### Migration

- No schema, protocol, configuration, or dependency changes are required.
- Re-run the release installer to replace the executable.

### Verification

- GitHub Actions CI passed `go test ./...`, `go vet ./...`, and the Windows
  amd64 executable build.

## [2.0.0] - 2026-07-30

### Added

- Additive Schema v5 prompt-extraction jobs with secret redaction, an
  8,000-Unicode-character limit, seven-day retention, and a 500-job
  per-repository cap.
- Background natural-language memory decisions through the signed-in Codex or
  Claude CLI, with routine and repair models plus strict protocol validation.
- Runtime health reporting for pending, processing, completed, and failed
  memory jobs.

### Changed

- Hook foreground work now queues user prompts and launches the detached
  repository worker without waiting for model extraction.
- Capsule refresh is queued only when accepted operations change the current
  memory revision.
- Production now exposes one `standard` privacy policy. Its
  `context-compactor/v1` wire value remains `balanced` for stored-data
  compatibility.
- Ordinary project instructions can become memory without a special prefix or
  a required phrase. Explanation-only prompts complete as `no_change`.

### Removed

- Removed the directive-only extractor and the obsolete synchronous
  `JournalHandler` extraction path.

### Breaking Changes

- New production runs reject the legacy `strict` and `audit` CLI privacy
  selections.
- Deterministic directive-only capture has been replaced by background host
  model decisions. A signed-in Codex or Claude CLI is now required for new
  natural-language memory extraction.

### Migration

- Existing databases migrate automatically to Schema v5.
- Existing version 1 `strict`, `balanced`, and `audit` stored data remains
  readable.
- Re-run the release installer to replace the executable and refresh managed
  Hook definitions; no manual worker command is required.

### Verification

- `go test ./...`
- `go vet ./...`
- Fresh-project executable Hook tests for automatic publication, next-turn
  injection, `no_change`, prompt redaction, and worker-child recursion guards.

### Known Issues

- GitHub release assets currently provide a Windows amd64 executable. Other
  supported development platforms build from source.
- Background extraction depends on the configured host CLI and model provider
  being available.

## [1.0.4] - 2026-07-29

### Fixed

- Added the PowerShell call operator to managed Codex Hook commands so Windows
  executable paths containing spaces invoke correctly.

## [1.0.3] - 2026-07-29

### Changed

- Refined installer output with friendly status colors
  (Cyan/Green/Yellow/Red).
- Hid raw CLI JSON from general users in installer output.
- Corrected bilingual README install and management wording, including global
  Codex Hook removal and its managed-definition scope.

### Breaking Changes

- None.

### Notes

- No public protocol, runtime, or dependency behavior changes were introduced
  in this release.

## [1.0.2] - 2026-07-28

### Fixed

- On Windows, global Codex Hook installation now avoids hardcoding
  `--project-root`; dynamic mode derives project context from the runtime `cwd`
  in each Codex Hook payload.
- Added the install-only `--dynamic-codex-project-root` flag and enabled it when
  the release installer runs without an explicit `-ProjectRoot`.
- Kept explicit `-ProjectRoot` behavior unchanged as fixed project-local root
  mode.

## [1.0.1] - 2026-07-28

### Fixed

- Codex Hook input now accepts an optional UTF-8 BOM while preserving strict
  single-document JSON decoding.

## [1.0.0] - 2026-07-28

### Added

- Initial bilingual project documentation and Apache-2.0 license.
- `context-compactor/v1` protocol foundation and deterministic validation.
- Repository-local SQLite event journal with checksum-verified migrations,
  idempotent writes, bounded retention, and structured resume checkpoints.
- Deterministic memory reducer with lifecycle transitions, duplicate detection,
  derived advisory or blocking contradictions, and rebuildable materialized
  views.
- Checksum-verified Windows amd64 release installation with managed Codex and
  Claude Hook setup.
