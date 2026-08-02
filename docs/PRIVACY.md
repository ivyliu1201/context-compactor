# Privacy

`context-compactor` is a local-first tool in standard mode only.

## Local data files

- `.context-compactor/state.yaml` is the readable derived state.
- `.context-compactor/state.backup.yaml` is its backup.
- `.context-compactor/events.sqlite` is the lightweight journal.

`state.yaml` and `state.backup.yaml` never store raw prompts, transcripts, logs,
tool output, reasoning text, or credentials.

## Redaction and prompt handling

Before a prompt is written to the journal, defined secret spans are replaced with
`[REDACTED]`:

- API-key style environment assignments.
- `Authorization: Bearer ...` and `Authorization: Basic ...` patterns.
- `bearer-token` style assignments.
- `password` and `secret` assignments.
- private-key blocks.

Unknown secret formats are not guaranteed to be detected.

`events.sqlite.prompt_text` stores only redacted text, is limited to 8,000
Unicode characters, and remains local. `prompt_text` is cleared immediately after
successful completion; any still-retained `prompt_text` is scrubbed no later than
seven days. Non-prompt event/job metadata may remain. The journal is capped at
500 job rows per repository.

## Model boundary

By default, the bundled adapter invokes the signed-in Codex CLI with ephemeral,
read-only execution. It sends the bounded `redacted_prompt` and previous derived
state needed for the `context-compactor/model/v1` decision. User configuration
and repository rules are ignored; the model is instructed not to call tools and
runs in an isolated temporary workspace with a read-only sandbox.

Before launch, the bundled adapter removes environment variables whose names
contain the defined API-key, access/auth-token, Bearer, client-secret,
credential, password, or private-key fragments. Saved Codex CLI authentication
remains available so the CLI can use the account the user has already signed in
with.

`ModelCommandJson` may explicitly select an external adapter instead. Whether
the bundled Codex CLI or an external command sends data remotely, the provider’s
own policy applies; this project makes no provider-side retention claim.

## Durable output policy

Persistent model output is accepted only when it is exactly `no_change` or a
complete, validated updated state. Outputs are deterministically validated before
writing and must not copy prompt/transcript/logs/tool output/reasoning or
credentials. A candidate state containing any defined secret pattern is rejected
before publication.

## Managed installation and state

- Host configs are managed in `.codex/hooks.json` and
  `.claude/settings.local.json`.
- Global installation metadata is under the selected install root (default:
  `%LOCALAPPDATA%\context-compactor`), including `install.json` and per-project
  manifests.
- Uninstall removes only managed hooks and manifests, and removes the managed
  private installation only when no projects remain.
- Uninstall does not delete repository `.context-compactor` state.
- To remove repository state, do it after uninstall and worker shutdown; this is a
  manual action and is permanent.

Legacy migration from `.context-compactor/context.db` is read-only and never
modifies or deletes the original database.
