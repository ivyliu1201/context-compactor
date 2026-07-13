# TODO

Each item must be implemented, verified, reviewed, and committed independently.

## 0. Protocol foundation

- [x] Define `context-compactor/v1` transient event and mutation types.
- [x] Add strict JSON decoding and deterministic validation.
- [x] Enforce privacy-mode evidence limits and critical-memory confidence rules.
- [x] Verify with focused unit tests, `go test ./...`, and `go vet ./...`.

## 1. Local event journal

- [ ] Select and document the SQLite driver before adding the dependency.
- [ ] Add schema migrations, WAL mode, idempotent event IDs, and retention.
- [ ] Ensure default persistence excludes complete prompts.
- [ ] Test concurrency, interrupted writes, retention, and secret rejection.

## 2. Deterministic memory reducer

- [ ] Apply validated add, supersede, resolve, and expire operations.
- [ ] Detect duplicate records and active critical contradictions.
- [ ] Build a rebuildable materialized memory view.

## 3. Context compiler

- [ ] Reserve budget for goals, constraints, blockers, and next actions.
- [ ] Add deterministic recency, priority, and lexical relevance scoring.
- [ ] Guarantee configured context budget limits.

## 4. Agent adapters

- [ ] Define the thin adapter interface.
- [ ] Add Codex CLI hook support.
- [ ] Add Claude Code hook support.
- [ ] Add install, uninstall, status, and doctor commands.

## 5. Benchmark suite

- [ ] Implement reproducible 10-, 30-, and 50-turn synthetic fixtures.
- [ ] Add continuous-development, requirement-reversal, and resume scenarios.
- [ ] Compare full transcript, summary-only, strict, and balanced modes.
- [ ] Report quality gates, median and worst-case results, and token estimates.

## 6. Open-source distribution

- [ ] Confirm the final GitHub module path before the first public release.
- [ ] Add supported-platform CI after build commands are stable.
- [ ] Add security, privacy, and contribution documentation.
- [ ] Prepare a draft `v0.1.0` release only after MVP verification passes.
