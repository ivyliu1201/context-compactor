from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
import sqlite3
import tempfile
import unittest
from pathlib import Path
from typing import Optional
from unittest import mock

from context_compactor.cli import main
from context_compactor.migration import (
    MigrationError,
    apply_legacy_migration,
    preview_legacy_migration,
)
from context_compactor.state import ProjectState, load_state, publish_state

SCHEMA_CHECKSUMS = {
    1: "2f2f91d61cba529eb3b7911c02914697626a1c2c526bbfce15c8e3c882d77254",
    2: "5cb66cdd121f9322f9c893444f4f3f15ff948f0de0d7e00e1cf1c462cb8500cb",
    3: "a593c764e9b8daa3e0ef94f8a663e2b5611b3b3b1665929047dc8a147ca549eb",
    4: "747d855239ee39e6c1411138623a5cfd22a4390acc70c89c77b043ff761e8c22",
    5: "c46e23419fca9741427f0a0de688857ed2aba15840d17d0dbdbb3ada1b6d11fe",
}
SYNTHETIC_TOKEN = "SYNTHETIC_TOKEN_123456"
SYNTHETIC_EVENT_SECRET = "sk-SYNTHETIC123456"


def _digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _create_legacy_database(root: Path, *, latest_schema: int = 5) -> Path:
    data_dir = root / ".context-compactor"
    data_dir.mkdir()
    database = data_dir / "context.db"
    with contextlib.closing(sqlite3.connect(database)) as connection, connection:
        connection.executescript(
            """
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
);
CREATE TABLE memory_records (
    record_id TEXT PRIMARY KEY,
    conflict_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    canonical_value TEXT NOT NULL,
    priority TEXT NOT NULL,
    lifecycle_status TEXT NOT NULL,
    record_json TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    source_operation_id TEXT NOT NULL UNIQUE,
    source_operation_seq INTEGER NOT NULL UNIQUE,
    terminal_operation_id TEXT,
    superseded_by_record_id TEXT,
    duplicate_of_record_id TEXT
);
CREATE TABLE memory_view_state (
    singleton INTEGER PRIMARY KEY,
    last_event_seq INTEGER NOT NULL,
    last_operation_seq INTEGER NOT NULL,
    view_digest TEXT NOT NULL,
    record_count INTEGER NOT NULL,
    contradiction_count INTEGER NOT NULL
);
"""
        )
        for version in range(1, min(latest_schema, 5) + 1):
            connection.execute(
                """
INSERT INTO schema_migrations(version, checksum, applied_at)
VALUES (?, ?, ?)
""",
                (
                    version,
                    SCHEMA_CHECKSUMS[version],
                    "2026-07-31T00:00:00Z",
                ),
            )
        if latest_schema > 5:
            connection.execute(
                """
INSERT INTO schema_migrations(version, checksum, applied_at)
VALUES (?, ?, ?)
""",
                (
                    latest_schema,
                    "f" * 64,
                    "2026-07-31T00:00:00Z",
                ),
            )
    return database


def _insert_record(
    database: Path,
    *,
    sequence: int,
    kind: str,
    value: str,
    lifecycle: str = "active",
    artifact: str = "SPEC.md",
    source_event_id: Optional[str] = None,
    record_json_changes: Optional[dict[str, object]] = None,
) -> None:
    record_id = f"legacy-record-{sequence}"
    event_id = source_event_id or f"legacy-event-{sequence}"
    record = {
        "id": record_id,
        "conflict_key": f"legacy.{kind}.{sequence}",
        "kind": kind,
        "value": value,
        "priority": "normal",
        "confidence": "explicit",
        "status": "active",
        "source": {
            "event_id": event_id,
            "evidence": f"synthetic evidence {sequence}",
            "artifact": artifact,
        },
        "created_at": "2026-07-31T00:00:00Z",
    }
    if record_json_changes:
        record.update(record_json_changes)
    terminal = f"legacy-terminal-{sequence}" if lifecycle != "active" else None
    with contextlib.closing(sqlite3.connect(database)) as connection, connection:
        connection.execute(
            """
INSERT INTO memory_records (
    record_id, conflict_key, kind, canonical_value, priority,
    lifecycle_status, record_json, source_event_id, source_operation_id,
    source_operation_seq, terminal_operation_id, superseded_by_record_id,
    duplicate_of_record_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
""",
            (
                record_id,
                f"legacy.{kind}.{sequence}",
                kind,
                " ".join(value.split()).lower(),
                "normal",
                lifecycle,
                json.dumps(record, separators=(",", ":")),
                event_id,
                f"legacy-operation-{sequence}",
                sequence,
                terminal,
                None,
                None,
            ),
        )


def _write_view_state(database: Path) -> None:
    with contextlib.closing(sqlite3.connect(database)) as connection, connection:
        count, cursor = connection.execute(
            """
SELECT COUNT(*), COALESCE(MAX(source_operation_seq), 0)
FROM memory_records
"""
        ).fetchone()
        connection.execute(
            """
INSERT INTO memory_view_state (
    singleton, last_event_seq, last_operation_seq, view_digest,
    record_count, contradiction_count
) VALUES (1, ?, ?, ?, ?, 0)
""",
            (cursor, cursor, "0" * 64, count),
        )


class MigrationTests(unittest.TestCase):
    def test_populated_migration_maps_supported_active_records_privately(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root)
            records = (
                ("goal", "Ship the migration", "SPEC.md"),
                ("acceptance_criterion", "Keep the old DB", "TODO.md"),
                ("constraint", "Do not persist prompts", "SPEC.md"),
                (
                    "decision",
                    f"Bearer {SYNTHETIC_TOKEN}",
                    "docs/adr/0003-readable-state-python-runtime.md",
                ),
                ("task", "Run migration tests", "tests/test_migration.py"),
                ("blocker", "Unknown schema blocks apply", "SPEC.md"),
                ("question", "Is the source unchanged?", "C:/absolute/path"),
                ("test_result", "Focused migration test passed", "TODO.md"),
                ("file", "legacy-only.txt", "legacy-only.txt"),
            )
            for sequence, (kind, value, artifact) in enumerate(records, start=1):
                _insert_record(
                    database,
                    sequence=sequence,
                    kind=kind,
                    value=value,
                    artifact=artifact,
                    source_event_id=(
                        SYNTHETIC_EVENT_SECRET
                        if kind == "test_result"
                        else None
                    ),
                )
            _insert_record(
                database,
                sequence=10,
                kind="task",
                value="Resolved legacy task",
                lifecycle="resolved",
            )
            _write_view_state(database)
            before = _digest(database)

            preview = preview_legacy_migration(root)

            self.assertTrue(preview["can_apply"])
            self.assertEqual(preview["total_records"], 10)
            self.assertEqual(preview["active_records"], 9)
            self.assertEqual(preview["migrated_records"], 8)
            self.assertEqual(preview["unsupported_records"], 1)
            self.assertEqual(preview["inactive_records"], 1)
            self.assertEqual(preview["redaction_count"], 1)
            self.assertEqual(preview["omitted_source_references"], 2)
            self.assertEqual(preview["legacy_operation_cursor"], 10)
            self.assertEqual(preview["target_counts"]["goals"], 2)
            self.assertEqual(preview["target_counts"]["next_actions"], 0)
            self.assertFalse(
                (root / ".context-compactor" / "state.yaml").exists()
            )

            report = apply_legacy_migration(root)
            state = load_state(root)
            serialized = (root / ".context-compactor" / "state.yaml").read_text(
                encoding="utf-8"
            )

            self.assertTrue(report["applied"])
            self.assertTrue(report["source_unchanged"])
            self.assertEqual(state.metadata.source_cursor, 0)
            self.assertEqual(
                [entry.statement for entry in state.goals],
                ["Ship the migration", "Keep the old DB"],
            )
            self.assertEqual(
                [entry.statement for entry in state.open_tasks],
                ["Run migration tests"],
            )
            self.assertEqual(len(state.recent_verification), 1)
            self.assertIsNone(
                state.recent_verification[0].source_event_id
            )
            self.assertIsNone(state.open_questions[0].source)
            self.assertIn("[REDACTED]", state.decisions[0].statement)
            self.assertNotIn(SYNTHETIC_TOKEN, serialized)
            self.assertNotIn(SYNTHETIC_EVENT_SECRET, serialized)
            self.assertNotIn("synthetic evidence", serialized)
            self.assertIn("recent_verification:", serialized)
            self.assertEqual(_digest(database), before)

    def test_empty_database_produces_valid_empty_state(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root)
            before = _digest(database)

            report = apply_legacy_migration(root)

            self.assertTrue(report["applied"])
            self.assertEqual(report["migrated_records"], 0)
            self.assertEqual(load_state(root), ProjectState.empty())
            self.assertEqual(_digest(database), before)

    def test_repeated_migration_is_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root)
            _insert_record(
                database,
                sequence=1,
                kind="goal",
                value="Migrate once",
            )
            _write_view_state(database)
            first = apply_legacy_migration(root)
            state_path = root / ".context-compactor" / "state.yaml"
            before = state_path.read_bytes()
            before_mtime = state_path.stat().st_mtime_ns

            second = apply_legacy_migration(root)

            self.assertTrue(first["applied"])
            self.assertFalse(second["applied"])
            self.assertEqual(second["status"], "already_migrated")
            self.assertEqual(state_path.read_bytes(), before)
            self.assertEqual(state_path.stat().st_mtime_ns, before_mtime)

    def test_unknown_schema_stops_without_target_state(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root, latest_schema=6)
            before = _digest(database)

            with self.assertRaisesRegex(MigrationError, "unsupported"):
                preview_legacy_migration(root)
            with self.assertRaisesRegex(MigrationError, "unsupported"):
                apply_legacy_migration(root)

            self.assertFalse(
                (root / ".context-compactor" / "state.yaml").exists()
            )
            self.assertEqual(_digest(database), before)

    def test_invalid_active_record_is_reported_and_not_partially_migrated(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root)
            _insert_record(
                database,
                sequence=1,
                kind="goal",
                value="Valid goal",
            )
            _insert_record(
                database,
                sequence=2,
                kind="task",
                value="Invalid task",
                record_json_changes={"id": "different-record-id"},
            )
            _write_view_state(database)
            before = _digest(database)

            preview = preview_legacy_migration(root)

            self.assertFalse(preview["can_apply"])
            self.assertEqual(preview["migrated_records"], 1)
            self.assertEqual(preview["invalid_records"], 1)
            with self.assertRaisesRegex(MigrationError, "invalid_records_present"):
                apply_legacy_migration(root)
            self.assertFalse(
                (root / ".context-compactor" / "state.yaml").exists()
            )
            self.assertEqual(_digest(database), before)

    def test_interrupted_publication_keeps_existing_state_and_source(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root)
            _insert_record(
                database,
                sequence=1,
                kind="goal",
                value="Candidate goal",
            )
            _write_view_state(database)
            state_path = publish_state(root, ProjectState.empty())
            state_before = state_path.read_bytes()
            database_before = _digest(database)
            real_replace = os.replace

            def fail_state_replace(source: object, destination: object) -> None:
                if Path(destination) == state_path:
                    raise OSError("synthetic replace interruption")
                real_replace(source, destination)

            with mock.patch(
                "context_compactor.state.os.replace",
                side_effect=fail_state_replace,
            ):
                with self.assertRaisesRegex(MigrationError, "publication failed"):
                    apply_legacy_migration(root)

            self.assertEqual(state_path.read_bytes(), state_before)
            self.assertEqual(load_state(root), ProjectState.empty())
            self.assertEqual(_digest(database), database_before)

    def test_existing_nonempty_state_is_not_overwritten(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root)
            _insert_record(
                database,
                sequence=1,
                kind="goal",
                value="Legacy goal",
            )
            _write_view_state(database)
            state_path = publish_state(
                root,
                ProjectState(current_focus="Current Python state"),
            )
            before = state_path.read_bytes()

            preview = preview_legacy_migration(root)

            self.assertFalse(preview["can_apply"])
            self.assertEqual(preview["target_state"], "conflict")
            with self.assertRaisesRegex(MigrationError, "existing_state_conflict"):
                apply_legacy_migration(root)
            self.assertEqual(state_path.read_bytes(), before)

    def test_cli_preview_emits_counts_without_record_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = _create_legacy_database(root)
            _insert_record(
                database,
                sequence=1,
                kind="goal",
                value="Content must stay out of reports",
            )
            _write_view_state(database)
            stdout = io.StringIO()
            stderr = io.StringIO()

            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(
                stderr
            ):
                exit_code = main(
                    (
                        "migrate",
                        "preview",
                        "--project-root",
                        str(root),
                    )
                )

            report = json.loads(stdout.getvalue())
            self.assertEqual(exit_code, 0)
            self.assertEqual(stderr.getvalue(), "")
            self.assertEqual(report["migrated_records"], 1)
            self.assertNotIn("Content must stay out of reports", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
