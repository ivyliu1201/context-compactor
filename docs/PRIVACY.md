# Privacy

`context-compactor` is a local-first context compression tool for coding
agents. It processes hook input locally to create bounded structured memory for
later turns.

## Prompt Handling

User-prompt hooks may retain a local extraction job so the detached worker can
make a background memory decision. Before insertion, credential-like spans are
replaced with `[REDACTED]`; the remaining text is limited to 8,000 Unicode
characters. Jobs expire after seven days and each repository is capped at 500
jobs.

The bounded prompt field is not stored in event rows and is never rendered
directly into compiled capsules. A model receives the redacted job and returns
either `no_change` or a typed update; only validated durable facts and bounded
evidence can become memory. Deterministic protocol and privacy validation runs
before an update can become durable.

Production exposes one `standard` privacy policy. Its version 1 wire value is
`balanced` for compatibility. Existing `strict` and `audit` values remain
readable as legacy data but are rejected for new production runs.

## Local Files

Repository-local SQLite state is stored at
`.context-compactor/context.db`. Installation metadata is stored at
`.context-compactor/install.json`.

Managed Codex hook configuration is stored in `.codex/hooks.json`. Managed
Claude hook configuration is stored in `.claude/settings.local.json`.

## Model Providers

Background extraction invokes the same signed-in Codex or Claude host CLI that
delivered the hook. The bounded redacted prompt may therefore be sent to that
CLI's configured model provider according to its own settings and policies.
`context-compactor` does not control provider retention or make claims about
it. Claude API-key environment variables are withheld from child commands by
default unless `CONTEXT_COMPACTOR_USE_ANTHROPIC_API_KEY=1` is explicitly set.

## Removing Local State

Uninstall removes only managed hook definitions. It does not imply that the
local journal or memory state is deleted.

To remove repository-local state, first uninstall the managed hooks and wait
for any detached repository worker to stop, then delete the repository's
`.context-compactor` directory yourself. Doing so permanently removes prompt
jobs and resume memory for that repository.
