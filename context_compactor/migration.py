from __future__ import annotations

import hashlib
import json
import re
import sqlite3
from contextlib import closing
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Mapping, Optional, Tuple

from .paths import PathValue, project_paths, resolve_project_root
from .privacy import PrivacyFilterError, contains_known_secret, sanitize_prompt
from .state import (
    DEFAULT_TOKEN_BUDGET,
    STATE_LIST_FIELDS,
    ProjectState,
    StateEntry,
    StateError,
    StateMetadata,
    StateValidationError,
    estimate_input_tokens,
    load_state_file,
    publish_state,
    serialize_state,
)

LEGACY_DATABASE_NAME = "context.db"
LEGACY_MIN_SCHEMA_VERSION = 2
LEGACY_MAX_SCHEMA_VERSION = 5

_LEGACY_SCHEMA_CHECKSUMS = {
    1: "2f2f91d61cba529eb3b7911c02914697626a1c2c526bbfce15c8e3c882d77254",
    2: "5cb66cdd121f9322f9c893444f4f3f15ff948f0de0d7e00e1cf1c462cb8500cb",
    3: "a593c764e9b8daa3e0ef94f8a663e2b5611b3b3b1665929047dc8a147ca549eb",
    4: "747d855239ee39e6c1411138623a5cfd22a4390acc70c89c77b043ff761e8c22",
    5: "c46e23419fca9741427f0a0de688857ed2aba15840d17d0dbdbb3ada1b6d11fe",
}
_REQUIRED_TABLE_COLUMNS = {
    "schema_migrations": frozenset(("version", "checksum", "applied_at")),
    "memory_records": frozenset(
        (
            "record_id",
            "conflict_key",
            "kind",
            "canonical_value",
            "priority",
            "lifecycle_status",
            "record_json",
            "source_event_id",
            "source_operation_id",
            "source_operation_seq",
            "terminal_operation_id",
            "superseded_by_record_id",
            "duplicate_of_record_id",
        )
    ),
    "memory_view_state": frozenset(
        (
            "singleton",
            "last_event_seq",
            "last_operation_seq",
            "view_digest",
            "record_count",
            "contradiction_count",
        )
    ),
}
_KIND_TO_STATE_FIELD = {
    "goal": "goals",
    "acceptance_criterion": "goals",
    "constraint": "constraints",
    "decision": "decisions",
    "task": "open_tasks",
    "blocker": "blockers",
    "question": "open_questions",
    "test_result": "recent_verification",
}
_UNSUPPORTED_KINDS = frozenset(("file",))
_LEGACY_RECORD_FIELDS = frozenset(
    (
        "id",
        "conflict_key",
        "kind",
        "value",
        "priority",
        "confidence",
        "status",
        "source",
        "created_at",
        "expires_at",
    )
)
_LEGACY_REQUIRED_RECORD_FIELDS = frozenset(
    (
        "id",
        "kind",
        "value",
        "priority",
        "confidence",
        "status",
        "source",
        "created_at",
    )
)
_LEGACY_SOURCE_FIELDS = frozenset(("event_id", "evidence", "artifact"))
_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
_CONFLICT_KEY_PATTERN = re.compile(r"^[a-z0-9][a-z0-9._:-]{0,127}$")
_PRIORITIES = frozenset(("critical", "high", "normal", "low"))
_CONFIDENCES = frozenset(("explicit", "verified", "inferred"))
_READ_CHUNK_BYTES = 1024 * 1024
_BLOCKING_ISSUES = frozenset(
    (
        "invalid_records_present",
        "candidate_exceeds_state_budget",
        "existing_state_is_invalid",
        "existing_state_conflict",
    )
)


class MigrationError(RuntimeError):
    """Raised when a legacy database cannot be migrated safely."""


@dataclass(frozen=True)
class _Fingerprint:
    size: int
    modified_ns: int
    sha256: str


@dataclass(frozen=True)
class MigrationPreview:
    project_root: Path
    database_path: Path
    schema_version: int
    source_fingerprint: _Fingerprint
    total_records: int
    active_records: int
    migrated_records: int
    unsupported_records: int
    inactive_records: int
    invalid_records: int
    redaction_count: int
    omitted_source_references: int
    legacy_operation_cursor: int
    target_counts: Mapping[str, int]
    candidate_estimated_tokens: int
    target_state: str
    issues: Tuple[str, ...]
    candidate: ProjectState

    @property
    def can_apply(self) -> bool:
        return not any(issue in _BLOCKING_ISSUES for issue in self.issues)

    def as_mapping(
        self,
        *,
        action: str = "preview",
        status: str = "previewed",
        applied: bool = False,
    ) -> dict[str, object]:
        return {
            "ok": True,
            "action": action,
            "status": status,
            "applied": applied,
            "database_path": ".context-compactor/context.db",
            "schema_version": self.schema_version,
            "source_checksum": self.source_fingerprint.sha256,
            "source_unchanged": True,
            "total_records": self.total_records,
            "active_records": self.active_records,
            "migrated_records": self.migrated_records,
            "unsupported_records": self.unsupported_records,
            "inactive_records": self.inactive_records,
            "invalid_records": self.invalid_records,
            "redaction_count": self.redaction_count,
            "omitted_source_references": self.omitted_source_references,
            "legacy_operation_cursor": self.legacy_operation_cursor,
            "target_counts": dict(self.target_counts),
            "candidate_estimated_tokens": self.candidate_estimated_tokens,
            "target_state": self.target_state,
            "can_apply": self.can_apply,
            "issues": list(self.issues),
        }


def preview_legacy_migration(project_root: PathValue) -> dict[str, object]:
    return _prepare_migration(project_root).as_mapping()


def apply_legacy_migration(project_root: PathValue) -> dict[str, object]:
    preview = _prepare_migration(project_root)
    if not preview.can_apply:
        issues = ", ".join(preview.issues) or "migration_precondition_failed"
        raise MigrationError(f"legacy migration cannot be applied: {issues}")
    if preview.target_state == "already_migrated":
        return preview.as_mapping(
            action="apply",
            status="already_migrated",
            applied=False,
        )

    if _fingerprint(preview.database_path) != preview.source_fingerprint:
        raise MigrationError("legacy database changed after preview")
    try:
        publish_state(
            preview.project_root,
            preview.candidate,
        )
    except (OSError, StateError) as error:
        raise MigrationError(
            "state publication failed; the legacy database was retained"
        ) from error
    return preview.as_mapping(
        action="apply",
        status="migrated",
        applied=True,
    )


def _prepare_migration(project_root: PathValue) -> MigrationPreview:
    root = resolve_project_root(project_root)
    database_path = root / ".context-compactor" / LEGACY_DATABASE_NAME
    _validate_database_file(database_path)
    before = _fingerprint(database_path)

    try:
        with closing(_open_read_only(database_path)) as connection:
            connection.row_factory = sqlite3.Row
            schema_version = _validate_schema(connection)
            (
                rows,
                total_records,
                legacy_operation_cursor,
            ) = _read_active_records(connection)
    except MigrationError:
        raise
    except sqlite3.Error as error:
        raise MigrationError("legacy database could not be read safely") from error

    after = _fingerprint(database_path)
    if before != after:
        raise MigrationError("legacy database changed while it was being read")

    entries: dict[str, list[StateEntry]] = {
        name: [] for name in STATE_LIST_FIELDS
    }
    unsupported_records = 0
    invalid_records = 0
    redaction_count = 0
    omitted_sources = 0
    for row in rows:
        kind = row["kind"]
        if kind in _UNSUPPORTED_KINDS:
            unsupported_records += 1
            continue
        target_field = _KIND_TO_STATE_FIELD.get(kind)
        if target_field is None:
            invalid_records += 1
            continue
        try:
            entry, record_redactions, sources_omitted = _convert_record(row)
        except (MigrationError, PrivacyFilterError, StateValidationError):
            invalid_records += 1
            continue
        entries[target_field].append(entry)
        redaction_count += record_redactions
        omitted_sources += sources_omitted

    candidate = ProjectState(
        **{name: tuple(entries[name]) for name in STATE_LIST_FIELDS},
        metadata=StateMetadata(source_cursor=0, updated_at=""),
    )
    candidate.validate()
    candidate_tokens = estimate_input_tokens(serialize_state(candidate))
    target_state, target_issue = _target_state(root, candidate)

    issues = []
    if unsupported_records:
        issues.append("unsupported_records_skipped")
    if invalid_records:
        issues.append("invalid_records_present")
    if redaction_count:
        issues.append("recognized_secrets_redacted")
    if omitted_sources:
        issues.append("source_references_omitted")
    if candidate_tokens > DEFAULT_TOKEN_BUDGET:
        issues.append("candidate_exceeds_state_budget")
    if target_issue is not None:
        issues.append(target_issue)

    migrated_records = sum(len(values) for values in entries.values())
    active_records = len(rows)
    return MigrationPreview(
        project_root=root,
        database_path=database_path.resolve(strict=True),
        schema_version=schema_version,
        source_fingerprint=before,
        total_records=total_records,
        active_records=active_records,
        migrated_records=migrated_records,
        unsupported_records=unsupported_records,
        inactive_records=total_records - active_records,
        invalid_records=invalid_records,
        redaction_count=redaction_count,
        omitted_source_references=omitted_sources,
        legacy_operation_cursor=legacy_operation_cursor,
        target_counts={
            name: len(entries[name])
            for name in STATE_LIST_FIELDS
        },
        candidate_estimated_tokens=candidate_tokens,
        target_state=target_state,
        issues=tuple(issues),
        candidate=candidate,
    )


def _validate_database_file(database_path: Path) -> None:
    if not database_path.exists():
        raise MigrationError("legacy database does not exist")
    if not database_path.is_file():
        raise MigrationError("legacy database must be a regular file")
    for suffix in ("-wal", "-journal"):
        sidecar = Path(str(database_path) + suffix)
        try:
            pending = sidecar.exists() and sidecar.stat().st_size > 0
        except OSError as error:
            raise MigrationError("legacy database sidecar could not be inspected") from error
        if pending:
            raise MigrationError(
                "legacy database has a pending transaction; "
                "stop the old runtime and checkpoint it before migration"
            )


def _open_read_only(database_path: Path) -> sqlite3.Connection:
    uri = database_path.resolve(strict=True).as_uri() + "?mode=ro&immutable=1"
    connection = sqlite3.connect(uri, uri=True, timeout=5.0)
    try:
        connection.execute("PRAGMA query_only = ON")
        return connection
    except BaseException:
        connection.close()
        raise


def _validate_schema(connection: sqlite3.Connection) -> int:
    quick_check = connection.execute("PRAGMA quick_check(1)").fetchone()
    if quick_check is None or quick_check[0] != "ok":
        raise MigrationError("legacy database integrity check failed")

    tables = {
        row[0]: row[1]
        for row in connection.execute(
            """
SELECT name, type
FROM sqlite_schema
WHERE name IN ('schema_migrations', 'memory_records', 'memory_view_state')
"""
        )
    }
    if any(tables.get(name) != "table" for name in _REQUIRED_TABLE_COLUMNS):
        raise MigrationError("legacy database schema is incomplete")
    for table, required in _REQUIRED_TABLE_COLUMNS.items():
        columns = {
            row[1]
            for row in connection.execute(f"PRAGMA table_info('{table}')")
        }
        if not required.issubset(columns):
            raise MigrationError("legacy database schema is incomplete")

    migrations = connection.execute(
        "SELECT version, checksum FROM schema_migrations ORDER BY version"
    ).fetchall()
    if not migrations:
        raise MigrationError("legacy database has no schema version")
    versions = [row[0] for row in migrations]
    if any(
        isinstance(version, bool) or not isinstance(version, int)
        for version in versions
    ):
        raise MigrationError("legacy database schema version is invalid")
    latest = versions[-1]
    if (
        latest < LEGACY_MIN_SCHEMA_VERSION
        or latest > LEGACY_MAX_SCHEMA_VERSION
        or versions != list(range(1, latest + 1))
    ):
        raise MigrationError("legacy database schema version is unsupported")
    for version, checksum in migrations:
        if checksum != _LEGACY_SCHEMA_CHECKSUMS.get(version):
            raise MigrationError("legacy database migration checksum is invalid")
    return latest


def _read_active_records(
    connection: sqlite3.Connection,
) -> tuple[list[sqlite3.Row], int, int]:
    total_records = int(
        connection.execute("SELECT COUNT(*) FROM memory_records").fetchone()[0]
    )
    view_rows = connection.execute(
        """
SELECT last_operation_seq, record_count
FROM memory_view_state
WHERE singleton = 1
"""
    ).fetchall()
    if len(view_rows) > 1:
        raise MigrationError("legacy memory view state is invalid")
    if not view_rows:
        if total_records:
            raise MigrationError("legacy memory view state is missing")
        legacy_cursor = 0
    else:
        legacy_cursor = view_rows[0]["last_operation_seq"]
        record_count = view_rows[0]["record_count"]
        if (
            isinstance(legacy_cursor, bool)
            or not isinstance(legacy_cursor, int)
            or legacy_cursor < 0
            or isinstance(record_count, bool)
            or not isinstance(record_count, int)
            or record_count != total_records
        ):
            raise MigrationError("legacy memory view state is invalid")

    rows = connection.execute(
        """
SELECT record_id, conflict_key, kind, canonical_value, priority,
       lifecycle_status, record_json, source_event_id, source_operation_id,
       source_operation_seq, terminal_operation_id, superseded_by_record_id,
       duplicate_of_record_id
FROM memory_records
WHERE lifecycle_status = 'active'
ORDER BY source_operation_seq, record_id
"""
    ).fetchall()
    sequences = [row["source_operation_seq"] for row in rows]
    if any(
        isinstance(sequence, bool)
        or not isinstance(sequence, int)
        or sequence <= 0
        for sequence in sequences
    ):
        raise MigrationError("legacy memory view cursor is invalid")
    if legacy_cursor < max(sequences, default=0):
        raise MigrationError("legacy memory view cursor is invalid")
    return rows, total_records, legacy_cursor


def _convert_record(row: sqlite3.Row) -> tuple[StateEntry, int, int]:
    if (
        row["lifecycle_status"] != "active"
        or row["terminal_operation_id"] is not None
        or row["superseded_by_record_id"] is not None
        or row["duplicate_of_record_id"] is not None
    ):
        raise MigrationError("legacy active record lifecycle is invalid")
    if (
        isinstance(row["source_operation_seq"], bool)
        or not isinstance(row["source_operation_seq"], int)
        or row["source_operation_seq"] <= 0
    ):
        raise MigrationError("legacy active record sequence is invalid")

    try:
        record = json.loads(row["record_json"])
    except (json.JSONDecodeError, TypeError) as error:
        raise MigrationError("legacy record JSON is invalid") from error
    if not isinstance(record, dict):
        raise MigrationError("legacy record JSON must be an object")
    if (
        set(record) - _LEGACY_RECORD_FIELDS
        or not _LEGACY_REQUIRED_RECORD_FIELDS.issubset(record)
    ):
        raise MigrationError("legacy record fields are invalid")

    record_id = _legacy_string(record, "id")
    conflict_key = _legacy_optional_string(record, "conflict_key")
    kind = _legacy_string(record, "kind")
    value = _legacy_string(record, "value")
    priority = _legacy_string(record, "priority")
    confidence = _legacy_string(record, "confidence")
    status = _legacy_string(record, "status")
    created_at = _legacy_time(record, "created_at")
    expires_at = _legacy_optional_time(record, "expires_at")
    source = record["source"]
    if not isinstance(source, dict) or set(source) - _LEGACY_SOURCE_FIELDS:
        raise MigrationError("legacy record source is invalid")
    event_id = _legacy_string(source, "event_id")
    evidence = _legacy_optional_string(source, "evidence")
    artifact = _legacy_optional_string(source, "artifact")

    if (
        _ID_PATTERN.fullmatch(record_id) is None
        or _ID_PATTERN.fullmatch(event_id) is None
        or (
            conflict_key
            and _CONFLICT_KEY_PATTERN.fullmatch(conflict_key) is None
        )
        or kind != row["kind"]
        or kind not in _KIND_TO_STATE_FIELD
        or priority not in _PRIORITIES
        or confidence not in _CONFIDENCES
        or status != "active"
        or record_id != row["record_id"]
        or conflict_key != row["conflict_key"]
        or priority != row["priority"]
        or event_id != row["source_event_id"]
        or not value.strip()
        or len(value) > 2_000
        or len(artifact) > 1_024
        or len(evidence) > 8_000
        or (expires_at is not None and expires_at <= created_at)
    ):
        raise MigrationError("legacy record values are invalid")

    sanitized = sanitize_prompt(value, max_characters=2_000)
    if not sanitized.text or contains_known_secret(sanitized.text):
        raise MigrationError("legacy record could not be sanitized")

    source_event_id: Optional[str] = event_id
    omitted_sources = 0
    if contains_known_secret(source_event_id):
        source_event_id = None
        omitted_sources += 1

    source_value: Optional[str] = artifact.strip() or None
    if source_value is not None:
        try:
            if contains_known_secret(source_value):
                raise StateValidationError("source contains a recognized secret")
            StateEntry(
                statement=sanitized.text,
                source=source_value,
                source_event_id=source_event_id,
            ).validate()
        except (PrivacyFilterError, StateValidationError):
            source_value = None
            omitted_sources += 1

    entry = StateEntry(
        statement=sanitized.text,
        source=source_value,
        source_event_id=source_event_id,
    )
    entry.validate()
    return entry, sanitized.redaction_count, omitted_sources


def _legacy_string(value: Mapping[str, object], name: str) -> str:
    item = value.get(name)
    if not isinstance(item, str):
        raise MigrationError(f"legacy record {name} must be text")
    return item


def _legacy_optional_string(value: Mapping[str, object], name: str) -> str:
    item = value.get(name, "")
    if not isinstance(item, str):
        raise MigrationError(f"legacy record {name} must be text")
    return item


def _legacy_time(value: Mapping[str, object], name: str) -> datetime:
    item = _legacy_string(value, name)
    try:
        parsed = datetime.fromisoformat(item.replace("Z", "+00:00"))
    except ValueError as error:
        raise MigrationError(f"legacy record {name} is invalid") from error
    if (
        parsed.tzinfo is None
        or parsed.utcoffset() != timezone.utc.utcoffset(parsed)
    ):
        raise MigrationError(f"legacy record {name} must use UTC")
    return parsed


def _legacy_optional_time(
    value: Mapping[str, object],
    name: str,
) -> Optional[datetime]:
    if name not in value:
        return None
    return _legacy_time(value, name)


def _target_state(
    project_root: Path,
    candidate: ProjectState,
) -> tuple[str, Optional[str]]:
    state_path = project_paths(project_root).state
    if not state_path.exists():
        return "missing", None
    try:
        current = load_state_file(state_path)
    except StateError:
        return "invalid", "existing_state_is_invalid"
    if current == candidate:
        return "already_migrated", None
    if current == ProjectState.empty():
        return "empty", None
    return "conflict", "existing_state_conflict"


def _fingerprint(path: Path) -> _Fingerprint:
    try:
        stat = path.stat()
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            while True:
                chunk = stream.read(_READ_CHUNK_BYTES)
                if not chunk:
                    break
                digest.update(chunk)
    except OSError as error:
        raise MigrationError("legacy database could not be fingerprinted") from error
    return _Fingerprint(
        size=stat.st_size,
        modified_ns=stat.st_mtime_ns,
        sha256=digest.hexdigest(),
    )
