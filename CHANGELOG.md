# Changelog

All notable changes to this project will be documented in this file.

The project follows [Semantic Versioning](https://semver.org/) after its first
public release.

## [Unreleased]

### Added

- Initial bilingual project documentation and Apache-2.0 license.
- `context-compactor/v1` protocol foundation and deterministic validation.
- Repository-local SQLite event journal with checksum-verified migrations,
  idempotent writes, bounded retention, and structured resume checkpoints.
- Deterministic memory reducer with lifecycle transitions, duplicate detection,
  derived advisory or blocking contradictions, and rebuildable materialized
  views.

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
