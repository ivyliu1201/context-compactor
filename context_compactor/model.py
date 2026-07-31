from __future__ import annotations

import json
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Mapping, Optional, Protocol, Sequence, Tuple

from .state import ProjectState, StateError

MODEL_PROTOCOL = "context-compactor/model/v1"
MAX_MODEL_OUTPUT_BYTES = 1_000_000


class ModelError(RuntimeError):
    """Base class for model boundary failures."""


class ModelInvocationError(ModelError):
    """Raised when the configured command does not complete successfully."""


class ModelOutputError(ModelError):
    """Raised when model output violates the strict result contract."""


@dataclass(frozen=True)
class ModelRequest:
    event_seq: int
    event_id: str
    redacted_prompt: str
    previous_state: ProjectState
    project_root: Path
    state_token_budget: int

    def as_mapping(self) -> dict[str, object]:
        return {
            "protocol": MODEL_PROTOCOL,
            "event": {
                "sequence": self.event_seq,
                "id": self.event_id,
            },
            "redacted_prompt": self.redacted_prompt,
            "previous_state": self.previous_state.as_mapping(),
            "state_token_budget": self.state_token_budget,
            "instructions": (
                "Return exactly one JSON object: "
                '{"outcome":"no_change"} or '
                '{"outcome":"updated","state":{...complete state schema...}}. '
                "Do not copy the raw prompt, transcript, logs, tool output, "
                "reasoning, or credentials into state."
            ),
        }


@dataclass(frozen=True)
class ModelDecision:
    outcome: str
    state: Optional[ProjectState] = None

    @classmethod
    def from_mapping(cls, value: object) -> "ModelDecision":
        if not isinstance(value, Mapping):
            raise ModelOutputError("model result must be a JSON object")
        outcome = value.get("outcome")
        if outcome == "no_change":
            if set(value) != {"outcome"}:
                raise ModelOutputError("no_change result has unknown fields")
            return cls(outcome="no_change")
        if outcome == "updated":
            if set(value) != {"outcome", "state"}:
                raise ModelOutputError("updated result fields are invalid")
            try:
                state = ProjectState.from_mapping(value["state"])
            except (KeyError, StateError, TypeError) as error:
                raise ModelOutputError("updated state is invalid") from error
            return cls(outcome="updated", state=state)
        raise ModelOutputError("model outcome is invalid")


class MemoryModel(Protocol):
    def invoke(self, request: ModelRequest) -> ModelDecision:
        ...


class CommandModel:
    def __init__(
        self,
        command: Sequence[str],
        *,
        timeout_seconds: float = 120.0,
    ) -> None:
        normalized = tuple(str(argument) for argument in command)
        if not normalized or not normalized[0].strip():
            raise ModelInvocationError("model command is empty")
        if timeout_seconds <= 0:
            raise ModelInvocationError("model timeout must be positive")
        self.command: Tuple[str, ...] = normalized
        self.timeout_seconds = timeout_seconds

    def invoke(self, request: ModelRequest) -> ModelDecision:
        encoded = json.dumps(
            request.as_mapping(),
            ensure_ascii=False,
            separators=(",", ":"),
        ).encode("utf-8")
        try:
            completed = subprocess.run(
                self.command,
                cwd=str(request.project_root),
                input=encoded,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                timeout=self.timeout_seconds,
                check=False,
                shell=False,
            )
        except (OSError, subprocess.SubprocessError) as error:
            raise ModelInvocationError("model command did not complete") from error
        if completed.returncode != 0:
            raise ModelInvocationError("model command returned a failure status")
        if len(completed.stdout) > MAX_MODEL_OUTPUT_BYTES:
            raise ModelOutputError("model output exceeds the allowed size")
        try:
            decoded = completed.stdout.decode("utf-8", errors="strict")
            value = json.loads(decoded)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise ModelOutputError("model output is not valid JSON") from error
        return ModelDecision.from_mapping(value)
