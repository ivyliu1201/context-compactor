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
5. Compile mandatory and relevant memory within a configured token budget
   without blocking continued conversation when a soft compaction threshold is
   exceeded.
6. Support Codex CLI and Claude Code through thin adapters.
7. Measure token reduction and resume quality at 10, 30, 50, and 60 turns.

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

Context compilation has two separate responsibilities. Host transcript
compaction manages conversational messages and large tool results. The
context-compactor capsule manages structured, source-linked project memory.
An adapter must negotiate host capabilities and assign exactly one owner for
transcript compaction; it must not blindly apply local transcript compaction on
top of a host-native mechanism. Host compaction output is transient context,
not an authoritative replacement for the structured memory view.

Context compilation uses three distinct limits:

- the `trigger budget` is a soft watermark that schedules compaction before the
  active context becomes expensive
- the `target budget` is the desired size of a refreshed context capsule
- the `hard budget` is the maximum compiled input size, configured below the
  host model limit so tool results and model output still have room

Every configuration must satisfy `0 < target < trigger < hard`. The hard budget
available to the capsule is the host input limit minus system instructions,
tool definitions, retained recent messages, expected tool and model output,
and a configured safety margin. Token measurements identify the host and model
counter used. A conservative estimator may enforce a safe upper bound, but it
must not be reported as an exact host-token guarantee.

Crossing the trigger or target budget must not reject a user turn. The compiler
will reserve the hard budget in this order:

1. active goal and acceptance criteria
2. critical constraints and unresolved contradictions
3. current focus, blockers, and next action
4. relevant decisions and verification results
5. lower-priority related memory

Retrieval may improve lower-priority selection, but goals and critical
constraints must not depend on search recall.

`current focus` is derived from the highest-priority applicable active task,
then newest operation sequence and stable record ID for deterministic ties,
after reconciliation with the repository and the user's latest explicit
instruction. `next action` uses that active task first. A resume checkpoint's
`suggested next action` is only a fallback hint and never overrides active
memory or current repository evidence. If no candidate remains reliable, the
compiler reports the value as unknown instead of inferring one.

### 7.1 Continuous compaction

The adapter schedules capsule refresh after a turn or during idle time instead
of making the foreground response wait for compaction. A refresh applies this
order:

1. exclude expired, superseded, resolved, and duplicate materialized records
2. remove optional evidence text while retaining source event and artifact
   references
3. consolidate older working state into bounded, source-linked derived memory
4. reduce lower-priority retrieval results before reducing mandatory context
5. atomically publish a new capsule with its source cursors and digest

If a refresh has not completed before the next turn, the foreground compiler
uses the last verified capsule plus validated operations after that capsule's
operation cursor. Each capsule records its last event sequence, last operation
sequence, materialized-view digest, compiler-policy version, token-counter
identity, content digest, and creation time.

The verified capsule is a budget-selected derived subset, not a complete
materialized memory view. The foreground path must keep the verified capsule
and its newer operation delta separate and must not seed the reducer with
capsule records or claim that those records reconstruct the complete view.
Pending operations retain their validated journal order and operation
semantics. When a complete current view is required, it must be loaded or
rebuilt from the immutable journal through the pending operation cursor.

Before emitting foreground context, the compiler revalidates the capsule
content digest and the continuity of every newer operation. It measures the
complete supported host output, including framing, capsule records, pending
operations, and separators, with the active counter profile. A capsule's stored
token-counter identity is provenance and does not authorize reuse of an older
size measurement; a different active counter requires deterministic
remeasurement.

Only one refresh may publish for a repository scope at a time. A refresh reads
a fixed source snapshot and publishes with a compare-and-swap check against the
currently verified capsule. A stale job may be discarded or retried but must
not overwrite a capsule compiled from newer operations. Consumer or retention
cursors advance only with successful atomic publication. A crash before
publication leaves the previous verified capsule usable.

The in-process adapter coordinator schedules the builder asynchronously and
returns before the builder completes. It assigns a monotonically increasing
generation per repository scope and publishes only when the completed capsule
still matches both the fixed source snapshot and the latest scheduled
generation. Foreground context reads the current verified capsule plus newer
validated operations without waiting for the refresh result.

Capsule generation is derived work: the MVP compiler must produce it
deterministically from validated records and must never replace repository
inspection. Nondeterministic model-generated consolidation is outside this
contract until its output, source links, model and prompt-policy versions, and
validation can be durably reproduced.

Complete prompts remain transient. Any extraction needed for later compaction
must occur while transient content is available; background work consumes
validated structured records and bounded privacy-mode evidence, not a newly
persisted prompt transcript.

### 7.2 Overflow behavior

Exceeding a trigger or target budget is normal control flow, not a user-visible
failure. If full mandatory records would exceed the hard budget, the compiler
returns a bounded recovery capsule containing the current goal, next action,
compact critical-memory descriptors, source references, and an explicit
retrieval requirement. Each critical descriptor includes a stable record ID,
kind, priority, conflict key when present, and source reference. Both sides of
an active blocking contradiction remain separately addressable and must never
be merged into one apparently resolved statement. General conversation may
continue.

The combined verified capsule and pending operation delta is subject to the
same hard budget. If that foreground representation would exceed the hard
budget, the compiler must not silently truncate the operation delta or apply it
to the partial capsule as though the capsule were a complete view. It loads or
rebuilds the journal-backed materialized view through the pending operation
cursor and runs the normal budget compiler against that view, producing the
same bounded recovery form described above.

Foreground recovery model input remains bounded by the active hard limit.
Required record and operation identifiers remain control-plane lookup data and
must not be rendered into model context. Failure to read or rebuild the
journal-backed source of record is an operational failure and must not be
reported as budget exhaustion. Budget pressure alone must not ask the user to
end, restart, or shorten the conversation.

Before a state-changing action, the adapter must deterministically retrieve and
reconcile every critical record referenced by that recovery capsule through
direct ID lookup. This lookup is required source resolution, not relevance
search; lexical or semantic retrieval may only add optional context. The
adapter must not perform the action while required critical context is
unavailable or an active blocking contradiction remains. A physical host-model
input limit may still prevent a model call, but ordinary configured-budget
pressure must be handled without asking the user to end or restart the
conversation.

### 7.3 Executable hook runtime

Before adapter management commands may install host hooks, a distributable
local executable must provide the runtime bridge between Codex or Claude hook
processes and the pipeline defined in Section 4. For each invocation, the
runtime must:

1. read exactly one host hook payload from standard input and decode it through
   the matching thin adapter
2. run privacy filtering and extraction while transient content is available,
   producing only validated protocol operations for durable storage
3. idempotently append durable event metadata and operations, then load or
   rebuild the repository-scoped memory view
4. supply the last verified capsule plus newer validated operations to the
   foreground path and emit only output supported by that host event

The runtime must preserve the transcript-compaction owner negotiated by the
adapter and must not read transcript contents merely because a hook supplies a
transcript path. Complete prompts remain transient under the selected privacy
mode. Standard output is reserved for the host protocol; diagnostics go to
standard error and must not expose prompts, transcript paths, secrets, or
generated capsule contents.

The lifecycle of background refresh work must satisfy Section 7.1. A
short-lived hook process must durably enqueue the refresh before returning or
hand it to a running local worker. It must not start an in-memory background
goroutine and exit while reporting that the work was scheduled. Retries use
stable event identity and journal idempotency, and invalid input or unavailable
required state fails before partial durable mutation or malformed host output.

Installation is not considered successful unless the executable is resolvable,
the selected host runtime path is healthy, and `doctor` can verify the installed
hook definition. Until those conditions are met, installation must fail closed
without claiming that context compression is active.

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
actions. The user may replace the suggested next step before confirming. An
exact manual command `/context-resume` remains available in any session when an
agent host does not expose the required hook event. Natural-language resume
intent is recognized only on the first prompt after a new-session boundary;
ambiguous or host-specific commands such as `/resume` do not trigger this flow.
The common adapter gate consumes the latest checkpoint and a host-collected,
read-only repository snapshot. It grants state-change permission only after the
current four-field preview has been shown and explicitly confirmed. Replacing
the suggested next action invalidates the prior preview until the updated
version is shown and confirmed.

## 10. Benchmark contract

One turn is one user input followed by one agent response and its tool activity.
Each implementation milestone is evaluated with a reproducible 60-turn run and
checkpoints at turns 10, 30, 50, and 60 against the same scenario, repository
state, model, and acceptance checks. Compaction is triggered by measured token
usage rather than a fixed turn number, and its model input and output count
toward the run's token total.

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
| User turns rejected because a soft budget was exceeded | 0 |
| State-changing action with unreconciled recovery context | 0 |
| Task success gap from full transcript | no more than 3 percentage points |

Token reduction targets are at least 30% at 10 turns, 60% at 30 turns, and 75%
at both 50 and 60 turns. The report must show the reduction trend between the
50- and 60-turn checkpoints and the per-turn compiled input size for turns
51-60. A higher 60-turn release target may be set only after a reproducible
baseline exists. A run that meets token targets but fails a quality gate is a
failed run.

## 11. Compatibility and versioning

Protocol changes follow semantic compatibility rules:

- additive optional fields are backward-compatible
- changing required fields or operation meaning is breaking
- unknown input fields are rejected in v1

No stable public release or compatibility guarantee exists until the first
versioned release is approved.
