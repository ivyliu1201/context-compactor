# TODO

Each item must be implemented, verified, reviewed, and committed independently.

## 0. Protocol foundation

- [x] Define `context-compactor/v1` transient event and mutation types.
- [x] Add strict JSON decoding and deterministic validation.
- [x] Enforce privacy-mode evidence limits and critical-memory confidence rules.
- [x] Verify with focused unit tests, `go test ./...`, and `go vet ./...`.

## 1. Local event journal

- [x] Select and document the SQLite driver before adding the dependency.
- [x] Add schema migrations, WAL mode, idempotent event IDs, and retention.
- [x] Ensure default persistence excludes complete prompts.
- [x] Persist structured resume checkpoints: current progress, last verification,
      suggested next action, and repository fingerprint.
- [x] Test concurrency, interrupted writes, retention, and secret rejection.

## 2. Deterministic memory reducer

- [x] Apply validated add, supersede, resolve, and expire operations.
- [x] Detect duplicate records and active critical contradictions.
- [x] Build a rebuildable materialized memory view.

## 3. Context compiler

- [x] Define host-aware budget invariants, deterministic capsule publication,
      active-task precedence, and ID-based recovery reconciliation.
- [x] Reserve budget for goals, constraints, blockers, and next actions.
- [x] Compact oversized mandatory context into a bounded, source-linked
      recovery capsule without blocking conversation.
- [x] Add deterministic recency, priority, and lexical relevance scoring.
- [ ] Use the last verified capsule plus newer validated operations while a
      refresh is pending.
- [ ] Guarantee configured context budget limits with a host/model token
      counter or a documented conservative estimator.

## 4. Agent adapters

- [ ] Define the thin adapter interface and negotiate one owner for host
      transcript compaction.
- [ ] Add Codex CLI hook support.
- [ ] Add Claude Code hook support.
- [ ] Detect natural-language resume intent and provide an explicit resume
      command fallback.
- [ ] Emit only the four-field Resume Preview and require confirmation before
      state-changing actions.
- [ ] Refresh context capsules after a turn or during idle time without delaying
      the foreground response or allowing stale jobs to overwrite newer state.
- [ ] Add install, uninstall, status, and doctor commands.

## 5. Benchmark suite

- [ ] Implement reproducible 60-turn synthetic fixtures with checkpoints at
      turns 10, 30, 50, and 60.
- [ ] Add continuous-development, requirement-reversal, and resume scenarios.
- [ ] Verify Resume Preview fields and that no state-changing action occurs
      before confirmation.
- [ ] Verify soft-budget overflow never rejects a user turn and recovery context
      is reconciled before state-changing actions.
- [ ] Compare full transcript, summary-only, strict, and balanced modes.
- [ ] Report quality gates, median and worst-case results, foreground and
      compaction token costs, and turns 51-60 context-size stability.

## 6. Open-source distribution

- [ ] Confirm the final GitHub module path before the first public release.
- [ ] Add supported-platform CI after build commands are stable.
- [ ] Add security, privacy, and contribution documentation.
- [ ] Prepare a draft `v0.1.0` release only after MVP verification passes.
