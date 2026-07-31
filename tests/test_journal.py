from __future__ import annotations

import sqlite3
import tempfile
import threading
import unittest
from contextlib import closing
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from pathlib import Path

from context_compactor.journal import (
    EnqueueRequest,
    Journal,
    JournalConflictError,
    JournalLeaseError,
    digest_text,
)

NOW = datetime(2026, 7, 31, 4, 0, tzinfo=timezone.utc)


def request(
    index: int,
    *,
    prompt: str = "Record the current project goal.",
    enqueued_at: datetime = NOW,
) -> EnqueueRequest:
    return EnqueueRequest(
        event_id=f"event-{index:04d}",
        kind="user_prompt",
        occurred_at=enqueued_at,
        content_digest=digest_text(f"raw-event-{index}"),
        prompt=prompt,
        redaction_count=0,
        enqueued_at=enqueued_at,
    )


class JournalTests(unittest.TestCase):
    def test_concurrent_duplicate_enqueue_is_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with Journal.open(root):
                pass
            barrier = threading.Barrier(4)
            inserted = []
            failures = []
            lock = threading.Lock()

            def enqueue_once() -> None:
                try:
                    with Journal.open(root) as journal:
                        barrier.wait(timeout=10)
                        result = journal.enqueue(request(1))
                    with lock:
                        inserted.append(result.inserted)
                except BaseException as error:
                    with lock:
                        failures.append(error)

            threads = [threading.Thread(target=enqueue_once) for _ in range(4)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()

            self.assertEqual(failures, [])
            self.assertEqual(inserted.count(True), 1)
            self.assertEqual(inserted.count(False), 3)
            with Journal.open(root) as journal:
                self.assertEqual(len(journal.list_snapshots()), 1)

    def test_duplicate_identity_with_different_data_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                original = request(1)
                journal.enqueue(original)

                with self.assertRaises(JournalConflictError):
                    journal.enqueue(
                        replace(
                            original,
                            content_digest=digest_text("different"),
                        )
                    )

    def test_enqueue_transaction_rolls_back_when_job_insert_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                with closing(sqlite3.connect(str(journal.path))) as connection:
                    connection.execute(
                        """
CREATE TRIGGER synthetic_job_failure
BEFORE INSERT ON jobs
WHEN NEW.event_id = 'event-0001'
BEGIN
    SELECT RAISE(ABORT, 'synthetic job failure');
END
"""
                    )

                with self.assertRaises(sqlite3.DatabaseError):
                    journal.enqueue(request(1))

                with closing(sqlite3.connect(str(journal.path))) as connection:
                    count = connection.execute(
                        "SELECT COUNT(*) FROM events WHERE event_id = ?",
                        ("event-0001",),
                    ).fetchone()[0]
                self.assertEqual(count, 0)

    def test_claims_are_ordered_and_expired_lease_is_reclaimed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                journal.enqueue(request(1))
                journal.enqueue(request(2, enqueued_at=NOW + timedelta(seconds=1)))

                first = journal.claim_next(
                    NOW + timedelta(seconds=2),
                    lease_duration=timedelta(seconds=10),
                )
                blocked = journal.claim_next(
                    NOW + timedelta(seconds=3),
                    lease_duration=timedelta(seconds=10),
                )
                reclaimed = journal.claim_next(
                    NOW + timedelta(seconds=13),
                    lease_duration=timedelta(seconds=10),
                )

                self.assertIsNotNone(first)
                self.assertIsNone(blocked)
                self.assertIsNotNone(reclaimed)
                self.assertEqual(first.event_id, "event-0001")
                self.assertEqual(reclaimed.event_id, "event-0001")
                self.assertEqual(reclaimed.attempt_count, 2)
                self.assertNotEqual(first.lease_token, reclaimed.lease_token)
                with self.assertRaises(JournalLeaseError):
                    journal.complete(
                        first.event_id,
                        first.lease_token,
                        "updated",
                        NOW + timedelta(seconds=14),
                    )

                journal.complete(
                    reclaimed.event_id,
                    reclaimed.lease_token,
                    "updated",
                    NOW + timedelta(seconds=14),
                )
                second = journal.claim_next(
                    NOW + timedelta(seconds=15),
                    lease_duration=timedelta(seconds=10),
                )
                self.assertEqual(second.event_id, "event-0002")

    def test_completion_clears_prompt_for_updated_and_no_change(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                for index, outcome in ((1, "updated"), (2, "no_change")):
                    at = NOW + timedelta(seconds=index)
                    journal.enqueue(request(index, enqueued_at=at))
                    job = journal.claim_next(
                        at,
                        lease_duration=timedelta(minutes=1),
                    )
                    journal.complete(
                        job.event_id,
                        job.lease_token,
                        outcome,
                        at + timedelta(seconds=1),
                    )
                    snapshot = journal.job_snapshot(job.event_id)
                    self.assertEqual(snapshot.status, "completed")
                    self.assertEqual(snapshot.outcome, outcome)
                    self.assertFalse(snapshot.prompt_present)

                runtime = journal.runtime_snapshot(NOW + timedelta(minutes=1))
                self.assertEqual(runtime.source_cursor, 2)

    def test_cursor_waits_when_a_newer_job_expires_first(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                journal.enqueue(request(1))
                newer = request(2, enqueued_at=NOW + timedelta(seconds=1))
                journal.enqueue(
                    replace(
                        newer,
                        expires_at=NOW + timedelta(days=1),
                    )
                )

                journal.prune(NOW + timedelta(days=2))

                self.assertEqual(
                    journal.job_snapshot("event-0002").status,
                    "failed",
                )
                self.assertEqual(
                    journal.runtime_snapshot(NOW + timedelta(days=2)).source_cursor,
                    0,
                )
                older = journal.claim_next(
                    NOW + timedelta(days=2),
                    lease_duration=timedelta(minutes=1),
                )
                journal.complete(
                    older.event_id,
                    older.lease_token,
                    "updated",
                    NOW + timedelta(days=2, seconds=1),
                )
                self.assertEqual(
                    journal.runtime_snapshot(
                        NOW + timedelta(days=2, seconds=1)
                    ).source_cursor,
                    2,
                )

    def test_retry_backoff_and_terminal_failure_are_bounded(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                journal.enqueue(request(1))
                first = journal.claim_next(
                    NOW,
                    lease_duration=timedelta(minutes=1),
                )
                retry_at = NOW + timedelta(minutes=2)
                journal.retry(
                    first.event_id,
                    first.lease_token,
                    category="model_error",
                    failed_at=NOW + timedelta(seconds=1),
                    retry_at=retry_at,
                    retryable=True,
                )
                self.assertIsNone(
                    journal.claim_next(
                        NOW + timedelta(minutes=1),
                        lease_duration=timedelta(minutes=1),
                    )
                )
                second = journal.claim_next(
                    retry_at,
                    lease_duration=timedelta(minutes=1),
                )
                self.assertEqual(second.attempt_count, 2)
                journal.retry(
                    second.event_id,
                    second.lease_token,
                    category="invalid_output",
                    failed_at=retry_at + timedelta(seconds=1),
                    retry_at=None,
                    retryable=False,
                )

                snapshot = journal.job_snapshot(second.event_id)
                self.assertEqual(snapshot.status, "failed")
                self.assertEqual(snapshot.failure_category, "invalid_output")
                self.assertTrue(snapshot.prompt_present)
                self.assertEqual(
                    journal.runtime_snapshot(retry_at).source_cursor,
                    1,
                )

    def test_expiry_scrubs_prompt_and_capacity_keeps_500_newest_jobs(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                journal.enqueue(request(1))
                expiry_result = journal.prune(NOW + timedelta(days=7))
                expired = journal.job_snapshot("event-0001")
                self.assertEqual(expiry_result.prompts_scrubbed, 1)
                self.assertEqual(expired.status, "failed")
                self.assertEqual(expired.failure_category, "expired")
                self.assertFalse(expired.prompt_present)

                for index in range(2, 507):
                    at = NOW + timedelta(seconds=index)
                    journal.enqueue(request(index, enqueued_at=at))
                    job = journal.claim_next(
                        at,
                        lease_duration=timedelta(minutes=1),
                    )
                    journal.complete(
                        job.event_id,
                        job.lease_token,
                        "no_change",
                        at + timedelta(milliseconds=1),
                    )

                capacity_result = journal.prune(
                    NOW + timedelta(days=1),
                    max_jobs=500,
                )
                snapshots = journal.list_snapshots()
                self.assertEqual(capacity_result.jobs_deleted, 6)
                self.assertEqual(len(snapshots), 500)
                self.assertEqual(snapshots[0].event_id, "event-0007")

    def test_worker_lease_has_single_owner_and_can_be_reclaimed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with Journal.open(directory) as journal:
                self.assertTrue(
                    journal.acquire_worker_lease(
                        "worker-one",
                        NOW,
                        lease_duration=timedelta(seconds=10),
                    )
                )
                self.assertFalse(
                    journal.acquire_worker_lease(
                        "worker-two",
                        NOW + timedelta(seconds=1),
                        lease_duration=timedelta(seconds=10),
                    )
                )
                self.assertTrue(
                    journal.acquire_worker_lease(
                        "worker-two",
                        NOW + timedelta(seconds=11),
                        lease_duration=timedelta(seconds=10),
                    )
                )
                journal.release_worker_lease(
                    "worker-two",
                    NOW + timedelta(seconds=12),
                )
                snapshot = journal.runtime_snapshot(NOW + timedelta(seconds=12))
                self.assertFalse(snapshot.worker_lease_active)


if __name__ == "__main__":
    unittest.main()
