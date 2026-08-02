# context-compactor v3.2.0

## Highlights

- Replace raw one-command installer JSON with concise, color-coded progress and
  next-step messages.
- Preserve the existing idempotent installation path and the machine-readable
  `scripts/install.ps1` interface used by automation.
- Refresh both README headers with centered navigation and colored project
  badges.

## Changes

- The bootstrap now displays four cyan progress steps, a green success/result
  summary, project and install paths, and a yellow Hook trust reminder.
- The bootstrap captures and validates the existing installer report instead
  of printing it directly.
- Result messages distinguish a first installation, an updated source, and an
  already-current installation.
- The documented one-command entry point is pinned to the `v3.2.0` release
  tag.
- English and Traditional Chinese README content remains aligned.

## Breaking Changes

None.

## Migration

None. Existing installations can run the same one-command bootstrap from their
project directory. Automation that requires JSON should continue to invoke
`scripts/install.ps1` directly.

## Verification

- The complete Python suite passed 79 tests with 2 environment-dependent skips.
- The isolated archive-backed PowerShell test passed a first installation and
  a repeated no-op update using the same documented command.
- The test verified all four progress steps, success/result/next-step messages,
  the v3.2.0 install manifest, absence of raw installer JSON, and cleanup of
  bootstrap temporary files.
- The unexpected-download-host test continued to reject an untrusted release
  URL.
- PowerShell syntax parsing passed for the updated bootstrap.

## Known Issues

- Windows only.
- Requires PowerShell 5.1+, Git, Python 3.9+, network access to GitHub, and a
  signed-in Codex CLI when using the bundled adapter.
- Organizational policies may still prohibit PowerShell or downloaded source.
- This release changes installation presentation and documentation only; it
  does not change the compression runtime or rerun the existing token benchmark.
