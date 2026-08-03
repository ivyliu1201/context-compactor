# context-compactor v3.3.0

## Highlights

- Add cwd-aware global Codex Hook registration so one global installation can
  configure projects as they first submit prompts.
- Share one project manifest, state directory, and journal across concurrent
  Codex sessions without duplicate registration.
- Keep the one-command installer friendly and color-coded while accurately
  reporting installation, update, and already-current results.

## Changes

- Each global Hook event resolves its project from the payload `cwd`.
  `UserPromptSubmit` registers a project when necessary, while `SessionStart`
  only reads existing state.
- A registry lock ensures concurrent sessions register a project once and
  reuse its `events.sqlite` journal.
- A project-local Hook takes precedence over an inherited global Hook.
  Removing a global owner cleans only its inherited registrations and leaves
  local project data and Hooks intact.
- The PowerShell Hook wrapper temporarily provides the managed project manifest
  to the Python Hook, then restores the environment.
- Bootstrap result messages now correctly distinguish `Installed`, `Updated`,
  and `Already up to date`; `scripts/install.ps1` keeps its machine-readable
  JSON interface for automation.
- The documented one-command entry point is pinned to the `v3.3.0` release
  tag, and both READMEs describe only the current architecture.

## Breaking Changes

None.

## Migration

No data migration is required. Run the same one-command bootstrap from any
directory; it explicitly installs or updates the global Codex Hook in the
current Windows user's home directory. Later projects are registered
automatically on their first `UserPromptSubmit` according to the event `cwd`.
Automation that requires JSON should continue to invoke `scripts/install.ps1`
directly. Intentionally running the source installer with a project root
creates a project-local Hook, which takes precedence over an inherited global
Hook.

## Verification

- The complete Python suite passed 82 tests with 2 environment-dependent skips.
- Core tests passed for global Hook cwd selection, one project journal shared
  by multiple sessions, and global registration without duplicate entries.
- The archive-backed PowerShell bootstrap integration passed installation,
  update, and already-current results using the same bootstrap entry point.
- Python AST, PowerShell parser, and `git diff --check` validation passed.
- Two isolated fake Windows profiles verified global installation. For another
  cwd, three concurrent `UserPromptSubmit` calls created one manifest and one
  registry entry; all completed and shared one `events.sqlite` journal.

## Known Issues

- Windows only.
- Requires PowerShell 5.1+, Git, Python 3.9+, network access to GitHub, and a
  signed-in Codex CLI when using the bundled adapter.
- Organizational policies may still prohibit PowerShell or downloaded source.
- v3.3.0 does not rerun the token benchmark because its runtime compression
  logic is unchanged.
