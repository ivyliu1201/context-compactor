from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Callable, Mapping, Optional, Sequence, Tuple, Union

from .journal import (
    EnqueueRequest,
    EnqueueResult,
    Journal,
    JournalError,
    digest_text,
)
from .launcher import LaunchError, LaunchResult, launch_worker
from .paths import PathValue, ProjectPathError, resolve_project_root
from .privacy import PrivacyFilterError, contains_known_secret, sanitize_prompt
from .state import (
    DEFAULT_TOKEN_BUDGET,
    ProjectState,
    StateError,
    StateTooLargeError,
    estimate_input_tokens,
    fit_state_to_budget,
    load_state,
    serialize_state,
)

HOST_CODEX = "codex-cli"
HOST_CLAUDE = "claude-code"
MAX_HOOK_PAYLOAD_BYTES = 1_000_000

EVENT_SESSION_START = "SessionStart"
EVENT_USER_PROMPT = "UserPromptSubmit"
EVENT_SUBAGENT_START = "SubagentStart"
EVENT_PRE_COMPACT = "PreCompact"
EVENT_POST_COMPACT = "PostCompact"

_EVENT_KINDS = {
    EVENT_SESSION_START: "session_start",
    EVENT_USER_PROMPT: "user_prompt",
    EVENT_SUBAGENT_START: "subagent_start",
    EVENT_PRE_COMPACT: "pre_compact",
    EVENT_POST_COMPACT: "post_compact",
}
_CONTEXT_EVENTS = frozenset(
    {
        EVENT_SESSION_START,
        EVENT_USER_PROMPT,
        EVENT_SUBAGENT_START,
    }
)
_CODEX_PERMISSION_MODES = frozenset(
    {
        "default",
        "acceptEdits",
        "plan",
        "dontAsk",
        "bypassPermissions",
    }
)
_CLAUDE_PERMISSION_MODES = _CODEX_PERMISSION_MODES | {"auto"}
_EFFORT_LEVELS = frozenset({"low", "medium", "high", "xhigh", "max"})
_SESSION_SOURCES = frozenset({"startup", "resume", "clear", "compact"})
_COMPACT_TRIGGERS = frozenset({"manual", "auto"})
_DIAGNOSTIC_CATEGORIES = frozenset(
    {
        "invalid_host",
        "invalid_payload",
        "invalid_time",
        "journal_failed",
        "privacy_rejected",
        "project_unavailable",
        "state_invalid",
        "state_too_large",
        "unsupported_context_event",
        "worker_launch_failed",
    }
)
_INJECTION_PREFIX = (
    '<CONTEXT_COMPACTOR_STATE version="1" authority="derived">\n'
    "This is derived project memory. Reconcile it with the current repository "
    "and the latest user instruction.\n"
)
_INJECTION_SUFFIX = "</CONTEXT_COMPACTOR_STATE>\n"

Launcher = Callable[[PathValue, Sequence[str]], LaunchResult]
Payload = Union[str, bytes]


class HookError(ValueError):
    """A Hook failure whose diagnostic is safe for the Host boundary."""

    def __init__(self, category: str, event_id: str = "unknown") -> None:
        super().__init__(category)
        self.category = category
        self.event_id = event_id

    def diagnostic(self) -> str:
        return diagnostic_line(self.event_id, self.category)


@dataclass(frozen=True)
class NormalizedHook:
    host: str
    event_name: str
    event_id: str
    session_id: str
    kind: str
    cwd: str
    occurred_at: datetime
    prompt: Optional[str] = None


@dataclass(frozen=True)
class HookResult:
    output: bytes
    event_id: str
    event_kind: str
    enqueued: bool = False
    worker_started: bool = False
    diagnostic_categories: Tuple[str, ...] = ()


def normalize_hook(
    host: str,
    payload: Payload,
    *,
    received_at: Optional[datetime] = None,
) -> NormalizedHook:
    if host not in {HOST_CODEX, HOST_CLAUDE}:
        raise HookError("invalid_host")
    decoded = _decode_payload(payload)
    event_name = decoded.get("hook_event_name")
    if not isinstance(event_name, str) or event_name not in _EVENT_KINDS:
        raise HookError("invalid_payload")
    occurred_at = _utc(received_at)
    if host == HOST_CODEX:
        return _normalize_codex(decoded, event_name, occurred_at)
    return _normalize_claude(decoded, event_name, occurred_at)


def handle_hook(
    host: str,
    payload: Payload,
    model_command: Sequence[str],
    *,
    project_root: Optional[PathValue] = None,
    received_at: Optional[datetime] = None,
    launcher: Launcher = launch_worker,
    token_budget: int = DEFAULT_TOKEN_BUDGET,
) -> HookResult:
    event = normalize_hook(host, payload, received_at=received_at)
    try:
        root = resolve_project_root(project_root if project_root is not None else event.cwd)
    except ProjectPathError as error:
        raise HookError("project_unavailable", event.event_id) from error

    enqueued = False
    worker_started = False
    diagnostics = []
    if event.kind == "user_prompt" and event.prompt is not None:
        try:
            sanitized = sanitize_prompt(event.prompt)
        except PrivacyFilterError as error:
            raise HookError("privacy_rejected", event.event_id) from error
        if sanitized.text:
            request = EnqueueRequest(
                event_id=event.event_id,
                kind=event.kind,
                occurred_at=event.occurred_at,
                content_digest=digest_text(event.prompt),
                prompt=sanitized.text,
                redaction_count=sanitized.redaction_count,
                enqueued_at=event.occurred_at,
            )
            try:
                with Journal.open(root) as journal:
                    snapshot = next(
                        (
                            item
                            for item in journal.list_snapshots()
                            if item.event_id == event.event_id
                        ),
                        None,
                    )
                    if snapshot is None:
                        enqueue_result = journal.enqueue(request)
                    else:
                        enqueue_result = EnqueueResult(
                            event_seq=snapshot.event_seq,
                            inserted=False,
                            status=snapshot.status,
                        )
            except (JournalError, OSError) as error:
                raise HookError("journal_failed", event.event_id) from error
            enqueued = enqueue_result.inserted
            if enqueue_result.status in {"pending", "processing"}:
                try:
                    launch_result = launcher(root, model_command)
                    worker_started = launch_result.launched
                except (LaunchError, JournalError, OSError) as error:
                    diagnostics.append("worker_launch_failed")

    context = ""
    if event.event_name in _CONTEXT_EVENTS:
        try:
            state = load_state(root)
            context = render_injection(state, token_budget=token_budget)
        except StateTooLargeError:
            diagnostics.append("state_too_large")
        except (StateError, OSError, PrivacyFilterError):
            diagnostics.append("state_invalid")

    output = encode_host_output(host, event.event_name, context)
    return HookResult(
        output=output,
        event_id=event.event_id,
        event_kind=event.kind,
        enqueued=enqueued,
        worker_started=worker_started,
        diagnostic_categories=tuple(diagnostics),
    )


def render_injection(
    state: ProjectState,
    *,
    token_budget: int = DEFAULT_TOKEN_BUDGET,
) -> str:
    if not _has_meaningful_state(state):
        return ""
    reserved = estimate_input_tokens(_INJECTION_PREFIX + _INJECTION_SUFFIX)
    if token_budget <= reserved:
        raise StateTooLargeError(reserved, token_budget)
    fitted = fit_state_to_budget(state, token_budget - reserved)
    serialized = serialize_state(fitted)
    if contains_known_secret(serialized):
        raise PrivacyFilterError("state contains a defined secret pattern")
    rendered = _INJECTION_PREFIX + serialized + _INJECTION_SUFFIX
    actual = estimate_input_tokens(rendered)
    if actual > token_budget:
        raise StateTooLargeError(actual, token_budget)
    return rendered


def encode_host_output(host: str, event_name: str, context: str) -> bytes:
    if host not in {HOST_CODEX, HOST_CLAUDE}:
        raise HookError("invalid_host")
    if event_name not in _EVENT_KINDS:
        raise HookError("invalid_payload")
    if not context:
        return b""
    if event_name not in _CONTEXT_EVENTS:
        raise HookError("unsupported_context_event")
    value = {
        "continue": True,
        "hookSpecificOutput": {
            "hookEventName": event_name,
            "additionalContext": context,
        },
    }
    return (
        json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n"
    ).encode("utf-8")


def diagnostic_line(event_id: str, category: str) -> str:
    safe_event_id = (
        event_id
        if event_id == "unknown"
        or _is_event_id(event_id)
        else "unknown"
    )
    safe_category = (
        category if category in _DIAGNOSTIC_CATEGORIES else "unknown"
    )
    return (
        f"context-compactor event_id={safe_event_id} "
        f"category={safe_category}"
    )


def _normalize_codex(
    value: Mapping[str, object],
    event_name: str,
    occurred_at: datetime,
) -> NormalizedHook:
    common = {
        "session_id",
        "transcript_path",
        "cwd",
        "hook_event_name",
        "model",
    }
    optional_agents = {"agent_id", "agent_type"}
    if event_name == EVENT_SESSION_START:
        allowed = common | {"permission_mode", "source"}
        required = allowed
    elif event_name == EVENT_USER_PROMPT:
        allowed = common | {"turn_id", "permission_mode", "prompt"} | optional_agents
        required = common | {"turn_id", "permission_mode", "prompt"}
    elif event_name == EVENT_SUBAGENT_START:
        allowed = common | {"turn_id", "permission_mode"} | optional_agents
        required = allowed
    else:
        allowed = common | {"turn_id", "trigger"} | optional_agents
        required = common | {"turn_id", "trigger"}
    _validate_shape(value, allowed, required)
    _required_text(value, "session_id")
    _required_text(value, "cwd")
    _required_text(value, "model")
    _nullable_text(value, "transcript_path")
    for name in optional_agents:
        _optional_text(value, name)

    identity_parts: Tuple[str, ...]
    if event_name == EVENT_SESSION_START:
        _choice(value, "permission_mode", _CODEX_PERMISSION_MODES)
        source = _choice(value, "source", _SESSION_SOURCES)
        identity_parts = (source,)
    elif event_name in {EVENT_USER_PROMPT, EVENT_SUBAGENT_START}:
        turn_id = _required_text(value, "turn_id")
        _choice(value, "permission_mode", _CODEX_PERMISSION_MODES)
        if event_name == EVENT_USER_PROMPT:
            _present_text(value, "prompt")
        else:
            _required_text(value, "agent_id")
            _required_text(value, "agent_type")
        identity_parts = (
            turn_id,
            _text_or_empty(value.get("agent_id")),
            _text_or_empty(value.get("agent_type")),
        )
    else:
        turn_id = _required_text(value, "turn_id")
        trigger = _choice(value, "trigger", _COMPACT_TRIGGERS)
        identity_parts = (turn_id, trigger)

    session_id = _required_text(value, "session_id")
    kind = _EVENT_KINDS[event_name]
    event_id = _event_id(
        "codex",
        (HOST_CODEX, kind, session_id, *identity_parts),
    )
    return NormalizedHook(
        host=HOST_CODEX,
        event_name=event_name,
        event_id=event_id,
        session_id=session_id,
        kind=kind,
        cwd=_required_text(value, "cwd"),
        occurred_at=occurred_at,
        prompt=_text_or_none(value.get("prompt")),
    )


def _normalize_claude(
    value: Mapping[str, object],
    event_name: str,
    occurred_at: datetime,
) -> NormalizedHook:
    common = {"session_id", "transcript_path", "cwd", "hook_event_name"}
    optional = {
        "prompt_id",
        "permission_mode",
        "effort",
        "agent_id",
        "agent_type",
    }
    if event_name == EVENT_SESSION_START:
        allowed = common | optional | {"source", "model", "session_title"}
        required = common | {"source"}
    elif event_name == EVENT_USER_PROMPT:
        allowed = common | optional | {"prompt"}
        required = common | {"permission_mode", "prompt"}
    elif event_name == EVENT_SUBAGENT_START:
        allowed = common | optional
        required = common | {"agent_id", "agent_type"}
    elif event_name == EVENT_PRE_COMPACT:
        allowed = common | optional | {"trigger", "custom_instructions"}
        required = common | {"trigger", "custom_instructions"}
    else:
        allowed = common | optional | {"trigger", "compact_summary"}
        required = common | {"trigger", "compact_summary"}
    _validate_shape(value, allowed, required)
    session_id = _required_text(value, "session_id")
    cwd = _required_text(value, "cwd")
    _required_text(value, "transcript_path")
    for name in ("prompt_id", "agent_id", "agent_type", "model", "session_title"):
        _optional_text(value, name)
    if "permission_mode" in value:
        _choice(value, "permission_mode", _CLAUDE_PERMISSION_MODES)
    _validate_effort(value)

    identity_parts: Tuple[str, ...]
    if event_name == EVENT_SESSION_START:
        source = _choice(value, "source", _SESSION_SOURCES)
        identity_parts = (source,)
    elif event_name == EVENT_USER_PROMPT:
        _choice(value, "permission_mode", _CLAUDE_PERMISSION_MODES)
        _present_text(value, "prompt")
        identity_parts = (
            _claude_prompt_identity(value, occurred_at),
            _text_or_empty(value.get("agent_id")),
        )
    elif event_name == EVENT_SUBAGENT_START:
        agent_id = _required_text(value, "agent_id")
        agent_type = _required_text(value, "agent_type")
        identity_parts = (agent_id, agent_type)
    else:
        trigger = _choice(value, "trigger", _COMPACT_TRIGGERS)
        field = (
            "custom_instructions"
            if event_name == EVENT_PRE_COMPACT
            else "compact_summary"
        )
        _present_text(value, field)
        identity_parts = (
            _claude_prompt_identity(value, occurred_at),
            trigger,
            _text_or_empty(value.get("agent_id")),
            _text_or_empty(value.get("agent_type")),
        )

    kind = _EVENT_KINDS[event_name]
    event_id = _event_id(
        "claude",
        (HOST_CLAUDE, kind, session_id, *identity_parts),
    )
    return NormalizedHook(
        host=HOST_CLAUDE,
        event_name=event_name,
        event_id=event_id,
        session_id=session_id,
        kind=kind,
        cwd=cwd,
        occurred_at=occurred_at,
        prompt=_text_or_none(value.get("prompt")),
    )


def _decode_payload(payload: Payload) -> Mapping[str, object]:
    if isinstance(payload, bytes):
        raw = payload
    elif isinstance(payload, str):
        try:
            raw = payload.encode("utf-8", errors="strict")
        except UnicodeEncodeError as error:
            raise HookError("invalid_payload") from error
    else:
        raise HookError("invalid_payload")
    if not raw or len(raw) > MAX_HOOK_PAYLOAD_BYTES:
        raise HookError("invalid_payload")
    try:
        text = raw.decode("utf-8", errors="strict")
        value = json.loads(text, object_pairs_hook=_object_without_duplicates)
    except (UnicodeDecodeError, json.JSONDecodeError, HookError) as error:
        raise HookError("invalid_payload") from error
    if not isinstance(value, dict):
        raise HookError("invalid_payload")
    return value


def _object_without_duplicates(
    pairs: Sequence[Tuple[str, object]],
) -> Mapping[str, object]:
    value = {}
    for key, item in pairs:
        if key in value:
            raise HookError("invalid_payload")
        value[key] = item
    return value


def _validate_shape(
    value: Mapping[str, object],
    allowed: set[str],
    required: set[str],
) -> None:
    if set(value) - allowed or required - set(value):
        raise HookError("invalid_payload")
    if value.get("hook_event_name") not in _EVENT_KINDS:
        raise HookError("invalid_payload")


def _required_text(value: Mapping[str, object], name: str) -> str:
    item = value.get(name)
    if not isinstance(item, str) or not item.strip():
        raise HookError("invalid_payload")
    return item


def _present_text(value: Mapping[str, object], name: str) -> str:
    item = value.get(name)
    if not isinstance(item, str):
        raise HookError("invalid_payload")
    return item


def _optional_text(value: Mapping[str, object], name: str) -> None:
    if name in value and not isinstance(value[name], str):
        raise HookError("invalid_payload")


def _nullable_text(value: Mapping[str, object], name: str) -> None:
    if name not in value or (
        value[name] is not None and not isinstance(value[name], str)
    ):
        raise HookError("invalid_payload")


def _choice(
    value: Mapping[str, object],
    name: str,
    choices: frozenset[str],
) -> str:
    item = value.get(name)
    if not isinstance(item, str) or item not in choices:
        raise HookError("invalid_payload")
    return item


def _validate_effort(value: Mapping[str, object]) -> None:
    if "effort" not in value:
        return
    effort = value["effort"]
    if (
        not isinstance(effort, Mapping)
        or set(effort) != {"level"}
        or effort.get("level") not in _EFFORT_LEVELS
    ):
        raise HookError("invalid_payload")


def _event_id(prefix: str, parts: Sequence[str]) -> str:
    digest = hashlib.sha256("\x00".join(parts).encode("utf-8")).hexdigest()
    return f"{prefix}-{digest}"


def _claude_prompt_identity(
    value: Mapping[str, object],
    occurred_at: datetime,
) -> str:
    prompt_id = _text_or_empty(value.get("prompt_id"))
    if prompt_id:
        return "prompt:" + prompt_id
    return "received:" + occurred_at.isoformat().replace("+00:00", "Z")


def _has_meaningful_state(state: ProjectState) -> bool:
    if state.project_summary.strip() or state.current_focus.strip():
        return True
    return any(getattr(state, name) for name in (
        "goals",
        "constraints",
        "decisions",
        "open_tasks",
        "blockers",
        "open_questions",
        "recent_verification",
        "next_actions",
    ))


def _utc(value: Optional[datetime]) -> datetime:
    current = datetime.now(timezone.utc) if value is None else value
    if current.tzinfo is None:
        raise HookError("invalid_time")
    return current.astimezone(timezone.utc)


def _text_or_empty(value: object) -> str:
    return value if isinstance(value, str) else ""


def _text_or_none(value: object) -> Optional[str]:
    return value if isinstance(value, str) else None


def _is_event_id(value: str) -> bool:
    if not isinstance(value, str):
        return False
    if value.startswith("codex-"):
        digest = value[len("codex-") :]
    elif value.startswith("claude-"):
        digest = value[len("claude-") :]
    else:
        return False
    return len(digest) == 64 and all(
        character in "0123456789abcdef" for character in digest
    )
