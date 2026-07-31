from __future__ import annotations

import argparse
import contextlib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Mapping, Optional, Sequence

SOURCE_ROOT = Path(__file__).resolve().parents[1]
if str(SOURCE_ROOT) not in sys.path:
    sys.path.insert(0, str(SOURCE_ROOT))

from context_compactor.state import load_state

REPORT_SCHEMA_VERSION = 1
BACKGROUND_TIMEOUT_SECONDS = 30.0
HOOK_TIMEOUT_SECONDS = 20.0
HOOK_LATENCY_GATE_MS = 5_000.0
WINDOWS_CREATE_NO_WINDOW = 0x08000000
REDACTION_MARKER = "[REDACTED]"

_MODEL_SCRIPT = """\
import ctypes
import json
import os
import pathlib
import sys
import time

request = json.load(sys.stdin)
console = 0
if os.name == "nt":
    console = int(bool(ctypes.windll.kernel32.GetConsoleWindow()))
with pathlib.Path(sys.argv[1]).open("a", encoding="utf-8") as stream:
    stream.write(str(console) + "\\n")
    stream.flush()

time.sleep(1.0)
prompt = request["redacted_prompt"]
if "explanation-only" in prompt:
    result = {"outcome": "no_change"}
else:
    if "[REDACTED]" not in prompt:
        raise SystemExit(3)
    state = request["previous_state"]
    state["current_focus"] = "Verify the installed automatic memory flow."
    state["goals"] = [
        {
            "statement": "Keep local project memory private and bounded.",
            "source": "SPEC.md",
        }
    ]
    state["metadata"] = {
        "source_cursor": request["event"]["sequence"],
        "updated_at": "",
    }
    result = {"outcome": "updated", "state": state}
print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))
"""


class FreshProjectE2EError(RuntimeError):
    """Raised when the isolated E2E runner cannot produce a safe report."""


def _is_within(path: Path, parent: Path) -> bool:
    try:
        path.resolve(strict=False).relative_to(parent.resolve(strict=False))
    except ValueError:
        return False
    return True


def run_fresh_project_e2e(source_root: Path) -> dict[str, object]:
    source = source_root.resolve(strict=True)
    _validate_prerequisites(source)
    with tempfile.TemporaryDirectory(prefix="context-compactor-e2e-") as directory:
        temporary = Path(directory)
        profile = temporary / "profile"
        project = temporary / "fresh project"
        installation = profile / "Local App Data" / "context-compactor"
        tools = profile / "tools"
        project.mkdir(parents=True)
        tools.mkdir(parents=True)
        (profile / "Local App Data").mkdir(parents=True)
        (profile / "Roaming App Data").mkdir(parents=True)

        host_command = tools / "codex.cmd"
        host_command.write_text("@echo off\r\nexit /b 0\r\n", encoding="ascii")
        model_script = profile / "synthetic-model.py"
        model_script.write_text(_MODEL_SCRIPT, encoding="utf-8")
        console_observations = profile / "console-observations.txt"

        environment = _isolated_environment(profile, tools)
        powershell = _powershell()
        install_script = source / "scripts" / "install.ps1"
        model_command = (
            sys.executable,
            str(model_script),
            str(console_observations),
        )
        install = _run(
            (
                powershell,
                "-NoProfile",
                "-ExecutionPolicy",
                "Bypass",
                "-File",
                str(install_script),
                "-Action",
                "install",
                "-ProjectRoot",
                str(project),
                "-InstallDirectory",
                str(installation),
                "-AgentHost",
                "codex",
                "-Python",
                sys.executable,
                "-ModelCommandJson",
                json.dumps(model_command),
            ),
            cwd=source,
            environment=environment,
            timeout=180.0,
            category="install_failed",
        )
        install_report = _decode_report(install.stdout, "install_report_invalid")
        project_manifests = tuple((installation / "projects").glob("*.json"))
        if len(project_manifests) != 1:
            raise FreshProjectE2EError("project_manifest_missing")
        wrapper = Path(str(install_report.get("hook_wrapper", "")))
        private_python = Path(
            str(install_report.get("python_interpreter", ""))
        )
        active_source_value = install_report.get("active_source")
        active_source = (
            Path(str(active_source_value.get("path", "")))
            if isinstance(active_source_value, Mapping)
            else None
        )
        source_installed = (
            active_source is not None and active_source.is_dir()
        )
        private_venv_installed = (
            private_python.is_file()
            and _is_within(private_python, installation / ".venv")
        )
        hook_definition_healthy = bool(
            install_report.get("hooks")
            and install_report["hooks"][0].get("definition_healthy")
        )

        api_key = "sk-SYNTHETIC_API_KEY_123456789"
        bearer = "SYNTHETIC_BEARER_TOKEN_123456789"
        password = "SYNTHETIC_PASSWORD_123456789"
        private_key_body = "SYNTHETIC_PRIVATE_KEY_BODY_123456789"
        private_key = (
            "-----BEGIN PRIVATE KEY-----\n"
            + private_key_body
            + "\n-----END PRIVATE KEY-----"
        )
        secrets = (api_key, bearer, password, private_key_body, private_key)
        update_prompt = (
            f"api_key={api_key}\n"
            f"Authorization: Bearer {bearer}\n"
            f"password={password}\n"
            f"{private_key}\n"
            "memory-update-signal"
        )

        stdout_artifacts = [install.stdout]
        stderr_artifacts = [install.stderr]
        first_payload = _codex_prompt_payload(
            project,
            turn_id="turn-update",
            prompt=update_prompt,
        )
        first_hook, first_hook_ms = _run_hook(
            powershell,
            wrapper,
            project_manifests[0],
            first_payload,
            project,
            environment,
        )
        stdout_artifacts.append(first_hook.stdout)
        stderr_artifacts.append(first_hook.stderr)

        journal_path = project / ".context-compactor" / "events.sqlite"
        state_path = project / ".context-compactor" / "state.yaml"
        immediate_counts = _read_counts(journal_path)
        pending_prompts = _read_prompt_texts(journal_path)
        worker_outlived_hook = (
            immediate_counts["completed"] == 0 and not state_path.exists()
        )
        pending_redacted_prompt_observed = (
            len(pending_prompts) == 1
            and REDACTION_MARKER in pending_prompts[0]
            and len(pending_prompts[0]) <= 8_000
        )
        first_background_ms, first_counts = _wait_for_completion(
            journal_path,
            completed=1,
        )
        if not state_path.is_file():
            raise FreshProjectE2EError("state_not_published")
        state_after_update = state_path.read_bytes()
        state_mtime_after_update = state_path.stat().st_mtime_ns

        second_payload = _codex_prompt_payload(
            project,
            turn_id="turn-no-change",
            prompt=(
                "explanation-only: describe the architecture without "
                "changing project memory."
            ),
        )
        second_hook, second_hook_ms = _run_hook(
            powershell,
            wrapper,
            project_manifests[0],
            second_payload,
            project,
            environment,
        )
        stdout_artifacts.append(second_hook.stdout)
        stderr_artifacts.append(second_hook.stderr)
        second_background_ms, final_counts = _wait_for_completion(
            journal_path,
            completed=2,
        )
        no_change_preserved_state = (
            state_path.read_bytes() == state_after_update
            and state_path.stat().st_mtime_ns == state_mtime_after_update
        )

        session_hook, session_hook_ms = _run_hook(
            powershell,
            wrapper,
            project_manifests[0],
            _codex_session_payload(project),
            project,
            environment,
        )
        stdout_artifacts.append(session_hook.stdout)
        stderr_artifacts.append(session_hook.stderr)
        injection = _decode_report(
            session_hook.stdout,
            "session_injection_invalid",
        )
        injection_context = _injection_context(injection)
        state = load_state(project)

        console_lines = (
            console_observations.read_text(encoding="utf-8").splitlines()
            if console_observations.is_file()
            else []
        )
        visible_windows = sum(line != "0" for line in console_lines)
        yaml_text = state_path.read_text(encoding="utf-8")
        sqlite_text = _read_sqlite_text(journal_path)
        sqlite_bytes = b"".join(
            path.read_bytes()
            for path in journal_path.parent.glob("events.sqlite*")
            if path.is_file()
        )
        log_text = _read_logs((profile, project, installation))

        uninstall = _run(
            (
                powershell,
                "-NoProfile",
                "-ExecutionPolicy",
                "Bypass",
                "-File",
                str(install_script),
                "-Action",
                "uninstall",
                "-ProjectRoot",
                str(project),
                "-InstallDirectory",
                str(installation),
                "-AgentHost",
                "codex",
                "-Python",
                sys.executable,
            ),
            cwd=source,
            environment=environment,
            timeout=60.0,
            category="uninstall_failed",
        )
        uninstall_report = _decode_report(
            uninstall.stdout,
            "uninstall_report_invalid",
        )
        stdout_artifacts.append(uninstall.stdout)
        stderr_artifacts.append(uninstall.stderr)

        privacy_matches = {
            "yaml": _text_matches(yaml_text, secrets),
            "sqlite_text": _text_matches(sqlite_text, secrets),
            "sqlite_bytes": _byte_matches(sqlite_bytes, secrets),
            "stdout": _byte_matches(b"".join(stdout_artifacts), secrets),
            "stderr": _byte_matches(b"".join(stderr_artifacts), secrets),
            "logs": _text_matches(log_text, secrets),
            "pending_redacted_prompt": _text_matches(
                "\n".join(pending_prompts),
                secrets,
            ),
        }
        counts = {
            "events": final_counts["events"],
            "pending": final_counts["pending"],
            "processing": final_counts["processing"],
            "completed": final_counts["completed"],
            "failed": final_counts["failed"],
            "attempts": final_counts["attempts"],
            "updated": final_counts["updated"],
            "no_change": final_counts["no_change"],
            "retained_prompts": final_counts["retained_prompts"],
            "redactions": final_counts["redactions"],
            "state_publications": int(
                bool(state_after_update) and no_change_preserved_state
            ),
            "injection_bytes": len(session_hook.stdout),
            "console_observations": len(console_lines),
            "visible_console_windows": visible_windows,
        }
        latency_ms = {
            "hook": [
                round(first_hook_ms, 3),
                round(second_hook_ms, 3),
                round(session_hook_ms, 3),
            ],
            "background": [
                round(first_background_ms, 3),
                round(second_background_ms, 3),
            ],
        }
        checks = {
            "source_installed": source_installed,
            "private_venv": private_venv_installed,
            "hook_definition_healthy": hook_definition_healthy,
            "prompt_hook_protocol_valid": (
                _valid_optional_hook_output(
                    first_hook.stdout,
                    "UserPromptSubmit",
                )
                and _valid_optional_hook_output(
                    second_hook.stdout,
                    "UserPromptSubmit",
                )
            ),
            "prompt_hook_stderr_empty": (
                first_hook.stderr == b"" and second_hook.stderr == b""
            ),
            "worker_outlived_hook": worker_outlived_hook,
            "pending_prompt_redacted_and_bounded": (
                pending_redacted_prompt_observed
            ),
            "updated_completed": (
                first_counts["completed"] == 1
                and first_counts["updated"] == 1
                and first_counts["failed"] == 0
            ),
            "no_change_completed": (
                final_counts["completed"] == 2
                and final_counts["no_change"] == 1
                and final_counts["failed"] == 0
            ),
            "successful_prompts_cleared": (
                final_counts["retained_prompts"] == 0
            ),
            "state_published_once": counts["state_publications"] == 1,
            "no_change_preserved_state": no_change_preserved_state,
            "state_cursor_matches_update": state.metadata.source_cursor == 1,
            "journal_cursor_reached_last_event": (
                final_counts["source_cursor"] == 2
            ),
            "next_session_injected_state": (
                len(session_hook.stdout) > 0
                and 'authority="derived"' in injection_context
            ),
            "hook_latency_within_gate": (
                max(latency_ms["hook"]) < HOOK_LATENCY_GATE_MS
            ),
            "background_completed_within_gate": (
                max(latency_ms["background"])
                < BACKGROUND_TIMEOUT_SECONDS * 1_000
            ),
            "no_visible_console": (
                len(console_lines) == 2 and visible_windows == 0
            ),
            "privacy_scan_zero": all(
                matches == 0 for matches in privacy_matches.values()
            ),
            "uninstalled_cleanly": bool(
                uninstall_report.get("installation_removed")
            )
            and not installation.exists(),
        }
        report = {
            "schema_version": REPORT_SCHEMA_VERSION,
            "scenario": "fresh-project-standard",
            "mode": "standard",
            "counts": counts,
            "latency_ms": latency_ms,
            "privacy_matches": privacy_matches,
            "checks": checks,
            "ok": all(checks.values()),
        }
        report_text = json.dumps(
            report,
            ensure_ascii=False,
            separators=(",", ":"),
        )
        privacy_matches["report"] = _text_matches(report_text, secrets)
        checks["privacy_scan_zero"] = all(
            matches == 0 for matches in privacy_matches.values()
        )
        report["ok"] = all(checks.values())
        return report


def _validate_prerequisites(source_root: Path) -> None:
    if os.name != "nt":
        raise FreshProjectE2EError("windows_required")
    if not (source_root / "scripts" / "install.ps1").is_file():
        raise FreshProjectE2EError("installer_missing")
    if shutil.which("powershell.exe") is None or shutil.which("git") is None:
        raise FreshProjectE2EError("required_tool_missing")


def _isolated_environment(profile: Path, tools: Path) -> dict[str, str]:
    environment = os.environ.copy()
    environment.update(
        {
            "USERPROFILE": str(profile),
            "LOCALAPPDATA": str(profile / "Local App Data"),
            "APPDATA": str(profile / "Roaming App Data"),
            "PSModuleAnalysisCachePath": str(
                profile / "PowerShell" / "ModuleAnalysisCache"
            ),
            "PATH": str(tools)
            + os.pathsep
            + environment.get("PATH", ""),
            "PYTHONDONTWRITEBYTECODE": "1",
        }
    )
    return environment


def _powershell() -> str:
    value = shutil.which("powershell.exe")
    if value is None:
        raise FreshProjectE2EError("powershell_missing")
    return value


def _run(
    command: Sequence[str],
    *,
    cwd: Path,
    environment: Mapping[str, str],
    timeout: float,
    category: str,
    input_bytes: Optional[bytes] = None,
) -> subprocess.CompletedProcess[bytes]:
    options: dict[str, object] = {}
    if os.name == "nt":
        options["creationflags"] = WINDOWS_CREATE_NO_WINDOW
    try:
        completed = subprocess.run(
            tuple(command),
            cwd=str(cwd),
            env=dict(environment),
            input=input_bytes,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
            **options,
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise FreshProjectE2EError(category) from error
    if completed.returncode != 0:
        raise FreshProjectE2EError(category)
    return completed


def _run_hook(
    powershell: str,
    wrapper: Path,
    manifest: Path,
    payload: bytes,
    project: Path,
    environment: Mapping[str, str],
) -> tuple[subprocess.CompletedProcess[bytes], float]:
    started = time.monotonic()
    completed = _run(
        (
            powershell,
            "-NoProfile",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            str(wrapper),
            "-Manifest",
            str(manifest),
            "-AgentHost",
            "codex",
        ),
        cwd=project,
        environment=environment,
        input_bytes=payload,
        timeout=HOOK_TIMEOUT_SECONDS,
        category="hook_failed",
    )
    return completed, (time.monotonic() - started) * 1_000


def _codex_prompt_payload(
    project: Path,
    *,
    turn_id: str,
    prompt: str,
) -> bytes:
    return json.dumps(
        {
            "session_id": "fresh-project-session",
            "transcript_path": None,
            "cwd": str(project),
            "hook_event_name": "UserPromptSubmit",
            "model": "synthetic-local-model",
            "turn_id": turn_id,
            "permission_mode": "acceptEdits",
            "prompt": prompt,
        },
        ensure_ascii=False,
        separators=(",", ":"),
    ).encode("utf-8")


def _codex_session_payload(project: Path) -> bytes:
    return json.dumps(
        {
            "session_id": "fresh-project-next-session",
            "transcript_path": None,
            "cwd": str(project),
            "hook_event_name": "SessionStart",
            "model": "synthetic-local-model",
            "permission_mode": "default",
            "source": "resume",
        },
        separators=(",", ":"),
    ).encode("utf-8")


def _read_counts(journal_path: Path) -> dict[str, int]:
    if not journal_path.is_file():
        raise FreshProjectE2EError("journal_missing")
    with contextlib.closing(_read_only_connection(journal_path)) as connection:
        jobs = connection.execute(
            """
SELECT
    COUNT(*) AS jobs,
    COALESCE(SUM(status = 'pending'), 0) AS pending,
    COALESCE(SUM(status = 'processing'), 0) AS processing,
    COALESCE(SUM(status = 'completed'), 0) AS completed,
    COALESCE(SUM(status = 'failed'), 0) AS failed,
    COALESCE(SUM(attempt_count), 0) AS attempts,
    COALESCE(SUM(outcome = 'updated'), 0) AS updated,
    COALESCE(SUM(outcome = 'no_change'), 0) AS no_change,
    COALESCE(SUM(prompt_text IS NOT NULL), 0) AS retained_prompts,
    COALESCE(SUM(redaction_count), 0) AS redactions
FROM jobs
"""
        ).fetchone()
        events = connection.execute("SELECT COUNT(*) FROM events").fetchone()[0]
        runtime = connection.execute(
            """
SELECT source_cursor, worker_lease_token
FROM runtime_state
WHERE singleton = 1
"""
        ).fetchone()
    names = (
        "jobs",
        "pending",
        "processing",
        "completed",
        "failed",
        "attempts",
        "updated",
        "no_change",
        "retained_prompts",
        "redactions",
    )
    result = {name: int(jobs[index]) for index, name in enumerate(names)}
    result["events"] = int(events)
    result["source_cursor"] = int(runtime[0])
    result["worker_lease_active"] = int(runtime[1] is not None)
    return result


def _read_prompt_texts(journal_path: Path) -> list[str]:
    with contextlib.closing(_read_only_connection(journal_path)) as connection:
        rows = connection.execute(
            """
SELECT prompt_text
FROM jobs
WHERE prompt_text IS NOT NULL
ORDER BY event_seq
"""
        ).fetchall()
    return [str(row[0]) for row in rows]


def _wait_for_completion(
    journal_path: Path,
    *,
    completed: int,
) -> tuple[float, dict[str, int]]:
    started = time.monotonic()
    deadline = started + BACKGROUND_TIMEOUT_SECONDS
    counts = _read_counts(journal_path)
    while time.monotonic() < deadline:
        counts = _read_counts(journal_path)
        if (
            counts["completed"] >= completed
            and counts["worker_lease_active"] == 0
        ):
            return (time.monotonic() - started) * 1_000, counts
        if counts["failed"]:
            raise FreshProjectE2EError("background_worker_failed")
        time.sleep(0.05)
    raise FreshProjectE2EError("background_worker_timeout")


def _read_only_connection(path: Path) -> sqlite3.Connection:
    uri = path.resolve(strict=True).as_uri() + "?mode=ro"
    connection = sqlite3.connect(uri, uri=True, timeout=2.0)
    connection.execute("PRAGMA query_only = ON")
    return connection


def _decode_report(value: bytes, category: str) -> dict[str, object]:
    try:
        decoded = json.loads(value.decode("utf-8", errors="strict"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise FreshProjectE2EError(category) from error
    if not isinstance(decoded, dict):
        raise FreshProjectE2EError(category)
    return decoded


def _valid_optional_hook_output(value: bytes, event_name: str) -> bool:
    if not value:
        return True
    try:
        decoded = json.loads(value.decode("utf-8", errors="strict"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return False
    if not isinstance(decoded, Mapping) or decoded.get("continue") is not True:
        return False
    specific = decoded.get("hookSpecificOutput")
    if not isinstance(specific, Mapping):
        return False
    context = specific.get("additionalContext")
    return (
        specific.get("hookEventName") == event_name
        and isinstance(context, str)
        and 'authority="derived"' in context
    )


def _injection_context(value: Mapping[str, object]) -> str:
    specific = value.get("hookSpecificOutput")
    if not isinstance(specific, Mapping):
        raise FreshProjectE2EError("session_injection_invalid")
    context = specific.get("additionalContext")
    if not isinstance(context, str) or not context:
        raise FreshProjectE2EError("session_injection_invalid")
    return context


def _read_sqlite_text(path: Path) -> str:
    values = []
    with contextlib.closing(_read_only_connection(path)) as connection:
        tables = [
            str(row[0])
            for row in connection.execute(
                """
SELECT name
FROM sqlite_schema
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
ORDER BY name
"""
            )
        ]
        for table in tables:
            columns = [
                str(row[1])
                for row in connection.execute(
                    f"PRAGMA table_info({_quote_identifier(table)})"
                )
                if "TEXT" in str(row[2]).upper()
            ]
            for column in columns:
                query = (
                    f"SELECT {_quote_identifier(column)} "
                    f"FROM {_quote_identifier(table)} "
                    f"WHERE {_quote_identifier(column)} IS NOT NULL"
                )
                values.extend(str(row[0]) for row in connection.execute(query))
    return "\n".join(values)


def _quote_identifier(value: str) -> str:
    return '"' + value.replace('"', '""') + '"'


def _read_logs(roots: Sequence[Path]) -> str:
    content = []
    seen = set()
    for root in roots:
        if not root.exists():
            continue
        for path in root.rglob("*.log"):
            resolved = path.resolve(strict=True)
            if resolved in seen or not resolved.is_file():
                continue
            seen.add(resolved)
            try:
                content.append(
                    resolved.read_text(encoding="utf-8", errors="replace")
                )
            except OSError as error:
                raise FreshProjectE2EError("log_scan_failed") from error
    return "\n".join(content)


def _text_matches(value: str, secrets: Sequence[str]) -> int:
    return sum(value.count(secret) for secret in secrets)


def _byte_matches(value: bytes, secrets: Sequence[str]) -> int:
    return sum(value.count(secret.encode("utf-8")) for secret in secrets)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run the isolated fresh-project release Gate.",
    )
    parser.add_argument(
        "--source-root",
        type=Path,
        default=SOURCE_ROOT,
    )
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = _parser().parse_args(argv)
    try:
        report = run_fresh_project_e2e(args.source_root)
    except FreshProjectE2EError as error:
        print(
            json.dumps(
                {
                    "schema_version": REPORT_SCHEMA_VERSION,
                    "scenario": "fresh-project-standard",
                    "ok": False,
                    "error": str(error),
                },
                separators=(",", ":"),
            )
        )
        return 1
    print(json.dumps(report, ensure_ascii=False, separators=(",", ":")))
    return 0 if report["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
