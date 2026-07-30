# Changelog

All notable changes to this project will be documented in this file.

The project follows [Semantic Versioning](https://semver.org/) after its first
public release.

## [Unreleased]

No unreleased changes.

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
