from __future__ import annotations

import hashlib
import os
import re
import secrets
import sqlite3
from contextlib import contextmanager
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Iterator, Optional, Tuple

from .paths import PathValue, project_paths
from .privacy import (
    MAX_RETAINED_CHARACTERS,
    PrivacyFilterError,
    contains_known_secret,
)

SCHEMA_VERSION = 1
MAX_JOB_RETENTION = 500
MAX_PROMPT_AGE = timedelta(days=7)
VALID_OUTCOMES = frozenset({"updated", "no_change"})
VALID_FAILURE_CATEGORIES = frozenset(
    {
        "expired",
        "invalid_output",
        "model_error",
        "publish_error",
        "state_validation",
        "unknown",
        "worker_crash",
    }
)
_DIGEST = re.compile(r"^[0-9a-f]{64}$")
_MIGRATION_TABLE_SQL = """
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
)
"""
_MIGRATIONS = (
    (
        1,
        """
CREATE TABLE events (
    event_seq INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE jobs (
    event_seq INTEGER PRIMARY KEY REFERENCES events(event_seq) ON DELETE CASCADE,
    event_id TEXT NOT NULL UNIQUE REFERENCES events(event_id),
    prompt_text TEXT,
    prompt_digest TEXT NOT NULL,
    redaction_count INTEGER NOT NULL CHECK (redaction_count >= 0),
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'processing', 'completed', 'failed')
    ),
    outcome TEXT CHECK (outcome IN ('updated', 'no_change')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    enqueued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    lease_token TEXT,
    lease_until TEXT,
    last_attempt_at TEXT,
    next_attempt_at TEXT,
    completed_at TEXT,
    failure_category TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX jobs_status_sequence_idx ON jobs(status, event_seq);
CREATE TABLE runtime_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    source_cursor INTEGER NOT NULL DEFAULT 0 CHECK (source_cursor >= 0),
    worker_lease_token TEXT,
    worker_lease_until TEXT,
    last_worker_activity_at TEXT
);
INSERT INTO runtime_state(singleton, source_cursor) VALUES (1, 0);
""",
    ),
)


class JournalError(ValueError):
    """Base class for lightweight journal failures."""


class JournalConflictError(JournalError):
    """Raised when one stable event identity is reused with different data."""


class JournalLeaseError(JournalError):
    """Raised when a caller no longer owns a job or worker lease."""


@dataclass(frozen=True)
class EnqueueRequest:
    event_id: str
    kind: str
    occurred_at: datetime
    content_digest: str
    prompt: str
    redaction_count: int
    enqueued_at: datetime
    expires_at: Optional[datetime] = None


@dataclass(frozen=True)
class EnqueueResult:
    event_seq: int
    inserted: bool
    status: str


@dataclass(frozen=True)
class ClaimedJob:
    event_seq: int
    event_id: str
    kind: str
    prompt: str
    prompt_digest: str
    redaction_count: int
    attempt_count: int
    enqueued_at: datetime
    expires_at: datetime
    lease_token: str
    lease_until: datetime


@dataclass(frozen=True)
class JobSnapshot:
    event_seq: int
    event_id: str
    status: str
    outcome: Optional[str]
    attempt_count: int
    prompt_present: bool
    lease_until: Optional[datetime]
    next_attempt_at: Optional[datetime]
    completed_at: Optional[datetime]
    failure_category: Optional[str]


@dataclass(frozen=True)
class RuntimeSnapshot:
    source_cursor: int
    worker_lease_active: bool
    worker_lease_until: Optional[datetime]
    last_worker_activity_at: Optional[datetime]


@dataclass(frozen=True)
class PruneResult:
    prompts_scrubbed: int
    jobs_deleted: int


class Journal:
    def __init__(self, connection: sqlite3.Connection, path: Path) -> None:
        self._connection = connection
        self.path = path

    @classmethod
    def open(
        cls,
        project_root: PathValue,
        *,
        path: Optional[PathValue] = None,
    ) -> "Journal":
        paths = project_paths(project_root)
        journal_path = Path(path).resolve(strict=False) if path else paths.journal
        journal_path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        connection = sqlite3.connect(
            str(journal_path),
            timeout=5.0,
            isolation_level=None,
        )
        connection.row_factory = sqlite3.Row
        try:
            connection.execute("PRAGMA busy_timeout = 5000")
            connection.execute("PRAGMA foreign_keys = ON")
            connection.execute("PRAGMA synchronous = FULL")
            mode = connection.execute("PRAGMA journal_mode = WAL").fetchone()[0]
            if str(mode).lower() != "wal":
                raise JournalError("SQLite WAL journal mode is unavailable")
            _apply_migrations(connection)
            os.chmod(journal_path, 0o600)
            return cls(connection, journal_path)
        except BaseException:
            connection.close()
            raise

    def close(self) -> None:
        self._connection.close()

    def __enter__(self) -> "Journal":
        return self

    def __exit__(self, _type: object, _value: object, _traceback: object) -> None:
        self.close()

    @property
    def schema_version(self) -> int:
        row = self._connection.execute(
            "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"
        ).fetchone()
        return int(row[0])

    def enqueue(self, request: EnqueueRequest) -> EnqueueResult:
        prepared = _prepare_enqueue(request)
        with self._transaction() as connection:
            existing = connection.execute(
                """
SELECT e.event_seq, e.kind, e.occurred_at, e.content_digest,
       j.prompt_digest, j.redaction_count, j.enqueued_at, j.expires_at, j.status
FROM events e
JOIN jobs j ON j.event_seq = e.event_seq
WHERE e.event_id = ?
""",
                (prepared.event_id,),
            ).fetchone()
            if existing is not None:
                _verify_duplicate(existing, prepared)
                return EnqueueResult(
                    event_seq=int(existing["event_seq"]),
                    inserted=False,
                    status=str(existing["status"]),
                )

            cursor = connection.execute(
                """
INSERT INTO events(event_id, kind, occurred_at, content_digest, created_at)
VALUES (?, ?, ?, ?, ?)
""",
                (
                    prepared.event_id,
                    prepared.kind,
                    prepared.occurred_at,
                    prepared.content_digest,
                    prepared.enqueued_at,
                ),
            )
            event_seq = int(cursor.lastrowid)
            connection.execute(
                """
INSERT INTO jobs(
    event_seq, event_id, prompt_text, prompt_digest, redaction_count,
    status, attempt_count, enqueued_at, expires_at, updated_at
) VALUES (?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)
""",
                (
                    event_seq,
                    prepared.event_id,
                    prepared.prompt,
                    prepared.prompt_digest,
                    prepared.redaction_count,
                    prepared.enqueued_at,
                    prepared.expires_at,
                    prepared.enqueued_at,
                ),
            )
            return EnqueueResult(
                event_seq=event_seq,
                inserted=True,
                status="pending",
            )

    def claim_next(
        self,
        now: datetime,
        *,
        lease_duration: timedelta,
    ) -> Optional[ClaimedJob]:
        now_text = _format_time(now)
        if lease_duration <= timedelta(0):
            raise JournalError("job lease duration must be positive")
        lease_until = _as_utc(now) + lease_duration
        lease_until_text = _format_time(lease_until)
        with self._transaction() as connection:
            self._scrub_expired(connection, now)
            row = connection.execute(
                """
SELECT e.event_seq, e.event_id, e.kind, j.*
FROM events e
JOIN jobs j ON j.event_seq = e.event_seq
WHERE j.status IN ('pending', 'processing')
ORDER BY e.event_seq
LIMIT 1
"""
            ).fetchone()
            if row is None:
                return None
            if row["status"] == "pending":
                retry_at = _parse_optional_time(row["next_attempt_at"])
                if retry_at is not None and retry_at > _as_utc(now):
                    return None
            else:
                current_lease = _parse_optional_time(row["lease_until"])
                if current_lease is not None and current_lease > _as_utc(now):
                    return None
            if row["prompt_text"] is None:
                raise JournalError("claimable job has no retained prompt")

            token = secrets.token_hex(24)
            cursor = connection.execute(
                """
UPDATE jobs
SET status = 'processing',
    attempt_count = attempt_count + 1,
    lease_token = ?,
    lease_until = ?,
    last_attempt_at = ?,
    next_attempt_at = NULL,
    updated_at = ?
WHERE event_seq = ?
  AND (
      status = 'pending'
      OR (status = 'processing' AND lease_until <= ?)
  )
""",
                (
                    token,
                    lease_until_text,
                    now_text,
                    now_text,
                    row["event_seq"],
                    now_text,
                ),
            )
            if cursor.rowcount != 1:
                raise JournalLeaseError("job lease was lost")
            return ClaimedJob(
                event_seq=int(row["event_seq"]),
                event_id=str(row["event_id"]),
                kind=str(row["kind"]),
                prompt=str(row["prompt_text"]),
                prompt_digest=str(row["prompt_digest"]),
                redaction_count=int(row["redaction_count"]),
                attempt_count=int(row["attempt_count"]) + 1,
                enqueued_at=_parse_time(row["enqueued_at"]),
                expires_at=_parse_time(row["expires_at"]),
                lease_token=token,
                lease_until=lease_until,
            )

    def complete(
        self,
        event_id: str,
        lease_token: str,
        outcome: str,
        completed_at: datetime,
    ) -> None:
        _validate_identifier("event_id", event_id)
        _validate_identifier("lease_token", lease_token)
        if outcome not in VALID_OUTCOMES:
            raise JournalError("job outcome must be updated or no_change")
        completed_text = _format_time(completed_at)
        with self._transaction() as connection:
            row = connection.execute(
                """
SELECT event_seq
FROM jobs
WHERE event_id = ?
  AND status = 'processing'
  AND lease_token = ?
  AND lease_until > ?
""",
                (event_id, lease_token, completed_text),
            ).fetchone()
            if row is None:
                raise JournalLeaseError("job completion lease was lost")
            connection.execute(
                """
UPDATE jobs
SET prompt_text = NULL,
    status = 'completed',
    outcome = ?,
    lease_token = NULL,
    lease_until = NULL,
    next_attempt_at = NULL,
    completed_at = ?,
    failure_category = NULL,
    updated_at = ?
WHERE event_id = ?
""",
                (outcome, completed_text, completed_text, event_id),
            )
            _advance_cursor(connection, completed_text)

    def retry(
        self,
        event_id: str,
        lease_token: str,
        *,
        category: str,
        failed_at: datetime,
        retry_at: Optional[datetime],
        retryable: bool,
    ) -> None:
        _validate_identifier("event_id", event_id)
        _validate_identifier("lease_token", lease_token)
        if category not in VALID_FAILURE_CATEGORIES:
            raise JournalError("failure category is not allowed")
        failed_text = _format_time(failed_at)
        retry_text: Optional[str] = None
        if retryable:
            if retry_at is None or _as_utc(retry_at) < _as_utc(failed_at):
                raise JournalError("retry_at must not precede failed_at")
            retry_text = _format_time(retry_at)
        elif retry_at is not None:
            raise JournalError("terminal failure must not set retry_at")

        with self._transaction() as connection:
            row = connection.execute(
                """
SELECT event_seq
FROM jobs
WHERE event_id = ?
  AND status = 'processing'
  AND lease_token = ?
  AND lease_until > ?
""",
                (event_id, lease_token, failed_text),
            ).fetchone()
            if row is None:
                raise JournalLeaseError("job retry lease was lost")
            status = "pending" if retryable else "failed"
            completed_at = None if retryable else failed_text
            connection.execute(
                """
UPDATE jobs
SET status = ?,
    outcome = NULL,
    lease_token = NULL,
    lease_until = NULL,
    next_attempt_at = ?,
    completed_at = ?,
    failure_category = ?,
    updated_at = ?
WHERE event_id = ?
""",
                (
                    status,
                    retry_text,
                    completed_at,
                    category,
                    failed_text,
                    event_id,
                ),
            )
            if not retryable:
                _advance_cursor(connection, failed_text)

    def acquire_worker_lease(
        self,
        token: str,
        now: datetime,
        *,
        lease_duration: timedelta,
    ) -> bool:
        _validate_identifier("worker lease token", token)
        if lease_duration <= timedelta(0):
            raise JournalError("worker lease duration must be positive")
        now_text = _format_time(now)
        until_text = _format_time(_as_utc(now) + lease_duration)
        with self._transaction() as connection:
            cursor = connection.execute(
                """
UPDATE runtime_state
SET worker_lease_token = ?,
    worker_lease_until = ?,
    last_worker_activity_at = ?
WHERE singleton = 1
  AND (
      worker_lease_token IS NULL
      OR worker_lease_until <= ?
      OR worker_lease_token = ?
  )
""",
                (token, until_text, now_text, now_text, token),
            )
            return cursor.rowcount == 1

    def release_worker_lease(
        self,
        token: str,
        released_at: datetime,
    ) -> None:
        _validate_identifier("worker lease token", token)
        released_text = _format_time(released_at)
        with self._transaction() as connection:
            cursor = connection.execute(
                """
UPDATE runtime_state
SET worker_lease_token = NULL,
    worker_lease_until = NULL,
    last_worker_activity_at = ?
WHERE singleton = 1 AND worker_lease_token = ?
""",
                (released_text, token),
            )
            if cursor.rowcount != 1:
                raise JournalLeaseError("worker lease was lost")

    def prune(
        self,
        now: datetime,
        *,
        max_jobs: int = MAX_JOB_RETENTION,
    ) -> PruneResult:
        if isinstance(max_jobs, bool) or not isinstance(max_jobs, int) or max_jobs < 0:
            raise JournalError("max_jobs must be a non-negative integer")
        now_utc = _as_utc(now)
        with self._transaction() as connection:
            scrubbed = self._scrub_expired(connection, now_utc)
            rows = connection.execute(
                """
SELECT event_seq, status, lease_until
FROM jobs
ORDER BY event_seq DESC
"""
            ).fetchall()
            active = {
                int(row["event_seq"])
                for row in rows
                if row["status"] == "processing"
                and _parse_optional_time(row["lease_until"]) is not None
                and _parse_time(row["lease_until"]) > now_utc
            }
            available = max(0, max_jobs - len(active))
            keep = set(active)
            for row in rows:
                sequence = int(row["event_seq"])
                if sequence in active:
                    continue
                if available <= 0:
                    break
                keep.add(sequence)
                available -= 1
            deleted_sequences = [
                int(row["event_seq"])
                for row in rows
                if int(row["event_seq"]) not in keep
            ]
            if deleted_sequences:
                connection.executemany(
                    "DELETE FROM events WHERE event_seq = ?",
                    ((sequence,) for sequence in deleted_sequences),
                )
                _advance_cursor(connection, _format_time(now_utc))
            return PruneResult(
                prompts_scrubbed=scrubbed,
                jobs_deleted=len(deleted_sequences),
            )

    def job_snapshot(self, event_id: str) -> JobSnapshot:
        _validate_identifier("event_id", event_id)
        row = self._connection.execute(
            """
SELECT event_seq, event_id, status, outcome, attempt_count,
       prompt_text IS NOT NULL AS prompt_present,
       lease_until, next_attempt_at, completed_at, failure_category
FROM jobs
WHERE event_id = ?
""",
            (event_id,),
        ).fetchone()
        if row is None:
            raise JournalError("job was not found")
        return _snapshot(row)

    def list_snapshots(self) -> Tuple[JobSnapshot, ...]:
        rows = self._connection.execute(
            """
SELECT event_seq, event_id, status, outcome, attempt_count,
       prompt_text IS NOT NULL AS prompt_present,
       lease_until, next_attempt_at, completed_at, failure_category
FROM jobs
ORDER BY event_seq
"""
        ).fetchall()
        return tuple(_snapshot(row) for row in rows)

    def runtime_snapshot(self, now: datetime) -> RuntimeSnapshot:
        now_utc = _as_utc(now)
        row = self._connection.execute(
            """
SELECT source_cursor, worker_lease_token, worker_lease_until,
       last_worker_activity_at
FROM runtime_state
WHERE singleton = 1
"""
        ).fetchone()
        lease_until = _parse_optional_time(row["worker_lease_until"])
        return RuntimeSnapshot(
            source_cursor=int(row["source_cursor"]),
            worker_lease_active=bool(row["worker_lease_token"])
            and lease_until is not None
            and lease_until > now_utc,
            worker_lease_until=lease_until,
            last_worker_activity_at=_parse_optional_time(
                row["last_worker_activity_at"]
            ),
        )

    def _scrub_expired(
        self,
        connection: sqlite3.Connection,
        now: datetime,
    ) -> int:
        now_text = _format_time(now)
        rows = connection.execute(
            """
SELECT event_seq, status, prompt_text
FROM jobs
WHERE expires_at <= ?
""",
            (now_text,),
        ).fetchall()
        scrubbed = sum(1 for row in rows if row["prompt_text"] is not None)
        terminalized = [
            int(row["event_seq"])
            for row in rows
            if row["status"] in {"pending", "processing"}
        ]
        connection.execute(
            """
UPDATE jobs
SET prompt_text = NULL,
    status = CASE
        WHEN status IN ('pending', 'processing') THEN 'failed'
        ELSE status
    END,
    outcome = CASE
        WHEN status IN ('pending', 'processing') THEN NULL
        ELSE outcome
    END,
    lease_token = NULL,
    lease_until = NULL,
    next_attempt_at = NULL,
    completed_at = CASE
        WHEN status IN ('pending', 'processing') THEN ?
        ELSE completed_at
    END,
    failure_category = CASE
        WHEN status IN ('pending', 'processing') THEN 'expired'
        ELSE failure_category
    END,
    updated_at = ?
WHERE expires_at <= ?
""",
            (now_text, now_text, now_text),
        )
        if terminalized:
            _advance_cursor(connection, now_text)
        return scrubbed

    @contextmanager
    def _transaction(self) -> Iterator[sqlite3.Connection]:
        self._connection.execute("BEGIN IMMEDIATE")
        try:
            yield self._connection
        except BaseException:
            self._connection.rollback()
            raise
        else:
            self._connection.commit()


@dataclass(frozen=True)
class _PreparedEnqueue:
    event_id: str
    kind: str
    occurred_at: str
    content_digest: str
    prompt: str
    prompt_digest: str
    redaction_count: int
    enqueued_at: str
    expires_at: str


def digest_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _prepare_enqueue(request: EnqueueRequest) -> _PreparedEnqueue:
    _validate_identifier("event_id", request.event_id)
    _validate_identifier("kind", request.kind)
    if _DIGEST.fullmatch(request.content_digest) is None:
        raise JournalError("content_digest must be lowercase SHA-256")
    if not isinstance(request.prompt, str) or not request.prompt.strip():
        raise JournalError("prompt must be non-empty text")
    if len(request.prompt) > MAX_RETAINED_CHARACTERS:
        raise JournalError("prompt exceeds the retained character limit")
    try:
        if contains_known_secret(request.prompt):
            raise JournalError("prompt contains a defined secret pattern")
    except PrivacyFilterError as error:
        raise JournalError("prompt is not safe to persist") from error
    if (
        isinstance(request.redaction_count, bool)
        or not isinstance(request.redaction_count, int)
        or request.redaction_count < 0
    ):
        raise JournalError("redaction_count must be a non-negative integer")

    occurred_at = _as_utc(request.occurred_at)
    enqueued_at = _as_utc(request.enqueued_at)
    expires_at = (
        enqueued_at + MAX_PROMPT_AGE
        if request.expires_at is None
        else _as_utc(request.expires_at)
    )
    if expires_at <= enqueued_at or expires_at > enqueued_at + MAX_PROMPT_AGE:
        raise JournalError("prompt expiry must be after enqueue and within seven days")
    return _PreparedEnqueue(
        event_id=request.event_id,
        kind=request.kind,
        occurred_at=_format_time(occurred_at),
        content_digest=request.content_digest,
        prompt=request.prompt,
        prompt_digest=digest_text(request.prompt),
        redaction_count=request.redaction_count,
        enqueued_at=_format_time(enqueued_at),
        expires_at=_format_time(expires_at),
    )


def _verify_duplicate(row: sqlite3.Row, prepared: _PreparedEnqueue) -> None:
    expected = (
        prepared.kind,
        prepared.occurred_at,
        prepared.content_digest,
        prepared.prompt_digest,
        prepared.redaction_count,
        prepared.enqueued_at,
        prepared.expires_at,
    )
    actual = (
        row["kind"],
        row["occurred_at"],
        row["content_digest"],
        row["prompt_digest"],
        row["redaction_count"],
        row["enqueued_at"],
        row["expires_at"],
    )
    if actual != expected:
        raise JournalConflictError("event identity was reused with different data")


def _apply_migrations(connection: sqlite3.Connection) -> None:
    connection.execute("BEGIN IMMEDIATE")
    try:
        connection.execute(_MIGRATION_TABLE_SQL)
        for version, script in _MIGRATIONS:
            checksum = digest_text(script)
            row = connection.execute(
                "SELECT checksum FROM schema_migrations WHERE version = ?",
                (version,),
            ).fetchone()
            if row is not None:
                if row["checksum"] != checksum:
                    raise JournalError(
                        f"migration {version} checksum does not match"
                    )
                continue
            for statement in script.split(";"):
                if statement.strip():
                    connection.execute(statement)
            connection.execute(
                """
INSERT INTO schema_migrations(version, checksum, applied_at)
VALUES (?, ?, ?)
""",
                (version, checksum, _format_time(datetime.now(timezone.utc))),
            )
        connection.commit()
    except BaseException:
        connection.rollback()
        raise


def _advance_cursor(
    connection: sqlite3.Connection,
    activity_at: str,
) -> None:
    current = int(
        connection.execute(
            "SELECT source_cursor FROM runtime_state WHERE singleton = 1"
        ).fetchone()[0]
    )
    first_nonterminal = connection.execute(
        """
SELECT MIN(event_seq)
FROM jobs
WHERE event_seq > ? AND status IN ('pending', 'processing')
""",
        (current,),
    ).fetchone()[0]
    if first_nonterminal is None:
        maximum = connection.execute(
            "SELECT COALESCE(MAX(event_seq), ?) FROM events",
            (current,),
        ).fetchone()[0]
        target = max(current, int(maximum))
    else:
        target = max(current, int(first_nonterminal) - 1)
    connection.execute(
        """
UPDATE runtime_state
SET source_cursor = ?,
    last_worker_activity_at = ?
WHERE singleton = 1
""",
        (target, activity_at),
    )


def _snapshot(row: sqlite3.Row) -> JobSnapshot:
    return JobSnapshot(
        event_seq=int(row["event_seq"]),
        event_id=str(row["event_id"]),
        status=str(row["status"]),
        outcome=str(row["outcome"]) if row["outcome"] is not None else None,
        attempt_count=int(row["attempt_count"]),
        prompt_present=bool(row["prompt_present"]),
        lease_until=_parse_optional_time(row["lease_until"]),
        next_attempt_at=_parse_optional_time(row["next_attempt_at"]),
        completed_at=_parse_optional_time(row["completed_at"]),
        failure_category=(
            str(row["failure_category"])
            if row["failure_category"] is not None
            else None
        ),
    )


def _validate_identifier(name: str, value: str) -> None:
    if (
        not isinstance(value, str)
        or not value.strip()
        or len(value) > 256
        or any(ord(character) < 32 for character in value)
    ):
        raise JournalError(f"{name} is invalid")
    try:
        if contains_known_secret(value):
            raise JournalError(f"{name} contains a defined secret pattern")
    except PrivacyFilterError as error:
        raise JournalError(f"{name} is not safe to persist") from error


def _as_utc(value: datetime) -> datetime:
    if not isinstance(value, datetime) or value.tzinfo is None:
        raise JournalError("timestamps must be timezone-aware")
    return value.astimezone(timezone.utc)


def _format_time(value: datetime) -> str:
    return (
        _as_utc(value)
        .isoformat(timespec="microseconds")
        .replace("+00:00", "Z")
    )


def _parse_time(value: str) -> datetime:
    return datetime.fromisoformat(str(value).replace("Z", "+00:00")).astimezone(
        timezone.utc
    )


def _parse_optional_time(value: object) -> Optional[datetime]:
    if value is None:
        return None
    return _parse_time(str(value))
