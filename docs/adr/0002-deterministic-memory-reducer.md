# ADR 0002: Deterministic memory reducer

## Status

Accepted for the deterministic memory reducer milestone.

## Context

The journal stores validated incremental operations, but future context
compilation needs a current memory view. That view must be rebuildable and must
not silently overwrite stale, duplicated, or contradictory records.

Importance and contradiction handling are separate concerns. Reusing the word
`critical` for both would make the protocol ambiguous and would let an
extractor influence whether a conflict blocks later automation.

## Decision

- Keep record `priority` as `critical`, `high`, `normal`, or `low`. Priority is
  an input used by later context selection.
- Add `conflict_key` to memory records. It identifies a single semantic slot
  whose active values are expected to agree, such as
  `privacy.prompt_retention`. Critical records require a key. Non-critical
  records may omit it when they are naturally multi-valued.
- Derive contradiction `impact` inside the reducer; extractors cannot provide
  it. A disagreement involving any critical record is `blocking`. Other
  disagreements are `advisory`.
- Treat records with the same non-empty key, kind, and
  case/whitespace-normalized value as duplicates. Records without keys are not
  compared because they represent naturally multi-valued memory. Records with
  different keys are never deduplicated.
- Apply operations in durable journal sequence order:
  - `add` creates an active or duplicate record.
  - `supersede` deactivates an active target and creates a replacement with the
    same kind and conflict key.
  - `resolve` completes an active blocker, question, or task.
  - `expire` deactivates any active record.
- Invalid targets and invalid lifecycle transitions fail the complete rebuild.
- Recompute contradictions from the final active set, so a later operation in
  the same journal can clear an earlier disagreement.
- Build the complete view from immutable operations and replace all derived
  tables in one transaction. Update the reducer cursor only in that same
  transaction.
- Calculate a deterministic SHA-256 digest over the ordered materialized view.
  Loading the view verifies its row counts and digest.

## Schema v2

The forward-only migration creates:

- `memory_records`: active and historical materialized lifecycle records.
- `memory_contradictions`: deterministic record pairs with `advisory` or
  `blocking` impact.
- `memory_view_state`: source cursors, row counts, and deterministic view
  digest.

The immutable `memory_operations` table remains the source used for rebuilds.
Materialized tables are derived data and may be deleted and recreated.

## Compatibility

`conflict_key` is an additive JSON field, but the new validation rule requires
it on critical records. A pre-release database containing critical operation
records without this field will fail rebuilding instead of guessing a key; its
operations must be re-extracted. No stable public protocol release exists yet,
so the stricter rule is adopted before `v0.1.0`.

Older binaries reject the new field because v1 decoding rejects unknown JSON
fields. Mixed-version writers and reducers are therefore unsupported during
this pre-release migration.

## Consequences

- Conflict handling cannot be downgraded by model output.
- Blocking conflicts remain visible for a later compiler or adapter gate.
- Rebuilding the complete view favors deterministic correctness over
  incremental performance. Incremental reduction may be added later only if it
  produces the same digest as a full rebuild.
- Generated memory remains subordinate to repository evidence and explicit
  user instructions.
