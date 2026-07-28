# Privacy

`context-compactor` is a local-first context compression tool for coding
agents. It processes hook input locally to create bounded structured memory for
later turns.

## Prompt Handling

Complete prompts can be processed transiently inside a hook process. The
default durable protocol does not have a full-prompt field. Durable records are
event metadata and validated structured memory operations.

The built-in deterministic extractor persists only explicit
`[context-compactor]` directives. Ordinary prompt text remains transient.

Privacy modes control durable evidence:

- `strict` stores structured facts without evidence text.
- `balanced` can store bounded, redacted evidence spans.
- `audit` retains content only through explicit opt-in and a configured
  retention policy.

## Local Files

Repository-local SQLite state is stored at
`.context-compactor/context.db`. Installation metadata is stored at
`.context-compactor/install.json`.

Managed Codex hook configuration is stored in `.codex/hooks.json`. Managed
Claude hook configuration is stored in `.claude/settings.local.json`.

## Model Providers

The agent host or model provider may receive prompts according to its own
configuration and policies. `context-compactor` does not control provider
retention or make claims about it.

## Removing Local State

Uninstall removes only managed hook definitions. It does not imply that the
local journal or memory state is deleted.

To remove repository-local state, first uninstall the managed hooks or stop
related processes, then delete the repository's `.context-compactor` directory
yourself. Doing so permanently removes resume memory for that repository.
