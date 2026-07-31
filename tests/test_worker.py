from __future__ import annotations

import io
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Callable, List, Union

from context_compactor.cli import main
from context_compactor.journal import EnqueueRequest, Journal, digest_text
from context_compactor.model import (
    CommandModel,
    ModelDecision,
    ModelInvocationError,
    ModelOutputError,
    ModelRequest,
)
from context_compactor.state import (
    ProjectState,
    StateEntry,
    StateMetadata,
    load_state,
    publish_state,
)
from context_compactor.worker import MemoryWorker

NOW = datetime(2026, 7, 31, 6, 0, tzinfo=timezone.utc)
DecisionSource = Union[
    ModelDecision,
    BaseException,
    Callable[[ModelRequest], ModelDecision],
]


@dataclass
class MutableClock:
    value: datetime

    def __call__(self) -> datetime:
        return self.value


class FakeModel:
    def __init__(self, decisions: List[DecisionSource]) -> None:
        self.decisions = decisions
        self.requests: List[ModelRequest] = []

    def invoke(self, request: ModelRequest) -> ModelDecision:
        self.requests.append(request)
        if not self.decisions:
            raise AssertionError("fake model received an unexpected call")
        decision = self.decisions.pop(0)
        if isinstance(decision, BaseException):
            raise decision
        if callable(decision):
            return decision(request)
        return decision


def enqueue(journal: Journal, index: int, at: datetime = NOW) -> None:
    prompt = f"Set the current project focus for event {index}."
    journal.enqueue(
        EnqueueRequest(
            event_id=f"event-{index:04d}",
            kind="user_prompt",
            occurred_at=at,
            content_digest=digest_text(f"raw-{index}"),
            prompt=prompt,
            redaction_count=0,
            enqueued_at=at,
        )
    )


def updated_decision(request: ModelRequest) -> ModelDecision:
    return ModelDecision(
        outcome="updated",
        state=ProjectState(
            current_focus=f"Task for {request.event_id}",
            goals=(StateEntry("Keep project memory readable"),),
            metadata=StateMetadata(
                source_cursor=request.event_seq,
                updated_at="2026-07-31T06:00:00Z",
            ),
        ),
    )


class WorkerTests(unittest.TestCase):
    def test_update_publishes_state_and_clears_prompt(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            model = FakeModel([updated_decision])
            with Journal.open(root) as journal:
                enqueue(journal, 1)
                result = MemoryWorker(
                    journal,
                    root,
                    model,
                    clock=MutableClock(NOW + timedelta(seconds=1)),
                ).drain()
                snapshot = journal.job_snapshot("event-0001")

            state = load_state(root)
            self.assertEqual(result.updated, 1)
            self.assertEqual(state.current_focus, "Task for event-0001")
            self.assertEqual(state.metadata.source_cursor, 1)
            self.assertEqual(snapshot.status, "completed")
            self.assertEqual(snapshot.outcome, "updated")
            self.assertFalse(snapshot.prompt_present)

    def test_no_change_preserves_state_bytes_and_is_completed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state_path = publish_state(
                root,
                ProjectState(current_focus="existing"),
            )
            before = state_path.read_bytes()
            with Journal.open(root) as journal:
                enqueue(journal, 1)
                result = MemoryWorker(
                    journal,
                    root,
                    FakeModel([ModelDecision(outcome="no_change")]),
                    clock=MutableClock(NOW + timedelta(seconds=1)),
                ).drain()
                snapshot = journal.job_snapshot("event-0001")

            self.assertEqual(result.no_change, 1)
            self.assertEqual(state_path.read_bytes(), before)
            self.assertEqual(snapshot.status, "completed")
            self.assertEqual(snapshot.outcome, "no_change")
            self.assertFalse(snapshot.prompt_present)

    def test_command_model_accepts_only_the_strict_result_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            request = ModelRequest(
                event_seq=1,
                event_id="event-0001",
                redacted_prompt="ordinary project instruction",
                previous_state=ProjectState.empty(),
                project_root=root,
                state_token_budget=2_000,
            )
            valid = CommandModel(
                (
                    sys.executable,
                    "-c",
                    "import json; print(json.dumps({'outcome':'no_change'}))",
                )
            )
            invalid = CommandModel(
                (
                    sys.executable,
                    "-c",
                    "import json; print(json.dumps("
                    "{'outcome':'no_change','extra':True}))",
                )
            )

            self.assertEqual(valid.invoke(request).outcome, "no_change")
            with self.assertRaises(ModelOutputError):
                invalid.invoke(request)

    def test_stale_cursor_is_terminal_and_last_valid_state_is_preserved(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state_path = publish_state(
                root,
                ProjectState(current_focus="last valid"),
            )
            before = state_path.read_bytes()
            stale = ModelDecision(
                outcome="updated",
                state=ProjectState(
                    current_focus="must not publish",
                    metadata=StateMetadata(source_cursor=0),
                ),
            )
            with Journal.open(root) as journal:
                enqueue(journal, 1)
                result = MemoryWorker(
                    journal,
                    root,
                    FakeModel([stale]),
                    clock=MutableClock(NOW + timedelta(seconds=1)),
                    max_attempts=1,
                ).drain()
                snapshot = journal.job_snapshot("event-0001")

            self.assertEqual(result.failed, 1)
            self.assertEqual(snapshot.status, "failed")
            self.assertEqual(snapshot.failure_category, "state_validation")
            self.assertEqual(state_path.read_bytes(), before)

    def test_candidate_cannot_copy_the_retained_prompt(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def copy_prompt(request: ModelRequest) -> ModelDecision:
                return ModelDecision(
                    outcome="updated",
                    state=ProjectState(
                        current_focus=request.redacted_prompt,
                        metadata=StateMetadata(
                            source_cursor=request.event_seq,
                        ),
                    ),
                )

            with Journal.open(root) as journal:
                enqueue(journal, 1)
                result = MemoryWorker(
                    journal,
                    root,
                    FakeModel([copy_prompt]),
                    clock=MutableClock(NOW + timedelta(seconds=1)),
                    max_attempts=1,
                ).drain()
                snapshot = journal.job_snapshot("event-0001")

            self.assertEqual(result.failed, 1)
            self.assertEqual(snapshot.failure_category, "state_validation")
            self.assertFalse(
                (root / ".context-compactor" / "state.yaml").exists()
            )

    def test_retry_then_update_succeeds_after_backoff(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            clock = MutableClock(NOW)
            model = FakeModel(
                [
                    ModelInvocationError("synthetic failure"),
                    updated_decision,
                ]
            )
            with Journal.open(root) as journal:
                enqueue(journal, 1)
                worker = MemoryWorker(
                    journal,
                    root,
                    model,
                    clock=clock,
                    retry_delays=(timedelta(seconds=5),),
                )
                first = worker.drain()
                clock.value += timedelta(seconds=6)
                second = worker.drain()
                snapshot = journal.job_snapshot("event-0001")

            self.assertEqual(first.retry_scheduled, 1)
            self.assertEqual(second.updated, 1)
            self.assertEqual(snapshot.attempt_count, 2)
            self.assertFalse(snapshot.prompt_present)

    def test_exhausted_invalid_output_keeps_last_valid_state(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state_path = publish_state(
                root,
                ProjectState(current_focus="last valid"),
            )
            before = state_path.read_bytes()
            clock = MutableClock(NOW)
            model = FakeModel(
                [
                    ModelOutputError("synthetic invalid output"),
                    ModelOutputError("synthetic invalid output"),
                ]
            )
            with Journal.open(root) as journal:
                enqueue(journal, 1)
                worker = MemoryWorker(
                    journal,
                    root,
                    model,
                    clock=clock,
                    max_attempts=2,
                    retry_delays=(timedelta(seconds=5),),
                )
                worker.drain()
                clock.value += timedelta(seconds=6)
                result = worker.drain()
                snapshot = journal.job_snapshot("event-0001")

            self.assertEqual(result.failed, 1)
            self.assertEqual(snapshot.status, "failed")
            self.assertEqual(snapshot.failure_category, "invalid_output")
            self.assertTrue(snapshot.prompt_present)
            self.assertEqual(state_path.read_bytes(), before)

    def test_crash_after_publish_is_recovered_without_model_call(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with Journal.open(root) as journal:
                enqueue(journal, 1)
                claimed = journal.claim_next(
                    NOW,
                    lease_duration=timedelta(seconds=1),
                )
                publish_state(
                    root,
                    ProjectState(
                        current_focus="already published",
                        metadata=StateMetadata(
                            source_cursor=claimed.event_seq,
                            updated_at="2026-07-31T06:00:00Z",
                        ),
                    ),
                )
                model = FakeModel([])
                result = MemoryWorker(
                    journal,
                    root,
                    model,
                    clock=MutableClock(NOW + timedelta(seconds=2)),
                ).drain()
                snapshot = journal.job_snapshot("event-0001")

            self.assertEqual(result.recovered, 1)
            self.assertEqual(model.requests, [])
            self.assertEqual(snapshot.status, "completed")
            self.assertEqual(snapshot.outcome, "updated")
            self.assertFalse(snapshot.prompt_present)

    def test_one_drain_consumes_all_claimable_jobs_in_order(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            decisions = [ModelDecision(outcome="no_change") for _ in range(3)]
            model = FakeModel(decisions)
            with Journal.open(root) as journal:
                for index in range(1, 4):
                    enqueue(journal, index, NOW + timedelta(seconds=index))
                result = MemoryWorker(
                    journal,
                    root,
                    model,
                    clock=MutableClock(NOW + timedelta(seconds=10)),
                ).drain()
                snapshots = journal.list_snapshots()

            self.assertEqual(result.claimed, 3)
            self.assertEqual(result.no_change, 3)
            self.assertEqual(
                [request.event_id for request in model.requests],
                ["event-0001", "event-0002", "event-0003"],
            )
            self.assertTrue(
                all(
                    snapshot.status == "completed"
                    and not snapshot.prompt_present
                    for snapshot in snapshots
                )
            )

    def test_direct_worker_cli_runs_the_structured_model_command(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            at = datetime.now(timezone.utc)
            with Journal.open(root) as journal:
                enqueue(journal, 1, at)
            output = io.StringIO()

            with redirect_stdout(output):
                exit_code = main(
                    (
                        "worker",
                        "run",
                        "--project-root",
                        str(root),
                        "--model-command",
                        sys.executable,
                        "-c",
                        "import json; print(json.dumps("
                        "{'outcome':'no_change'}))",
                    )
                )

            with Journal.open(root) as journal:
                snapshot = journal.job_snapshot("event-0001")
            self.assertEqual(exit_code, 0)
            self.assertIn('"no_change":1', output.getvalue())
            self.assertEqual(snapshot.status, "completed")
            self.assertFalse(snapshot.prompt_present)


if __name__ == "__main__":
    unittest.main()
