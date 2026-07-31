from __future__ import annotations

import secrets
from dataclasses import dataclass, replace
from datetime import datetime, timedelta, timezone
from typing import Callable, Optional, Sequence, Tuple

from .journal import ClaimedJob, Journal, JournalLeaseError
from .model import (
    MemoryModel,
    ModelInvocationError,
    ModelOutputError,
    ModelRequest,
)
from .paths import PathValue, resolve_project_root
from .privacy import PrivacyFilterError, contains_known_secret
from .state import (
    DEFAULT_TOKEN_BUDGET,
    ProjectState,
    StateError,
    StateMetadata,
    fit_state_to_budget,
    load_state,
    publish_state,
    serialize_state,
)

DEFAULT_JOB_LEASE = timedelta(minutes=5)
DEFAULT_WORKER_LEASE = timedelta(minutes=10)
DEFAULT_RETRY_DELAYS = (timedelta(seconds=5), timedelta(seconds=30))


class CandidateStateError(ValueError):
    """Raised when a model candidate cannot safely replace current state."""


@dataclass(frozen=True)
class WorkerResult:
    claimed: int = 0
    updated: int = 0
    no_change: int = 0
    recovered: int = 0
    retry_scheduled: int = 0
    failed: int = 0
    already_running: bool = False

    def as_mapping(self) -> dict[str, object]:
        return {
            "claimed": self.claimed,
            "updated": self.updated,
            "no_change": self.no_change,
            "recovered": self.recovered,
            "retry_scheduled": self.retry_scheduled,
            "failed": self.failed,
            "already_running": self.already_running,
        }


class MemoryWorker:
    def __init__(
        self,
        journal: Journal,
        project_root: PathValue,
        model: MemoryModel,
        *,
        clock: Optional[Callable[[], datetime]] = None,
        token_budget: int = DEFAULT_TOKEN_BUDGET,
        max_attempts: int = 3,
        retry_delays: Sequence[timedelta] = DEFAULT_RETRY_DELAYS,
        job_lease: timedelta = DEFAULT_JOB_LEASE,
        worker_lease: timedelta = DEFAULT_WORKER_LEASE,
    ) -> None:
        if max_attempts <= 0:
            raise ValueError("max_attempts must be positive")
        if token_budget <= 0:
            raise ValueError("token_budget must be positive")
        normalized_delays = tuple(retry_delays)
        if not normalized_delays or any(
            delay <= timedelta(0) for delay in normalized_delays
        ):
            raise ValueError("retry delays must be positive")
        if job_lease <= timedelta(0) or worker_lease <= timedelta(0):
            raise ValueError("worker leases must be positive")
        self.journal = journal
        self.project_root = resolve_project_root(project_root)
        self.model = model
        self.clock = clock or (lambda: datetime.now(timezone.utc))
        self.token_budget = token_budget
        self.max_attempts = max_attempts
        self.retry_delays: Tuple[timedelta, ...] = normalized_delays
        self.job_lease = job_lease
        self.worker_lease = worker_lease

    def drain(self, *, max_jobs: Optional[int] = None) -> WorkerResult:
        if max_jobs is not None and (
            isinstance(max_jobs, bool)
            or not isinstance(max_jobs, int)
            or max_jobs <= 0
        ):
            raise ValueError("max_jobs must be a positive integer")
        worker_token = secrets.token_hex(24)
        now = self.clock()
        if not self.journal.acquire_worker_lease(
            worker_token,
            now,
            lease_duration=self.worker_lease,
        ):
            return WorkerResult(already_running=True)

        result = WorkerResult()
        try:
            while max_jobs is None or result.claimed < max_jobs:
                now = self.clock()
                if not self.journal.acquire_worker_lease(
                    worker_token,
                    now,
                    lease_duration=self.worker_lease,
                ):
                    break
                job = self.journal.claim_next(
                    now,
                    lease_duration=self.job_lease,
                )
                if job is None:
                    break
                result = replace(result, claimed=result.claimed + 1)
                try:
                    outcome = self._process(job)
                except ModelInvocationError:
                    result = self._record_failure(result, job, "model_error")
                except ModelOutputError:
                    result = self._record_failure(result, job, "invalid_output")
                except (CandidateStateError, PrivacyFilterError, StateError):
                    result = self._record_failure(
                        result,
                        job,
                        "state_validation",
                    )
                except OSError:
                    result = self._record_failure(result, job, "publish_error")
                else:
                    if outcome == "updated":
                        result = replace(result, updated=result.updated + 1)
                    elif outcome == "no_change":
                        result = replace(
                            result,
                            no_change=result.no_change + 1,
                        )
                    else:
                        result = replace(
                            result,
                            recovered=result.recovered + 1,
                        )
            return result
        finally:
            try:
                self.journal.release_worker_lease(worker_token, self.clock())
            except JournalLeaseError:
                pass

    def _process(self, job: ClaimedJob) -> str:
        current = load_state(self.project_root)
        if current.metadata.source_cursor == job.event_seq:
            completed_at = self.clock()
            self.journal.complete(
                job.event_id,
                job.lease_token,
                "updated",
                completed_at,
            )
            return "recovered"
        if current.metadata.source_cursor > job.event_seq:
            raise CandidateStateError("state cursor is ahead of the claimed job")

        decision = self.model.invoke(
            ModelRequest(
                event_seq=job.event_seq,
                event_id=job.event_id,
                redacted_prompt=job.prompt,
                previous_state=current,
                project_root=self.project_root,
                state_token_budget=self.token_budget,
            )
        )
        completed_at = self.clock()
        if decision.outcome == "no_change":
            self.journal.complete(
                job.event_id,
                job.lease_token,
                "no_change",
                completed_at,
            )
            return "no_change"
        if decision.state is None:
            raise ModelOutputError("updated model result has no state")
        candidate = self._prepare_candidate(decision.state, job, completed_at)
        publish_state(
            self.project_root,
            candidate,
            token_budget=self.token_budget,
        )
        self.journal.complete(
            job.event_id,
            job.lease_token,
            "updated",
            completed_at,
        )
        return "updated"

    def _prepare_candidate(
        self,
        candidate: ProjectState,
        job: ClaimedJob,
        completed_at: datetime,
    ) -> ProjectState:
        candidate.validate()
        if candidate.metadata.source_cursor != job.event_seq:
            raise CandidateStateError("candidate source cursor is stale")
        serialized = serialize_state(candidate)
        if contains_known_secret(serialized):
            raise CandidateStateError("candidate contains a defined secret pattern")
        retained_prompt = job.prompt.strip()
        if retained_prompt and any(
            retained_prompt in value for value in _state_text_values(candidate)
        ):
            raise CandidateStateError("candidate copied the retained prompt")
        reconciled = replace(
            candidate,
            metadata=StateMetadata(
                source_cursor=job.event_seq,
                updated_at=_format_state_time(completed_at),
            ),
        )
        return fit_state_to_budget(reconciled, self.token_budget)

    def _record_failure(
        self,
        result: WorkerResult,
        job: ClaimedJob,
        category: str,
    ) -> WorkerResult:
        failed_at = self.clock()
        retryable = job.attempt_count < self.max_attempts
        retry_at = None
        if retryable:
            delay_index = min(job.attempt_count - 1, len(self.retry_delays) - 1)
            retry_at = failed_at + self.retry_delays[delay_index]
        self.journal.retry(
            job.event_id,
            job.lease_token,
            category=category,
            failed_at=failed_at,
            retry_at=retry_at,
            retryable=retryable,
        )
        if retryable:
            return replace(
                result,
                retry_scheduled=result.retry_scheduled + 1,
            )
        return replace(result, failed=result.failed + 1)


def _format_state_time(value: datetime) -> str:
    if value.tzinfo is None:
        raise CandidateStateError("worker clock must be timezone-aware")
    return (
        value.astimezone(timezone.utc)
        .isoformat(timespec="microseconds")
        .replace("+00:00", "Z")
    )


def _state_text_values(state: ProjectState) -> Tuple[str, ...]:
    values = [state.project_summary, state.current_focus]
    for field_name in (
        "goals",
        "constraints",
        "decisions",
        "open_tasks",
        "blockers",
        "open_questions",
        "recent_verification",
        "next_actions",
    ):
        for entry in getattr(state, field_name):
            values.append(entry.statement)
            if entry.source is not None:
                values.append(entry.source)
            if entry.source_event_id is not None:
                values.append(entry.source_event_id)
    return tuple(values)
