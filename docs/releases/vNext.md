# context-compactor v3.1.0

## Highlights

- Add one Windows command for both first-time installation and later updates.
- Download only the latest public stable GitHub Release from the official
  `ivyliu1201/context-compactor` repository.
- Keep the existing source-only installation, private venv, bundled Codex
  adapter, project-local Hooks, and privacy behavior unchanged.

## Changes

- Added `scripts/bootstrap.ps1`.
- The public command loads the bootstrap from the version-pinned `v3.1.0`
  release tag instead of executing the mutable `main` branch.
- The bootstrap validates the GitHub repository download URL, SemVer release
  tag, and downloaded source version before invoking the installer.
- The bootstrap always invokes the idempotent `install` action, so the same
  command works before and after an installation exists.
- Temporary release archives and extracted source are removed after each run.
- README guidance now tells users to run the command from the target project
  directory and to use `codex.cmd` when PowerShell blocks the npm
  `codex.ps1` wrapper.

## Breaking Changes

None.

## Migration

None. Existing installations can run the new bootstrap command in their
project directory.

## Verification

- The complete Python suite passed 79 tests with 2 environment-dependent skips.
- PowerShell syntax parsing passed for the bootstrap.
- An isolated PowerShell test executed the documented remote-script command
  twice: the first run created the source installation and the second reused
  it without reporting a missing installation.
- A live GitHub `releases/latest` run downloaded public v3.0.0 source and
  completed two consecutive bootstrap runs in a clean isolated project with
  the bundled Codex adapter.
- Both paths verified cleanup of bootstrap temporary files.

## Known Issues

- Windows only.
- Requires PowerShell 5.1+, Git, Python 3.9+, network access to GitHub, and a
  signed-in Codex CLI when using the bundled adapter.
- Organizational policies may still prohibit PowerShell or downloaded source.
