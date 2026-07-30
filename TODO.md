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
- [x] Use the last verified capsule plus newer validated operations while a
      refresh is pending.
- [x] Guarantee configured context budget limits with a host/model token
      counter or a documented conservative estimator.

## 4. Agent adapters

- [x] Define the thin adapter interface and negotiate one owner for host
      transcript compaction.
- [x] Add Codex CLI hook support.
- [x] Add Claude Code hook support.
- [x] Detect natural-language resume intent and provide an explicit resume
      command fallback.
- [x] Emit only the four-field Resume Preview and require confirmation before
      state-changing actions.
- [x] Refresh context capsules after a turn or during idle time without delaying
      the foreground response or allowing stale jobs to overwrite newer state.
- [x] Add an executable hook runtime that connects Codex and Claude hook input
      to privacy filtering, validated journal operations, foreground context,
      and durable refresh enqueue.
- [x] Close the executable refresh lifecycle with additive Schema v4 job state,
      a detached per-repository drain worker, single-flight lease, retry and
      health diagnostics, empty-output suppression, capsule publication, and
      next-hook injection.
- [x] Accept ordinary natural-language prompts without a required prefix,
      retain only a bounded redacted extraction job, and use a background model
      to return `no_change` or a protocol-valid memory update.
- [x] Keep production on one standard privacy policy while preserving version 1
      wire and stored-data compatibility.
- [x] Process extraction jobs through the detached repository worker and enqueue
      capsule refresh only when accepted memory operations change the current
      memory revision.
- [x] Remove the directive-only extractor after the background path is verified
      and use plain-English names in the active memory flow.
- [x] Verify install-to-injection behavior from a fresh project without
      manually invoking the refresh worker.
- [x] Add install, uninstall, status, and doctor commands.

## 5. Benchmark suite

- [x] Implement reproducible 60-turn synthetic fixtures with checkpoints at
      turns 10, 30, 50, and 60.
- [x] Add continuous-development, requirement-reversal, and resume scenarios.
- [x] Verify Resume Preview fields and that no state-changing action occurs
      before confirmation.
- [x] Verify soft-budget overflow never rejects a user turn and recovery context
      is reconciled before state-changing actions.
- [x] Compare full transcript, summary-only, strict, and balanced modes.
- [x] Produce the five-seed formal 60-turn and endurance 120-turn benchmark
      report. Run deterministic checks on every turn; evaluate the foreground
      model at turns 10, 30, 50, and 60 in the formal matrix and turns 60, 90,
      and 120 in the endurance matrix; add event checkpoints for host
      compaction, capsule publication or switching, bounded recovery, critical
      constraint changes, foreground budget-boundary pressure, current-focus
      changes, and background refresh or publication failures. Deduplicate
      overlapping triggers, localize failures with separate diagnostic
      checkpoints, preserve every seed result, and report fixed, event, and
      diagnostic results separately. Report per-scenario and per-mode median
      and worst-case values, foreground and compaction input/output token costs
      by measurement basis, turns 51-60 and 61-120 context-size stability, and
      all quality Gates. Model-dependent metrics remain `not_evaluated` unless
      the configured foreground model completes both reproducible matrices; an
      incomplete report cannot claim release pass.

## 6. Open-source distribution

- [x] Confirm the final GitHub module path before the first public release.
- [x] Add supported-platform CI after build commands are stable.
- [x] Add security, privacy, and contribution documentation.
- [x] Publish the `v1.0.0` release only after MVP verification passes.
