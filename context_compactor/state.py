from __future__ import annotations

import json
import math
import os
import re
import tempfile
from dataclasses import dataclass, field, replace
from datetime import datetime
from pathlib import Path, PurePosixPath
from typing import Mapping, Optional

from .paths import PathValue, project_paths

SCHEMA_VERSION = 1
DEFAULT_TOKEN_BUDGET = 2_000
STATE_LIST_FIELDS = (
    "goals",
    "constraints",
    "decisions",
    "open_tasks",
    "blockers",
    "open_questions",
    "recent_verification",
    "next_actions",
)
_TOP_LEVEL_FIELDS = {
    "schema_version",
    "project_summary",
    "current_focus",
    *STATE_LIST_FIELDS,
    "metadata",
}
_ENTRY_FIELDS = {"statement", "source", "source_event_id"}
_METADATA_FIELDS = {"source_cursor", "updated_at"}
_KEY_VALUE = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*):(.*)$")


class StateError(ValueError):
    """Base class for state format and validation failures."""


class StateFormatError(StateError):
    """Raised when state.yaml is not in the supported YAML subset."""


class StateValidationError(StateError):
    """Raised when parsed state violates the versioned schema."""


class StateTooLargeError(StateValidationError):
    """Raised when a state cannot fit without dropping mandatory content."""

    def __init__(self, actual_tokens: int, token_budget: int) -> None:
        super().__init__(
            f"state requires {actual_tokens} estimated tokens; budget is {token_budget}"
        )
        self.actual_tokens = actual_tokens
        self.token_budget = token_budget


@dataclass(frozen=True)
class StateEntry:
    statement: str
    source: Optional[str] = None
    source_event_id: Optional[str] = None

    @classmethod
    def from_value(cls, value: object) -> "StateEntry":
        if isinstance(value, str):
            entry = cls(statement=value)
        elif isinstance(value, Mapping):
            unknown = set(value) - _ENTRY_FIELDS
            if unknown:
                raise StateValidationError(
                    f"unknown state entry fields: {', '.join(sorted(unknown))}"
                )
            entry = cls(
                statement=_required_string(value.get("statement"), "statement"),
                source=_optional_string(value.get("source"), "source"),
                source_event_id=_optional_string(
                    value.get("source_event_id"), "source_event_id"
                ),
            )
        else:
            raise StateValidationError("state entries must be strings or mappings")
        entry.validate()
        return entry

    def validate(self) -> None:
        if not self.statement.strip():
            raise StateValidationError("state entry statement is empty")
        _reject_control_characters(self.statement, "state entry statement")
        if self.source is not None:
            _validate_relative_source(self.source)
        if self.source_event_id is not None:
            if not self.source_event_id.strip():
                raise StateValidationError("source_event_id is empty")
            _reject_control_characters(self.source_event_id, "source_event_id")

    def as_mapping(self) -> dict[str, object]:
        value: dict[str, object] = {"statement": self.statement}
        if self.source is not None:
            value["source"] = self.source
        if self.source_event_id is not None:
            value["source_event_id"] = self.source_event_id
        return value


@dataclass(frozen=True)
class StateMetadata:
    source_cursor: int = 0
    updated_at: str = ""

    @classmethod
    def from_mapping(cls, value: object) -> "StateMetadata":
        if not isinstance(value, Mapping):
            raise StateValidationError("metadata must be a mapping")
        unknown = set(value) - _METADATA_FIELDS
        if unknown:
            raise StateValidationError(
                f"unknown metadata fields: {', '.join(sorted(unknown))}"
            )
        cursor = value.get("source_cursor", 0)
        if isinstance(cursor, bool) or not isinstance(cursor, int) or cursor < 0:
            raise StateValidationError("metadata.source_cursor must be a non-negative integer")
        updated_at = _required_string(value.get("updated_at", ""), "updated_at")
        metadata = cls(source_cursor=cursor, updated_at=updated_at)
        metadata.validate()
        return metadata

    def validate(self) -> None:
        if self.source_cursor < 0:
            raise StateValidationError("metadata.source_cursor must be non-negative")
        if self.updated_at:
            try:
                datetime.fromisoformat(self.updated_at.replace("Z", "+00:00"))
            except ValueError as error:
                raise StateValidationError(
                    "metadata.updated_at must be an ISO-8601 timestamp"
                ) from error


@dataclass(frozen=True)
class ProjectState:
    schema_version: int = SCHEMA_VERSION
    project_summary: str = ""
    current_focus: str = ""
    goals: tuple[StateEntry, ...] = field(default_factory=tuple)
    constraints: tuple[StateEntry, ...] = field(default_factory=tuple)
    decisions: tuple[StateEntry, ...] = field(default_factory=tuple)
    open_tasks: tuple[StateEntry, ...] = field(default_factory=tuple)
    blockers: tuple[StateEntry, ...] = field(default_factory=tuple)
    open_questions: tuple[StateEntry, ...] = field(default_factory=tuple)
    recent_verification: tuple[StateEntry, ...] = field(default_factory=tuple)
    next_actions: tuple[StateEntry, ...] = field(default_factory=tuple)
    metadata: StateMetadata = field(default_factory=StateMetadata)

    @classmethod
    def empty(cls) -> "ProjectState":
        return cls()

    @classmethod
    def from_mapping(cls, value: object) -> "ProjectState":
        if not isinstance(value, Mapping):
            raise StateValidationError("state document must be a mapping")
        unknown = set(value) - _TOP_LEVEL_FIELDS
        if unknown:
            raise StateValidationError(
                f"unknown state fields: {', '.join(sorted(unknown))}"
            )
        missing = _TOP_LEVEL_FIELDS - set(value)
        if missing:
            raise StateValidationError(
                f"missing state fields: {', '.join(sorted(missing))}"
            )
        state = cls(
            schema_version=value["schema_version"],
            project_summary=_required_string(
                value["project_summary"], "project_summary"
            ),
            current_focus=_required_string(value["current_focus"], "current_focus"),
            **{
                name: _entries(value[name], name)
                for name in STATE_LIST_FIELDS
            },
            metadata=StateMetadata.from_mapping(value["metadata"]),
        )
        state.validate()
        return state

    def validate(self) -> None:
        if isinstance(self.schema_version, bool) or self.schema_version != SCHEMA_VERSION:
            raise StateValidationError(
                f"schema_version must be {SCHEMA_VERSION}"
            )
        _reject_control_characters(self.project_summary, "project_summary")
        _reject_control_characters(self.current_focus, "current_focus")
        for name in STATE_LIST_FIELDS:
            entries = getattr(self, name)
            if not isinstance(entries, tuple):
                raise StateValidationError(f"{name} must be a tuple of StateEntry")
            for entry in entries:
                if not isinstance(entry, StateEntry):
                    raise StateValidationError(f"{name} contains an invalid entry")
                entry.validate()
        self.metadata.validate()

    def as_mapping(self) -> dict[str, object]:
        self.validate()
        result: dict[str, object] = {
            "schema_version": self.schema_version,
            "project_summary": self.project_summary,
            "current_focus": self.current_focus,
        }
        for name in STATE_LIST_FIELDS:
            result[name] = [entry.as_mapping() for entry in getattr(self, name)]
        result["metadata"] = {
            "source_cursor": self.metadata.source_cursor,
            "updated_at": self.metadata.updated_at,
        }
        return result


def decode_state(text: str) -> ProjectState:
    if not isinstance(text, str):
        raise StateFormatError("state document must be text")
    return ProjectState.from_mapping(_parse_yaml(text))


def serialize_state(state: ProjectState) -> str:
    value = state.as_mapping()
    lines = [
        f"schema_version: {value['schema_version']}",
        f"project_summary: {_quote(value['project_summary'])}",
        f"current_focus: {_quote(value['current_focus'])}",
    ]
    for name in STATE_LIST_FIELDS:
        entries = value[name]
        if not entries:
            lines.append(f"{name}: []")
            continue
        lines.append(f"{name}:")
        for entry in entries:
            lines.append(f"  - statement: {_quote(entry['statement'])}")
            if "source" in entry:
                lines.append(f"    source: {_quote(entry['source'])}")
            if "source_event_id" in entry:
                lines.append(
                    f"    source_event_id: {_quote(entry['source_event_id'])}"
                )
    metadata = value["metadata"]
    lines.extend(
        (
            "metadata:",
            f"  source_cursor: {metadata['source_cursor']}",
            f"  updated_at: {_quote(metadata['updated_at'])}",
        )
    )
    return "\n".join(lines) + "\n"


def estimate_input_tokens(text: str) -> int:
    weighted_characters = sum(1.0 if ord(character) > 127 else 0.25 for character in text)
    return math.ceil(weighted_characters)


def enforce_token_budget(
    state: ProjectState,
    token_budget: int = DEFAULT_TOKEN_BUDGET,
) -> None:
    if token_budget <= 0:
        raise StateValidationError("token budget must be positive")
    actual = estimate_input_tokens(serialize_state(state))
    if actual > token_budget:
        raise StateTooLargeError(actual, token_budget)


def fit_state_to_budget(
    state: ProjectState,
    token_budget: int = DEFAULT_TOKEN_BUDGET,
) -> ProjectState:
    state.validate()
    candidate = state
    if estimate_input_tokens(serialize_state(candidate)) <= token_budget:
        return candidate

    candidate = replace(candidate, project_summary="")
    trim_order = (
        "open_questions",
        "recent_verification",
        "decisions",
        "open_tasks",
        "next_actions",
        "blockers",
    )
    for name in trim_order:
        entries = getattr(candidate, name)
        while entries:
            entries = entries[:-1]
            candidate = replace(candidate, **{name: entries})
            if estimate_input_tokens(serialize_state(candidate)) <= token_budget:
                return candidate

    enforce_token_budget(candidate, token_budget)
    return candidate


def load_state(
    project_root: PathValue,
) -> ProjectState:
    path = project_paths(project_root).state
    if not path.exists():
        return ProjectState.empty()
    return load_state_file(path)


def load_state_file(path: PathValue) -> ProjectState:
    state_path = Path(path)
    try:
        text = state_path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as error:
        raise StateFormatError("state file is unavailable") from error
    return decode_state(text)


def publish_state(
    project_root: PathValue,
    state: ProjectState,
    *,
    token_budget: int = DEFAULT_TOKEN_BUDGET,
) -> Path:
    paths = project_paths(project_root)
    state.validate()
    serialized = serialize_state(state)
    actual = estimate_input_tokens(serialized)
    if actual > token_budget:
        raise StateTooLargeError(actual, token_budget)

    previous: Optional[bytes] = None
    if paths.state.exists():
        load_state_file(paths.state)
        previous = paths.state.read_bytes()

    paths.data_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    state_temp = _write_temp(paths.data_dir, "state-", serialized.encode("utf-8"))
    backup_temp: Optional[Path] = None
    try:
        load_state_file(state_temp)
        if previous is not None:
            backup_temp = _write_temp(paths.data_dir, "backup-", previous)
            load_state_file(backup_temp)
            os.replace(backup_temp, paths.state_backup)
            backup_temp = None
        os.replace(state_temp, paths.state)
        state_temp = None
        return paths.state
    finally:
        for temporary in (state_temp, backup_temp):
            if temporary is not None:
                temporary.unlink(missing_ok=True)


def _write_temp(directory: Path, prefix: str, content: bytes) -> Path:
    descriptor, name = tempfile.mkstemp(prefix=prefix, suffix=".tmp", dir=directory)
    path = Path(name)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(path, 0o600)
        return path
    except BaseException:
        path.unlink(missing_ok=True)
        raise


def _parse_yaml(text: str) -> dict[str, object]:
    lines = [
        (number, line)
        for number, line in enumerate(text.splitlines(), start=1)
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if not lines:
        raise StateFormatError("state document is empty")
    if any("\t" in line for _, line in lines):
        raise StateFormatError("tabs are not supported")

    result: dict[str, object] = {}
    index = 0
    while index < len(lines):
        number, line = lines[index]
        if line.startswith(" "):
            raise StateFormatError(f"unexpected indentation on line {number}")
        key, raw = _split_key_value(line, number)
        if key in result:
            raise StateFormatError(f"duplicate key {key!r} on line {number}")
        if key not in _TOP_LEVEL_FIELDS:
            raise StateValidationError(f"unknown state fields: {key}")
        raw = raw.strip()
        if key in STATE_LIST_FIELDS:
            if raw == "[]":
                result[key] = []
                index += 1
                continue
            if raw:
                raise StateFormatError(f"{key} must be a block list or []")
            entries, index = _parse_entries(lines, index + 1)
            result[key] = entries
            continue
        if key == "metadata":
            if raw:
                raise StateFormatError("metadata must be a block mapping")
            metadata, index = _parse_metadata(lines, index + 1)
            result[key] = metadata
            continue
        result[key] = _parse_scalar(raw, number)
        index += 1
    return result


def _parse_entries(
    lines: list[tuple[int, str]],
    index: int,
) -> tuple[list[object], int]:
    entries: list[object] = []
    while index < len(lines):
        number, line = lines[index]
        if not line.startswith("  "):
            break
        if not line.startswith("  - "):
            raise StateFormatError(f"expected list item on line {number}")
        first = line[4:]
        if ":" not in first:
            entries.append(_parse_scalar(first.strip(), number))
            index += 1
            continue
        key, raw = _split_key_value(first, number)
        entry: dict[str, object] = {key: _parse_scalar(raw.strip(), number)}
        index += 1
        while index < len(lines):
            child_number, child = lines[index]
            if not child.startswith("    ") or child.startswith("  - "):
                break
            child_key, child_raw = _split_key_value(child[4:], child_number)
            if child_key in entry:
                raise StateFormatError(
                    f"duplicate key {child_key!r} on line {child_number}"
                )
            entry[child_key] = _parse_scalar(child_raw.strip(), child_number)
            index += 1
        entries.append(entry)
    return entries, index


def _parse_metadata(
    lines: list[tuple[int, str]],
    index: int,
) -> tuple[dict[str, object], int]:
    metadata: dict[str, object] = {}
    while index < len(lines):
        number, line = lines[index]
        if not line.startswith("  "):
            break
        if line.startswith("    ") or line.startswith("  - "):
            raise StateFormatError(f"invalid metadata indentation on line {number}")
        key, raw = _split_key_value(line[2:], number)
        if key in metadata:
            raise StateFormatError(f"duplicate key {key!r} on line {number}")
        metadata[key] = _parse_scalar(raw.strip(), number)
        index += 1
    return metadata, index


def _split_key_value(line: str, number: int) -> tuple[str, str]:
    match = _KEY_VALUE.fullmatch(line)
    if match is None:
        raise StateFormatError(f"expected key/value pair on line {number}")
    return match.group(1), match.group(2)


def _parse_scalar(raw: str, number: int) -> object:
    if not raw:
        raise StateFormatError(f"missing scalar on line {number}")
    if raw == "null":
        return None
    if re.fullmatch(r"0|[1-9][0-9]*", raw):
        return int(raw)
    if raw.startswith('"'):
        try:
            value = json.loads(raw)
        except json.JSONDecodeError as error:
            raise StateFormatError(f"invalid quoted string on line {number}") from error
        if not isinstance(value, str):
            raise StateFormatError(f"scalar must be a string on line {number}")
        return value
    raise StateFormatError(f"unsupported scalar on line {number}")


def _quote(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def _required_string(value: object, name: str) -> str:
    if not isinstance(value, str):
        raise StateValidationError(f"{name} must be a string")
    return value


def _optional_string(value: object, name: str) -> Optional[str]:
    if value is None:
        return None
    return _required_string(value, name)


def _entries(value: object, name: str) -> tuple[StateEntry, ...]:
    if not isinstance(value, list):
        raise StateValidationError(f"{name} must be a list")
    return tuple(StateEntry.from_value(entry) for entry in value)


def _reject_control_characters(value: str, name: str) -> None:
    if any(ord(character) < 32 and character not in "\n\r\t" for character in value):
        raise StateValidationError(f"{name} contains control characters")


def _validate_relative_source(value: str) -> None:
    normalized = value.replace("\\", "/")
    path = PurePosixPath(normalized)
    if (
        not value.strip()
        or path.is_absolute()
        or re.match(r"^[A-Za-z]:", normalized)
        or ".." in path.parts
    ):
        raise StateValidationError("source must be repository-relative")
