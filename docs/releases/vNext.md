# context-compactor v3.3.1

## Highlights

- Fixes the reproduced public v3.3.0 long-`%TEMP%` extraction failure on
  Windows PowerShell 5.1.
- Keeps the documented one-command install or update workflow unchanged.

## Changes

- Shortened the bootstrap temporary workspace name from
  `context-compactor-bootstrap-<full guid>` to `cc-<full guid>`.
- Shortened the bootstrap extraction subdirectory from `source` to `s`.
- The resulting longest extraction path is reduced from 263 characters to
  approximately 233 characters.
- Repository download URL validation, release-tag and source-version checks,
  installer behavior, user-facing output, installer JSON interface, and cleanup
  safety checks are unchanged.
- The documented one-command entry point is pinned to the `v3.3.1` release
  tag.

## Breaking Changes

None.

## Migration

No data migration is required. Existing v3.3.0 installations can use the same
one-command bootstrap command to update to v3.3.1.

## Verification

- The complete Python suite passed 82 tests with 2 environment-dependent skips.
- The archive-backed PowerShell integration test used an 80-character temporary
  directory and passed `Installed`, `Updated`, and `Already up to date`, with
  the temporary directory empty afterwards.
- The exact v3.3.1 candidate passed `Installed` then `Already up to date` using
  the same long-TEMP length that reproduced the public v3.3.0 failure and an
  archive entry containing 70 characters. It also verified the global
  `USERPROFILE` owner and temporary-directory cleanup.
- Python AST and all PowerShell parser checks passed.
- `git diff --check` passed for the implementation change.

## Known Issues

- Windows only.
- Requires PowerShell 5.1+, Git, Python 3.9+, network access to GitHub, and a
  signed-in Codex CLI when using the bundled adapter.
- Organizational policies may still prohibit PowerShell or downloaded source.
- An exceptionally deep custom `%TEMP%` path may still exceed the Windows
  PowerShell 5.1 legacy path limit; that extreme configuration is outside this
  patch's scope.
- This patch does not rerun the token benchmark because runtime compression is
  unchanged.
