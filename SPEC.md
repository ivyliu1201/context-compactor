# context-compactor Specification

## 1. Product definition

`context-compactor` is a local-first context compression layer for coding
agents. It converts transient agent events into structured, traceable memory and
compiles a bounded working context for future turns.

The memory view is not authoritative. The user's latest explicit instructions,
repository files, version control state, and executed verification results take
precedence over generated memory.

## 2. MVP goals

1. Normalize agent events behind a versioned protocol.
2. Persist a bounded event journal without complete prompts by default.
3. Apply validated incremental memory operations through a deterministic
   reducer.
4. Detect contradictions, superseded decisions, and stale working state.
5. Compile mandatory and relevant memory within a configured token budget.
6. Support Codex CLI and Claude Code through thin adapters.
7. Measure token reduction and resume quality at 10, 30, and 50 turns.

## 3. Non-goals for the first release

- Cloud synchronization or a hosted dashboard.
- A mandatory vector database or embedding provider.
- Treating generated memory as a replacement for repository inspection.
- Silently retaining complete prompts in the default privacy mode.
- Supporting every agent framework before the core contract is stable.

## 4. Architecture

```text
agent hook
  -> normalized transient event
  -> privacy filter and bounded event journal
  -> candidate memory operations
  -> deterministic validation and reducer
  -> materialized memory view
  -> relevance and token-budget compiler
  -> agent adapter injection
```

SQLite will become the event and memory source of record. Human-readable YAML
or Markdown output will be generated views that can be rebuilt from validated
records.

The local journal uses the driver and Schema v1 rules recorded in
`docs/adr/0001-sqlite-event-journal.md`. In the default schema, extraction runs
while transient content is available and only event metadata plus validated
memory operations are durable. This avoids retaining complete prompts merely
to support later background extraction.

## 5. Privacy modes

| Mode | Durable content | Default |
|---|---|---|
| `strict` | Structured facts and source event identifiers; no evidence text | No |
| `balanced` | Structured facts and bounded, redacted evidence spans | Yes |
| `audit` | Explicit opt-in content retention with a configured retention policy | No |

Complete prompt content may enter a transient normalization pipeline, but the
default durable protocol contains no full-prompt field. Unknown JSON fields are
rejected so callers cannot silently add one.

## 6. Protocol contract

The initial protocol identifier is `context-compactor/v1`.

### 6.1 Transient event

A normalized event includes:

- protocol version
- stable event and session identifiers
- event kind and UTC timestamp
- current working directory
- transient content
- bounded string metadata

Transient content is input to extraction and redaction. It is not automatically
eligible for persistence.

### 6.2 Memory mutation batch

A candidate extractor returns operations, not a replacement state document.
Supported initial operations are:

- `add`: introduce a new typed memory record
- `supersede`: replace an active record through a later operation
- `resolve`: mark a blocker, question, or task as completed
- `expire`: invalidate time-bound or stale state

Every operation refers to its source event. Added records have stable IDs,
typed kind, priority, confidence, lifecycle status, value, an optional
`conflict_key`, and optional bounded evidence or artifact references. A
conflict key names one semantic slot whose active values are expected to agree.
Critical records must provide one; non-critical naturally multi-valued records
may omit it.

Critical records cannot be based only on inferred confidence. They require an
explicit user statement or repository verification.

### 6.3 Materialized memory view

The deterministic reducer applies durable operations in journal order and
creates a rebuildable view. Materialized lifecycle states are `active`,
`superseded`, `resolved`, `expired`, and `duplicate`. Invalid targets or
lifecycle transitions fail the complete rebuild instead of partially updating
the view.

Record priority and conflict impact are separate dimensions. Priority remains
`critical`, `high`, `normal`, or `low`. When active records with the same
conflict key disagree, the reducer derives `blocking` impact if any involved
record is critical; otherwise it derives `advisory`. Extractors cannot set or
downgrade impact.

The reducer stores a deterministic digest and updates its consumer cursor in
the same transaction as the materialized records and contradictions. The
immutable journal remains the rebuild source.

## 7. Context compilation policy

The compiler will reserve budget in this order:

1. active goal and acceptance criteria
2. critical constraints and unresolved contradictions
3. current focus, blockers, and next action
4. relevant decisions and verification results
5. lower-priority related memory

Retrieval may improve lower-priority selection, but goals and critical
constraints must not depend on search recall.

## 8. Conflict policy

- New explicit user instructions supersede older conversational memory.
- Repository evidence supersedes inferred memory.
- Conflicting active critical records stop automatic consolidation and surface
  a contradiction for review.
- Advisory contradictions remain visible but do not by themselves block later
  automation.
- Expired and superseded records remain auditable but are not injected as active
  instructions.

## 9. Resume preview gate

On a new session, the adapter may load project memory through a session-start
hook. When the user asks to resume or continue the project, or invokes an
explicit resume command, the agent must perform only read-only repository
reconciliation and return this concise preview:

```text
目前進度：
最後驗證：
建議下一步：
是否依照以上步驟繼續？
```

The preview must not add unrelated sections. Repository or checkpoint
discrepancies are summarized within `目前進度` or `建議下一步`. When no verified
test record exists, `最後驗證` must state that no verifiable record was found.

Until the user explicitly confirms, the agent must not edit files, install
dependencies, change configuration, commit, or perform other state-changing
actions. The user may replace the suggested next step before confirming. A
manual resume command remains available when an agent host does not expose the
required hook event.

## 10. Benchmark contract

One turn is one user input followed by one agent response and its tool activity.
Each implementation milestone is evaluated at 10, 30, and 50 turns against the
same scenario, repository state, model, and acceptance checks.

### 10.1 Scenario families

1. Continuous implementation with stable requirements.
2. Requirement reversal that invalidates an earlier decision.
3. Compact or new-session recovery followed by continued implementation.

### 10.2 Baselines

- full transcript replay
- summary-only memory
- context-compactor `strict`
- context-compactor `balanced`

### 10.3 Acceptance gates

| Metric | Required result |
|---|---:|
| Critical requirement recall | 100% |
| Explicit negative-constraint recall | 100% |
| Stale decision treated as active | 0 |
| Unreported critical contradiction | 0 |
| Correct next action | 100% |
| Secret retained in memory or reports | 0 |
| Context budget violations | 0 |
| Task success gap from full transcript | no more than 3 percentage points |

Token reduction targets are at least 30% at 10 turns, 60% at 30 turns, and 75%
at 50 turns. A run that meets token targets but fails a quality gate is a failed
run.

## 11. Compatibility and versioning

Protocol changes follow semantic compatibility rules:

- additive optional fields are backward-compatible
- changing required fields or operation meaning is breaking
- unknown input fields are rejected in v1

No stable public release or compatibility guarantee exists until the first
versioned release is approved.
