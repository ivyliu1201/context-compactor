# Changelog

All notable changes to this project will be documented in this file.

The project follows [Semantic Versioning](https://semver.org/) after its first
public release.

## [Unreleased]

### Added

- Added a standard-mode-only model compaction flow with a Python 3.9+ standard-library
  worker process.
- Added repository-readable state files (`.context-compactor/state.yaml`,
  `.context-compactor/state.backup.yaml`) and a lightweight
  `.context-compactor/events.sqlite` journal.
- Added a Windows PowerShell 5.1+ source installer (`scripts/install.ps1`) with
  project actions for `install`, `update`, `status`, `doctor`, and
  `uninstall`.

### Changed

- Switched runtime, install, and storage behavior from the native executable
  model to a source-only, Python-detached worker model.
- Changed install/update to run through an external `ModelCommandJson` adapter
  implementing `context-compactor/model/v1`.
- Changed private installation root defaults to
  `%LOCALAPPDATA%\context-compactor`, which contains the managed virtual
  environment.

### Removed

- Removed the legacy Go runtime and native executable/release distribution flow.
- Removed the legacy provider wrapper scripts and native CI release path.

### Breaking Changes

- Standard releases are source-only and no longer ship native executable assets.
- Native provider wrappers are removed; an external model adapter must be supplied.
- Legacy data migration to v3 behavior is read-only preview then explicit apply, and
  the original `.context-compactor/context.db` is never modified.

### Migration

- Run read-only migration preview before apply for existing
  `.context-compactor/context.db`; do not modify or delete the original DB.
- `state.yaml` no longer stores raw prompt/transcript/log text.
- `events.sqlite` stores masked prompt text only, limited to 8,000 Unicode chars,
  and clears prompt text immediately on success with additional scrubbing within
  seven days.

### Verification

- Formal combined-standard evidence:
  - 30-turn release runs on seeds 17/29/43 (signed-in Codex CLI `gpt-5.6-sol/high`)
    reduced tokens by 68.66%, 68.65%, 68.66%.
  - 60-turn endurance runs on seeds 17/29/43 reduced tokens by 81.97%, 81.97%, 82.15%.
  - Correctness, privacy, state-budget, and failed-candidate-corruption gates passed.
- Windows CI now covers Python 3.9/3.13 syntax and tests plus PowerShell syntax
  checks.

### Known Issues

- Formal benchmark reruns require signed-in Codex CLI calls.
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
