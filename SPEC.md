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
7. Measure token reduction and response quality through deterministic
   per-turn checks plus fixed and risk-triggered model checkpoints in formal
   60-turn and endurance 120-turn evaluations.

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
A benchmark run is one execution for one scenario family, baseline mode, and
fixture seed. Every turn is processed by deterministic program checks.
Foreground-model evaluation occurs only at fixed checkpoints, qualifying
high-risk events, and diagnostic checkpoints added after a failure. The runner
must not call the foreground model on every turn.

### 10.1 Evaluation matrices

The formal matrix contains 60-turn runs with fixed foreground-model checkpoints
at turns 10, 30, 50, and 60.

The endurance matrix contains 120-turn runs with fixed foreground-model
checkpoints at turns 60, 90, and 120. It performs deterministic checks on every
turn, including context size, hard-budget compliance, capsule and journal
state, superseded-requirement inactivity, version and cursor continuity, and
background publication correctness.

Both reportable matrices use fixture seeds `1`, `2`, `3`, `4`, and `5` across
every scenario family and baseline mode. The fast development matrix may use
only seed `1`, but it does not produce reportable median, worst-case, or
release-pass results.

The 120-turn fixture prefix through turn 60 must be byte-for-byte identical to
the corresponding 60-turn fixture for the same scenario and seed. Its turn-60
deterministic and model results may be reused only when the complete benchmark
manifest and rendered-input digest are identical; otherwise turn 60 is
evaluated again.

Cells with the same scenario and seed are paired comparisons and must use the
same initial repository snapshot, foreground model, model settings, tool
definitions, acceptance checks, and token-counter profile. If the model
supports an explicit sampling seed, it uses the fixture seed; otherwise the
report records that seeded sampling is unsupported.

Every report records the benchmark contract and runner versions, fixture and
acceptance-check digests, initial repository fingerprint, provider and model
identity, available model revision, sampling settings, tool-definition digest,
fixture seed, and token-counter identity and mode. Secrets and credentials must
not enter the report.

Compaction is triggered by measured token usage rather than a fixed turn
number. Fixed model checkpoints are observation points and do not themselves
force compaction.

### 10.2 Scenario families

1. Continuous implementation with stable requirements.
2. Requirement reversal that invalidates an earlier decision.
3. Compact or new-session recovery followed by continued implementation.

### 10.3 Baselines

- full transcript replay
- summary-only memory
- context-compactor `strict`
- context-compactor `balanced`

### 10.4 Deterministic per-turn checks

Before any model evaluation, the runner performs deterministic checks on every
turn. Where applicable to the selected mode, these checks cover:

- fixture, session, event, operation, and checkpoint ordering
- the complete rendered foreground-context size and hard-budget status
- verified capsule identity, digest, version, generation, and publication state
- journal state and event, operation, consumer, and retention cursor continuity
- pending foreground-delta continuity and its measured budget position
- critical-constraint creation, replacement, supersession, and active status
- current-focus and active-task derivation
- stale background refresh rejection and compare-and-swap publication behavior
- bounded-recovery entry and required reconciliation state
- absence of retained secrets in memory, rendered context, logs, and reports
- absence of user-turn rejection caused only by soft-budget pressure

The endurance matrix records these results for all 120 turns. A deterministic
failure is a Gate failure even when all model checkpoints pass.

### 10.5 Foreground-model checkpoints

In addition to the fixed checkpoints, the runner adds an event checkpoint after
any of these high-risk events:

- host transcript compaction completes
- a verified capsule is published or the active capsule changes
- the compiler enters bounded recovery
- a critical constraint is added, replaced, updated through a replacement
  operation, or superseded
- the measured foreground representation, including its pending delta,
  approaches or crosses a configured target, trigger, or hard-budget boundary
- current focus or the active task changes
- a background compaction, refresh, or publication job fails

A foreground representation approaches a budget boundary when it reaches at
least 90% of that boundary while remaining below it. One event is emitted per
boundary episode; the trigger is rearmed only after the measured value falls
below 85% of that boundary. Crossing means moving from below the boundary to
meeting or exceeding it.

If multiple event reasons occur for the same rendered state, or a fixed and
event checkpoint coincide, the runner makes one foreground-model call and
records every trigger reason. Its token cost and unique invocation count are
recorded once. The result may be referenced by both the fixed and applicable
event breakdowns without duplicating the underlying call or cost.

Every foreground-model checkpoint verifies at least:

- correct recall of active critical constraints
- correct treatment of a new requirement as replacing its superseded
  predecessor
- correct current focus and active task
- a reasonable tool choice or next action under the scenario acceptance checks
- an `unknown` response when reliable information is unavailable instead of an
  unsupported guess

The foreground model evaluates the rendered context under test.
Scenario-specific deterministic acceptance checks evaluate its response, tool
selection, and proposed state-changing actions. A separate model judge may be
reported as supplementary evidence but must not determine the release Gate.

### 10.6 Failure localization

Failure at a fixed or event checkpoint does not cause the runner to call the
foreground model on every later turn.

The runner retains the original failure and inserts a diagnostic checkpoint
between the most recent passing point and the failing point. It repeatedly
bisects the remaining turn or ordered-event interval until the first failing
adjacent turn or event boundary is identified, or no smaller reproducible
interval exists.

Diagnostic checkpoints use the same fixture, repository state, mode, model,
settings, counter, and acceptance checks. Their results and token costs are
reported separately. A later diagnostic pass does not erase or convert the
original fixed or event checkpoint failure.

### 10.7 Evaluation and aggregation

The report preserves every seed result. Fixed checkpoint, event checkpoint,
and diagnostic checkpoint results are separate categories:

- fixed results are grouped by matrix and fixed turn
- event results are grouped by high-risk event type
- diagnostic results are grouped by the failure they localize

For each scenario, mode, checkpoint category, and applicable metric, the report
shows all five seed values and:

- the median as the third value after sorting the five values
- the worst case as the lowest value for recall, task success, and token
  reduction
- the worst case as the highest value for token cost, context size, error count,
  and secret retention or disclosure

The report shows median and worst-case results separately for every scenario
and mode. The overall worst case is the worst individual scenario-and-seed
cell; values from different scenarios must not be averaged in a way that hides
a failing cell.

Event checkpoint counts may vary by seed. Event metrics are aggregated by event
type after preserving the raw per-seed occurrences and results. A multi-labeled
model call is referenced in every applicable event-type breakdown but remains
one unique call in total invocation and token-cost calculations.

Task success for one run is the percentage of applicable deterministic
scenario acceptance checks that pass. The task-success gap for summary-only,
strict, or balanced mode is the paired full-transcript success rate minus the
candidate rate, expressed as percentage points. The full-transcript gap is
zero by definition.

Quality Gates apply to every fixed and event checkpoint in the reportable
matrices. A passing median must not hide an individual Gate failure.
Diagnostic checkpoints localize failures but do not replace the primary Gate
result.

If no foreground model is configured, its identity cannot be reproduced, or a
required model execution does not complete, model-dependent quality metrics
and task success are `not_evaluated`. Deterministic structural checks may still
be reported, but an incomplete matrix has overall status `not_evaluated` and
must not be described as a release pass.

### 10.8 Token accounting

Each run reports these model-token totals separately:

- foreground input and output tokens
- compaction input and output tokens
- fixed-checkpoint foreground tokens
- event-checkpoint foreground tokens
- diagnostic-checkpoint foreground tokens
- total tokens across all unique model calls

Foreground totals include every foreground agent-model call and the complete
input actually sent to it, including supported framing, retained messages, and
tool results. Compaction totals include every model call used to create or
refresh summaries, capsules, or consolidated memory. Provider retries that
consume model tokens are included.

A deterministic local compiler that invokes no model reports observed
compaction model input and output as zero. Deterministic per-turn program checks
do not add model-token cost. Missing usage observations must be
`not_evaluated`, not zero.

Each token value records a measurement basis of `observed`, `estimated`, or
`not_evaluated`. Provider-reported usage is preferred. When it is unavailable,
the active documented counter may provide an estimate, but observed and
estimated values are reported separately and are not combined into one median.

### 10.9 Acceptance gates

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
51-60.

For each mode and seed, the turns 51-60 stability report includes the raw
per-turn sizes, median, peak, range (`maximum - minimum`), end drift
(`turn 60 - turn 51`), peak ratio
(`maximum(turns 51-60) / max(1, turn 50)`), and per-turn hard-budget status.

The endurance report preserves the raw per-turn sizes and deterministic results
for turns 61-120. It reports their median, peak, range, end drift
(`turn 120 - turn 61`), peak ratio
(`maximum(turns 61-120) / max(1, turn 60)`), and per-turn hard-budget status.
Token reduction at turns 90 and 120 is reported against the paired
full-transcript baseline.

Raw series and hard-budget status must remain visible; a median alone must not
be labeled stable. Version 1 defines the 90- and 120-turn measurements but does
not add a new token-reduction, range, or drift threshold before a reproducible
endurance baseline exists.

A higher 60-turn target, a 90- or 120-turn token-reduction target, or an
additional tail-stability Gate may be set only after that baseline exists.
A deterministic per-turn failure or any fixed or event model-checkpoint failure
makes the run fail. A run that meets token targets but fails a quality Gate is
a failed run.

## 11. Compatibility and versioning

Protocol changes follow semantic compatibility rules:

- additive optional fields are backward-compatible
- changing required fields or operation meaning is breaking
- unknown input fields are rejected in v1

No stable public release or compatibility guarantee exists until the first
versioned release is approved.
