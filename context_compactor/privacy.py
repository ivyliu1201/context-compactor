from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Callable, Collection, Match, Optional, Pattern, Tuple

REDACTION_MARKER = "[REDACTED]"
MAX_RETAINED_CHARACTERS = 8_000
DEFAULT_HIGH_RISK_ENVIRONMENT_NAMES = frozenset(
    {
        "ANTHROPIC_API_KEY",
        "AWS_SECRET_ACCESS_KEY",
        "AZURE_CLIENT_SECRET",
        "DATABASE_URL",
        "GH_TOKEN",
        "GITHUB_TOKEN",
        "GOOGLE_API_KEY",
        "OPENAI_API_KEY",
    }
)

_SECRET_VALUE = r"(?:\"(?:\\.|[^\"\r\n])*\"|'(?:\\.|[^'\r\n])*'|\S+)"
_PRIVATE_KEY_PATTERN = re.compile(
    r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----.*?"
    r"(?:-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|\Z)",
    re.IGNORECASE | re.DOTALL,
)
_AUTHORIZATION_PATTERN = re.compile(
    r"(\bauthorization\s*:\s*)(?:bearer|basic)\s+\S+",
    re.IGNORECASE,
)
_BEARER_PATTERN = re.compile(
    r"(\bbearer\s+)[A-Za-z0-9._~+/=-]{8,}",
    re.IGNORECASE,
)
_ASSIGNMENT_PATTERN = re.compile(
    rf"(\b(?:api[\s_-]?key|access[\s_-]?token|client[\s_-]?secret|"
    rf"token|secret|password|passwd|pwd)\b\s*[:=]\s*){_SECRET_VALUE}",
    re.IGNORECASE,
)
_KNOWN_TOKEN_PATTERNS = (
    re.compile(r"\bsk-(?:proj-)?[A-Za-z0-9_-]{8,}\b"),
    re.compile(r"\bgh[pousr]_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bAKIA[A-Z0-9]{16}\b"),
)
_ENVIRONMENT_NAME = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


class PrivacyFilterError(ValueError):
    """Raised when prompt text cannot cross the durable privacy boundary."""


@dataclass(frozen=True)
class SanitizedText:
    text: str
    redaction_count: int
    truncated: bool


def sanitize_prompt(
    text: str,
    *,
    max_characters: int = MAX_RETAINED_CHARACTERS,
    high_risk_environment_names: Optional[Collection[str]] = None,
) -> SanitizedText:
    _validate_input(text, max_characters)
    names = (
        DEFAULT_HIGH_RISK_ENVIRONMENT_NAMES
        if high_risk_environment_names is None
        else frozenset(high_risk_environment_names)
    )
    environment_pattern = _environment_pattern(names)

    sanitized = text
    total = 0
    sanitized, count = _replace(
        _PRIVATE_KEY_PATTERN,
        sanitized,
        lambda _: REDACTION_MARKER,
    )
    total += count
    sanitized, count = _replace(
        _AUTHORIZATION_PATTERN,
        sanitized,
        lambda match: match.group(1) + REDACTION_MARKER,
    )
    total += count
    sanitized, count = _replace(
        _BEARER_PATTERN,
        sanitized,
        lambda match: match.group(1) + REDACTION_MARKER,
    )
    total += count
    sanitized, count = _replace(
        _ASSIGNMENT_PATTERN,
        sanitized,
        lambda match: match.group(1) + REDACTION_MARKER,
    )
    total += count
    if environment_pattern is not None:
        sanitized, count = _replace(
            environment_pattern,
            sanitized,
            lambda match: match.group(1) + REDACTION_MARKER,
        )
        total += count
    for pattern in _KNOWN_TOKEN_PATTERNS:
        sanitized, count = _replace(
            pattern,
            sanitized,
            lambda _: REDACTION_MARKER,
        )
        total += count

    sanitized = sanitized.strip()
    truncated = len(sanitized) > max_characters
    if truncated:
        sanitized = _bounded_text(sanitized, max_characters)
    return SanitizedText(
        text=sanitized,
        redaction_count=total,
        truncated=truncated,
    )


def contains_known_secret(
    text: str,
    *,
    high_risk_environment_names: Optional[Collection[str]] = None,
) -> bool:
    result = sanitize_prompt(
        text,
        high_risk_environment_names=high_risk_environment_names,
    )
    return result.redaction_count > 0


def _validate_input(text: str, max_characters: int) -> None:
    if not isinstance(text, str):
        raise PrivacyFilterError("prompt text must be a string")
    if (
        isinstance(max_characters, bool)
        or not isinstance(max_characters, int)
        or max_characters <= 0
        or max_characters > MAX_RETAINED_CHARACTERS
    ):
        raise PrivacyFilterError(
            f"retained prompt limit must be between 1 and {MAX_RETAINED_CHARACTERS}"
        )
    if any(
        ord(character) < 32 and character not in "\t\n\r"
        for character in text
    ):
        raise PrivacyFilterError("prompt text contains unsafe control characters")
    try:
        text.encode("utf-8", errors="strict")
    except UnicodeEncodeError as error:
        raise PrivacyFilterError("prompt text is not valid UTF-8") from error


def _environment_pattern(names: Collection[str]) -> Optional[Pattern[str]]:
    normalized = set()
    for name in names:
        if not isinstance(name, str) or _ENVIRONMENT_NAME.fullmatch(name) is None:
            raise PrivacyFilterError("high-risk environment name is invalid")
        normalized.add(name)
    if not normalized:
        return None
    alternatives = "|".join(
        re.escape(name) for name in sorted(normalized, key=lambda value: (-len(value), value))
    )
    return re.compile(
        rf"(\b(?:{alternatives})\b\s*[:=]\s*){_SECRET_VALUE}",
        re.IGNORECASE,
    )


def _replace(
    pattern: Pattern[str],
    text: str,
    replacement: Callable[[Match[str]], str],
) -> Tuple[str, int]:
    return pattern.subn(replacement, text)


def _bounded_text(text: str, max_characters: int) -> str:
    boundary = max_characters
    marker_start = text.rfind(
        REDACTION_MARKER,
        max(0, boundary - len(REDACTION_MARKER) + 1),
        boundary + len(REDACTION_MARKER),
    )
    if marker_start >= 0 and marker_start < boundary < marker_start + len(
        REDACTION_MARKER
    ):
        boundary = marker_start
    return text[:boundary]
