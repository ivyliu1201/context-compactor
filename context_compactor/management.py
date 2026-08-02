from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Callable, Mapping, Optional, Sequence, Tuple

from . import __version__
from .journal import SCHEMA_VERSION as JOURNAL_SCHEMA_VERSION
from .paths import PathValue, project_paths, resolve_project_root
from .privacy import PrivacyFilterError, contains_known_secret
from .state import StateError, load_state_file

MANIFEST_VERSION = 1
LEGACY_MANIFEST_VERSION = 1
HOST_CODEX = "codex"
HOST_CLAUDE = "claude"
SUPPORTED_HOSTS = (HOST_CODEX, HOST_CLAUDE)
HOOK_EVENTS = (
    "SessionStart",
    "SubagentStart",
    "UserPromptSubmit",
    "PreCompact",
    "PostCompact",
)
HOOK_TIMEOUT_SECONDS = 30
WORKER_NOT_RUNNING_THRESHOLD = timedelta(seconds=30)

_HOST_EXECUTABLES = {
    HOST_CODEX: "codex",
    HOST_CLAUDE: "claude",
}
_SAFE_VERSION = re.compile(r"[^A-Za-z0-9._-]+")
_SENSITIVE_VALUE_ARGUMENTS = frozenset(
    {
        "api-key",
        "access-token",
        "authorization",
        "client-secret",
        "password",
        "passwd",
        "pwd",
        "secret",
        "token",
    }
)
_Which = Callable[[str], Optional[str]]


class ManagementError(ValueError):
    """Raised when installation state cannot be changed or inspected safely."""


def default_install_root(
    environment: Optional[Mapping[str, str]] = None,
) -> Path:
    values = os.environ if environment is None else environment
    local_app_data = values.get("LOCALAPPDATA", "").strip()
    if not local_app_data:
        raise ManagementError(
            "LOCALAPPDATA is unavailable; specify an install root"
        )
    return Path(local_app_data).resolve(strict=False) / "context-compactor"


def install_source(
    *,
    project_root: PathValue,
    source_root: PathValue,
    install_root: Optional[PathValue],
    python: str,
    hosts: Sequence[str],
    model_command: Optional[Sequence[str]] = None,
    include_user_legacy: bool = False,
    now: Optional[datetime] = None,
    which: _Which = shutil.which,
) -> dict[str, object]:
    return _install_or_update(
        "install",
        project_root=project_root,
        source_root=source_root,
        install_root=install_root,
        python=python,
        hosts=hosts,
        model_command=model_command,
        include_user_legacy=include_user_legacy,
        now=now,
        which=which,
    )


def update_source(
    *,
    project_root: PathValue,
    source_root: PathValue,
    install_root: Optional[PathValue],
    python: str,
    hosts: Sequence[str],
    model_command: Optional[Sequence[str]] = None,
    include_user_legacy: bool = False,
    now: Optional[datetime] = None,
    which: _Which = shutil.which,
) -> dict[str, object]:
    return _install_or_update(
        "update",
        project_root=project_root,
        source_root=source_root,
        install_root=install_root,
        python=python,
        hosts=hosts,
        model_command=model_command,
        include_user_legacy=include_user_legacy,
        now=now,
        which=which,
    )


def status(
    *,
    project_root: PathValue,
    install_root: Optional[PathValue],
    hosts: Sequence[str],
    now: Optional[datetime] = None,
    which: _Which = shutil.which,
) -> dict[str, object]:
    root = resolve_project_root(project_root)
    installation = _resolve_install_root(install_root)
    selected = _normalize_hosts(hosts)
    checked_at = _as_utc(now or datetime.now(timezone.utc))
    report = _collect_status(root, installation, selected, checked_at, which)
    report["issues"] = report.pop("_issues")
    report["notices"] = report.pop("_notices")
    return report


def doctor(
    *,
    project_root: PathValue,
    install_root: Optional[PathValue],
    hosts: Sequence[str],
    now: Optional[datetime] = None,
    which: _Which = shutil.which,
) -> dict[str, object]:
    root = resolve_project_root(project_root)
    installation = _resolve_install_root(install_root)
    selected = _normalize_hosts(hosts)
    checked_at = _as_utc(now or datetime.now(timezone.utc))
    report = _collect_status(
        root,
        installation,
        selected,
        checked_at,
        which,
    )
    issues = list(report.pop("_issues"))
    notices = list(report.pop("_notices"))

    active_source = report["active_source"]
    interpreter = report["python_interpreter"]
    if report["installed"]:
        if not isinstance(active_source, dict) or not Path(
            str(active_source["path"])
        ).is_dir():
            issues.append("active_source_missing")
        if not isinstance(interpreter, str) or not Path(interpreter).is_file():
            issues.append("python_interpreter_missing")
        elif not _probe_installed_python(
            Path(interpreter),
            str(active_source["package_version"])
            if isinstance(active_source, dict)
            else __version__,
        ):
            issues.append("python_self_check_failed")
        wrapper = report["hook_wrapper"]
        if not isinstance(wrapper, str) or not Path(wrapper).is_file():
            issues.append("hook_wrapper_missing")
        if not report["powershell_available"]:
            issues.append("powershell_unavailable")

    for hook in report["hooks"]:
        if not hook["installed"]:
            issues.append(f"{hook['host']}_not_installed")
        elif not hook["definition_healthy"]:
            issues.append(f"{hook['host']}_hook_unhealthy")
        if hook["installed"] and not hook["host_available"]:
            issues.append(f"{hook['host']}_unavailable")
        if hook["installed"] and not hook["model_available"]:
            issues.append(f"{hook['host']}_model_unavailable")

    journal = report["journal"]
    if not journal["initialized"]:
        notices.append("journal_uninitialized")
    elif not journal["healthy"]:
        issues.append("journal_invalid")
    if journal["failed_jobs"] > 0:
        issues.append("failed_jobs_present")
    if journal["worker_not_running"]:
        issues.append("worker_not_running")

    state = report["state"]
    if state["status"] == "missing":
        notices.append("state_missing")
    elif not state["valid"]:
        issues.append("state_invalid")

    report["command"] = "doctor"
    report["healthy"] = not issues
    report["issues"] = _unique(issues)
    report["notices"] = _unique(notices)
    return report


def uninstall(
    *,
    project_root: PathValue,
    install_root: Optional[PathValue],
    hosts: Sequence[str],
) -> dict[str, object]:
    root = resolve_project_root(project_root)
    installation = _resolve_install_root(install_root)
    selected = _normalize_hosts(hosts)
    global_path = installation / "install.json"
    global_manifest = _load_manifest(global_path, "install")
    _validate_install_root(global_manifest, installation)

    project_id = _project_id(root)
    project_path = installation / "projects" / f"{project_id}.json"
    project_manifest = _load_manifest(project_path, "project")
    if Path(str(project_manifest.get("project_root", ""))) != root:
        raise ManagementError("project manifest does not match the project root")

    installed_hosts = _host_definitions(project_manifest)
    changes: dict[Path, Optional[bytes]] = {}
    hook_reports = []
    for host in selected:
        definition = installed_hosts.get(host)
        if not isinstance(definition, dict):
            raise ManagementError(f"{host} is not installed for this project")
        config_path = _host_config_path(root, host)
        if Path(str(definition.get("config_path", ""))) != config_path:
            raise ManagementError(f"{host} Hook configuration path is invalid")
        document, _ = _load_hook_document(config_path)
        counts = _count_owned_hooks(document, definition)
        if any(counts[event] != 1 for event in HOOK_EVENTS):
            raise ManagementError(
                f"{host} Hook definition changed; refusing ambiguous uninstall"
            )
        _remove_owned_hooks(document, definition)
        if bool(definition.get("config_created")) and not document:
            changes[config_path] = None
        else:
            changes[config_path] = _json_bytes(document)
        del installed_hosts[host]
        hook_reports.append(
            {
                "host": host,
                "installed": False,
                "config_path": str(config_path),
            }
        )

    projects = _project_registry(global_manifest)
    installation_removed = False
    cleanup_deferred = False
    if installed_hosts:
        project_manifest["hosts"] = installed_hosts
        changes[project_path] = _json_bytes(project_manifest)
        changes[global_path] = _json_bytes(global_manifest)
    else:
        changes[project_path] = None
        projects.pop(project_id, None)
        global_manifest["projects"] = projects
        if projects:
            changes[global_path] = _json_bytes(global_manifest)
        else:
            changes[global_path] = None

    _apply_file_changes(changes)
    if not projects:
        interpreter = Path(str(global_manifest.get("python_interpreter", "")))
        if _current_process_uses(interpreter):
            cleanup_deferred = True
        else:
            _remove_managed_installation(installation, global_manifest)
            installation_removed = not installation.exists()

    return {
        "command": "uninstall",
        "ok": True,
        "install_root": str(installation),
        "project_root": str(root),
        "hooks": hook_reports,
        "installation_removed": installation_removed,
        "cleanup_deferred": cleanup_deferred,
    }


def _install_or_update(
    command: str,
    *,
    project_root: PathValue,
    source_root: PathValue,
    install_root: Optional[PathValue],
    python: str,
    hosts: Sequence[str],
    model_command: Optional[Sequence[str]],
    include_user_legacy: bool,
    now: Optional[datetime],
    which: _Which,
) -> dict[str, object]:
    root = resolve_project_root(project_root)
    source = Path(source_root).resolve(strict=False)
    installation = _resolve_install_root(install_root)
    selected = _normalize_hosts(hosts)
    timestamp = _format_time(_as_utc(now or datetime.now(timezone.utc)))

    source_files = _source_files(source)
    system_python = _validate_python(python, which)
    resolved_model_command = (
        _validate_model_command(model_command, which)
        if model_command is not None
        else None
    )
    bundled_codex = (
        _required_executable(("codex",), which)
        if resolved_model_command is None
        else None
    )
    powershell = _required_executable(("powershell.exe", "powershell"), which)
    for host in selected:
        _required_executable((_HOST_EXECUTABLES[host],), which)

    global_path = installation / "install.json"
    current_global = _load_optional_manifest(global_path, "install")
    if command == "update" and current_global is None:
        raise ManagementError("update requires an existing source installation")
    if current_global is not None:
        _validate_install_root(current_global, installation)

    project_id = _project_id(root)
    project_path = installation / "projects" / f"{project_id}.json"
    current_project = _load_optional_manifest(project_path, "project")
    if command == "update" and current_project is None:
        raise ManagementError("update requires an installed project")
    if current_project is not None and Path(
        str(current_project.get("project_root", ""))
    ) != root:
        raise ManagementError("project manifest does not match the project root")
    if current_project is not None:
        _host_definitions(current_project)

    hook_documents = {}
    for host in selected:
        config_path = _host_config_path(root, host)
        document, config_existed = _load_hook_document(config_path)
        if host == HOST_CLAUDE and document.get("disableAllHooks") is True:
            raise ManagementError("Claude hooks are disabled")
        hook_documents[host] = (config_path, document, config_existed)

    legacy_hooks_removed = 0
    legacy_changes: dict[Path, Optional[bytes]] = {}
    for legacy_root, host, definition in _legacy_hook_installations(
        root,
        selected,
        include_user_legacy,
    ):
        config_path = _host_config_path(legacy_root, host)
        current_config_path, current_document, _ = hook_documents[host]
        if config_path == current_config_path:
            document = current_document
        else:
            document, _ = _load_hook_document(config_path)
        removed = sum(_count_owned_hooks(document, definition).values())
        if not removed:
            continue
        _remove_owned_hooks(document, definition)
        legacy_hooks_removed += removed
        if config_path != current_config_path:
            legacy_changes[config_path] = (
                None
                if definition["config_created"] and not document
                else _json_bytes(document)
            )

    source_id = _source_digest(source, source_files)
    version_name = (
        f"{_SAFE_VERSION.sub('-', __version__)}-{source_id[:12]}"
    )
    version_root = installation / "versions" / version_name
    source_created = _copy_versioned_source(
        source,
        source_files,
        version_root,
        source_id,
    )
    wrapper = installation / "context-compactor-hook.ps1"
    wrapper_content = (source / "scripts" / wrapper.name).read_bytes()
    (
        python_interpreter,
        active_pointer,
        active_pointer_content,
        environment_created,
    ) = _prepare_private_venv(
        installation,
        system_python,
        version_root,
    )
    if resolved_model_command is None:
        if bundled_codex is None:
            raise ManagementError("required codex executable is unavailable")
        resolved_model_command = _validate_model_command(
            (
                str(python_interpreter),
                "-m",
                "context_compactor.codex_adapter",
                "--codex-command",
                str(bundled_codex),
            ),
            which,
        )
        model_adapter = "bundled_codex"
    else:
        model_adapter = "external"

    global_manifest = current_global or {
        "schema_version": MANIFEST_VERSION,
        "install_root": str(installation),
        "projects": {},
        "installed_at": timestamp,
    }
    _validate_install_root(global_manifest, installation)
    previous_source_id = global_manifest.get("source_id")
    global_manifest.update(
        {
            "schema_version": MANIFEST_VERSION,
            "install_root": str(installation),
            "package_version": __version__,
            "source_id": source_id,
            "source_root": str(source),
            "version_root": str(version_root),
            "python_interpreter": str(python_interpreter),
            "hook_wrapper": str(wrapper),
            "updated_at": timestamp,
        }
    )

    project_manifest = current_project or {
        "schema_version": MANIFEST_VERSION,
        "project_id": project_id,
        "project_root": str(root),
        "install_root": str(installation),
        "hosts": {},
        "installed_at": timestamp,
    }
    installed_hosts = _host_definitions(project_manifest)
    changes: dict[Path, Optional[bytes]] = dict(legacy_changes)
    for host in selected:
        config_path, document, config_existed = hook_documents[host]
        previous = installed_hosts.get(host)
        if isinstance(previous, dict):
            _remove_owned_hooks(document, previous)
            config_created = bool(previous.get("config_created"))
        else:
            config_created = not config_existed
        hook_command, command_windows = _hook_commands(
            powershell,
            wrapper,
            project_path,
            host,
        )
        definition = {
            "config_path": str(config_path),
            "command": hook_command,
            "command_windows": command_windows,
            "config_created": config_created,
        }
        _add_owned_hooks(document, host, definition)
        installed_hosts[host] = definition
        changes[config_path] = _json_bytes(document)

    project_manifest.update(
        {
            "schema_version": MANIFEST_VERSION,
            "project_id": project_id,
            "project_root": str(root),
            "install_root": str(installation),
            "python_interpreter": str(python_interpreter),
            "model_command": list(resolved_model_command),
            "model_adapter": model_adapter,
            "hosts": installed_hosts,
            "updated_at": timestamp,
        }
    )
    projects = _project_registry(global_manifest)
    projects[project_id] = str(project_path)
    global_manifest["projects"] = projects
    changes[active_pointer] = active_pointer_content
    changes[wrapper] = wrapper_content
    changes[project_path] = _json_bytes(project_manifest)
    changes[global_path] = _json_bytes(global_manifest)
    try:
        _apply_file_changes(changes)
    except OSError as error:
        if environment_created:
            shutil.rmtree(installation / ".venv", ignore_errors=True)
        raise ManagementError("failed to write installation files") from error
    except BaseException:
        if environment_created:
            shutil.rmtree(installation / ".venv", ignore_errors=True)
        raise

    report = _collect_status(
        root,
        installation,
        selected,
        _as_utc(now or datetime.now(timezone.utc)),
        which,
    )
    report.pop("_issues")
    report.pop("_notices")
    report.update(
        {
            "command": command,
            "ok": True,
            "source_created": source_created,
            "source_changed": previous_source_id != source_id,
            "legacy_hooks_removed": legacy_hooks_removed,
            "model_adapter": model_adapter,
        }
    )
    return report


def _collect_status(
    root: Path,
    installation: Path,
    selected: Tuple[str, ...],
    now: datetime,
    which: _Which,
) -> dict[str, object]:
    issues = []
    notices = []
    global_path = installation / "install.json"
    try:
        global_manifest = _load_optional_manifest(global_path, "install")
    except ManagementError:
        global_manifest = None
        issues.append("install_manifest_invalid")

    project_path = (
        installation / "projects" / f"{_project_id(root)}.json"
    )
    try:
        project_manifest = _load_optional_manifest(project_path, "project")
    except ManagementError:
        project_manifest = None
        issues.append("project_manifest_invalid")

    active_source = None
    python_interpreter = None
    wrapper = None
    if global_manifest is not None:
        try:
            _validate_install_root(global_manifest, installation)
            active_source = {
                "package_version": str(
                    global_manifest.get("package_version", "")
                ),
                "source_id": str(global_manifest.get("source_id", "")),
                "path": str(global_manifest.get("version_root", "")),
            }
            python_interpreter = str(
                global_manifest.get("python_interpreter", "")
            )
            wrapper = str(global_manifest.get("hook_wrapper", ""))
        except ManagementError:
            issues.append("install_manifest_invalid")

    installed_hosts = (
        _host_definitions(project_manifest)
        if project_manifest is not None
        else {}
    )
    model_command = (
        project_manifest.get("model_command", [])
        if project_manifest is not None
        else []
    )
    model_adapter = (
        str(project_manifest.get("model_adapter", "external"))
        if project_manifest is not None
        else None
    )
    model_available = _model_command_available(
        model_command,
        model_adapter,
        which,
    )
    hook_reports = []
    for host in selected:
        definition = installed_hosts.get(host)
        hook_issues = []
        counts = {event: 0 for event in HOOK_EVENTS}
        document = None
        if isinstance(definition, dict):
            config_path = _host_config_path(root, host)
            if Path(str(definition.get("config_path", ""))) != config_path:
                hook_issues.append("hook_config_path_mismatch")
            else:
                try:
                    document, _ = _load_hook_document(config_path)
                    counts = _count_owned_hooks(document, definition)
                    if (
                        host == HOST_CLAUDE
                        and document.get("disableAllHooks") is True
                    ):
                        hook_issues.append("hooks_disabled")
                except ManagementError:
                    hook_issues.append("hook_config_invalid")
            definition_mismatch = any(
                counts[event] != 1 for event in HOOK_EVENTS
            )
            if (
                host == HOST_CODEX
                and document is not None
                and _count_codex_startup_hooks(document, definition) != 1
            ):
                definition_mismatch = True
            if definition_mismatch:
                hook_issues.append("hook_definition_mismatch")
        else:
            config_path = _host_config_path(root, host)
        installed = isinstance(definition, dict)
        definition_healthy = installed and not hook_issues
        hook_state = "not_installed"
        if installed and not definition_healthy:
            hook_state = "unhealthy"
        elif installed and host == HOST_CODEX:
            hook_state = "awaiting_manual_trust"
        elif installed:
            hook_state = "definition_ready"
        hook_reports.append(
            {
                "host": host,
                "installed": installed,
                "definition_healthy": definition_healthy,
                "state": hook_state,
                "manual_trust_required": host == HOST_CODEX,
                "host_activation_unknown": installed,
                "host_available": _executable_available(
                    _HOST_EXECUTABLES[host], which
                ),
                "model_available": model_available,
                "config_path": str(config_path),
                "command": (
                    str(definition.get("command", ""))
                    if isinstance(definition, dict)
                    else ""
                ),
                "command_windows": (
                    str(definition.get("command_windows", ""))
                    if isinstance(definition, dict)
                    else ""
                ),
                "events": counts,
                "issues": hook_issues,
            }
        )

    journal = _journal_status(project_paths(root).journal, now)
    state = _state_status(project_paths(root).state)
    installed = global_manifest is not None and project_manifest is not None
    report = {
        "command": "status",
        "ok": not issues,
        "installed": installed,
        "install_root": str(installation),
        "python_interpreter": python_interpreter,
        "interpreter_present": bool(python_interpreter)
        and Path(str(python_interpreter)).is_file(),
        "active_source": active_source,
        "hook_wrapper": wrapper,
        "model_adapter": model_adapter,
        "powershell_available": _any_executable_available(
            ("powershell.exe", "powershell"), which
        ),
        "project_root": str(root),
        "hooks": hook_reports,
        "journal": journal,
        "state": state,
        "last_valid_state_cursor": (
            state["source_cursor"] if state["valid"] else None
        ),
        "last_worker_activity": journal["last_worker_activity_at"],
        "_issues": issues,
        "_notices": notices,
    }
    return report


def _journal_status(path: Path, now: datetime) -> dict[str, object]:
    report: dict[str, object] = {
        "initialized": False,
        "healthy": True,
        "schema_version": None,
        "pending_jobs": 0,
        "processing_jobs": 0,
        "completed_updated": 0,
        "completed_no_change": 0,
        "failed_jobs": 0,
        "attempts": 0,
        "worker_lease_active": False,
        "worker_lease_until": None,
        "worker_not_running": False,
        "oldest_pending_at": None,
        "last_worker_activity_at": None,
        "source_cursor": None,
    }
    if not path.is_file():
        return report
    report["initialized"] = True
    try:
        uri = path.resolve(strict=True).as_uri() + "?mode=ro"
        connection = sqlite3.connect(uri, uri=True, timeout=1)
        connection.row_factory = sqlite3.Row
        try:
            connection.execute("PRAGMA query_only = ON")
            migration = connection.execute(
                "SELECT MAX(version) AS version FROM schema_migrations"
            ).fetchone()
            schema_version = (
                int(migration["version"])
                if migration is not None and migration["version"] is not None
                else 0
            )
            report["schema_version"] = schema_version
            if schema_version != JOURNAL_SCHEMA_VERSION:
                report["healthy"] = False
                return report
            counts = connection.execute(
                """
SELECT
    SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) AS pending_jobs,
    SUM(CASE WHEN status = 'processing' THEN 1 ELSE 0 END) AS processing_jobs,
    SUM(CASE WHEN status = 'completed' AND outcome = 'updated'
        THEN 1 ELSE 0 END) AS completed_updated,
    SUM(CASE WHEN status = 'completed' AND outcome = 'no_change'
        THEN 1 ELSE 0 END) AS completed_no_change,
    SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed_jobs,
    COALESCE(SUM(attempt_count), 0) AS attempts
FROM jobs
"""
            ).fetchone()
            for name in (
                "pending_jobs",
                "processing_jobs",
                "completed_updated",
                "completed_no_change",
                "failed_jobs",
                "attempts",
            ):
                report[name] = int(counts[name] or 0)
            oldest = connection.execute(
                """
SELECT enqueued_at
FROM jobs
WHERE status = 'pending'
ORDER BY julianday(enqueued_at), event_seq
LIMIT 1
"""
            ).fetchone()
            runtime = connection.execute(
                """
SELECT source_cursor, worker_lease_token, worker_lease_until,
       last_worker_activity_at
FROM runtime_state
WHERE singleton = 1
"""
            ).fetchone()
            if runtime is None:
                raise ManagementError("journal runtime state is missing")
            lease_until = _parse_optional_time(runtime["worker_lease_until"])
            lease_active = (
                bool(runtime["worker_lease_token"])
                and lease_until is not None
                and lease_until > now
            )
            oldest_at = (
                _parse_optional_time(oldest["enqueued_at"])
                if oldest is not None
                else None
            )
            last_activity = _parse_optional_time(
                runtime["last_worker_activity_at"]
            )
            report.update(
                {
                    "worker_lease_active": lease_active,
                    "worker_lease_until": _optional_time_text(lease_until),
                    "oldest_pending_at": _optional_time_text(oldest_at),
                    "last_worker_activity_at": _optional_time_text(
                        last_activity
                    ),
                    "source_cursor": int(runtime["source_cursor"]),
                    "worker_not_running": bool(
                        int(report["pending_jobs"]) > 0
                        and oldest_at is not None
                        and now - oldest_at
                        >= WORKER_NOT_RUNNING_THRESHOLD
                        and not lease_active
                    ),
                }
            )
        finally:
            connection.close()
    except (ManagementError, OSError, sqlite3.Error, TypeError, ValueError):
        report["healthy"] = False
    return report


def _state_status(path: Path) -> dict[str, object]:
    if not path.is_file():
        return {
            "status": "missing",
            "valid": False,
            "source_cursor": None,
        }
    try:
        state = load_state_file(path)
    except (OSError, StateError, UnicodeError, ValueError):
        return {
            "status": "invalid",
            "valid": False,
            "source_cursor": None,
        }
    return {
        "status": "valid",
        "valid": True,
        "source_cursor": state.metadata.source_cursor,
    }


def _source_files(source: Path) -> Tuple[Path, ...]:
    package = source / "context_compactor"
    lock = source / "requirements.lock"
    wrapper = source / "scripts" / "context-compactor-hook.ps1"
    if not package.is_dir() or not lock.is_file() or not wrapper.is_file():
        raise ManagementError(
            "source root is missing the Python package, lock file, or Hook wrapper"
        )
    files = tuple(sorted(package.rglob("*.py"))) + (lock, wrapper)
    if any(path.is_symlink() or not path.is_file() for path in files):
        raise ManagementError("source bundle contains an unsupported file")
    return files


def _source_digest(source: Path, files: Sequence[Path]) -> str:
    digest = hashlib.sha256()
    for path in files:
        relative = path.relative_to(source).as_posix().encode("utf-8")
        digest.update(len(relative).to_bytes(4, "big"))
        digest.update(relative)
        content = path.read_bytes()
        digest.update(len(content).to_bytes(8, "big"))
        digest.update(content)
    return digest.hexdigest()


def _copy_versioned_source(
    source: Path,
    files: Sequence[Path],
    target: Path,
    source_id: str,
) -> bool:
    marker = target / "source.json"
    if target.exists():
        existing = _load_manifest(marker, "source")
        if (
            existing.get("source_id") != source_id
            or existing.get("package_version") != __version__
        ):
            raise ManagementError("versioned source directory is inconsistent")
        return False

    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.parent / f".source-{uuid.uuid4().hex}.tmp"
    try:
        temporary.mkdir()
        for path in files:
            relative = path.relative_to(source)
            if relative.parts[:1] == ("scripts",):
                continue
            destination = temporary / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, destination)
        _atomic_write_bytes(
            temporary / "source.json",
            _json_bytes(
                {
                    "schema_version": MANIFEST_VERSION,
                    "package_version": __version__,
                    "source_id": source_id,
                }
            ),
        )
        os.replace(temporary, target)
    except BaseException:
        if temporary.exists():
            shutil.rmtree(temporary)
        raise
    return True


def _prepare_private_venv(
    installation: Path,
    system_python: Path,
    version_root: Path,
) -> Tuple[Path, Path, bytes, bool]:
    venv_root = installation / ".venv"
    interpreter = _venv_python(venv_root)
    environment_created = not venv_root.exists()
    try:
        if environment_created:
            _run_checked(
                (str(system_python), "-m", "venv", str(venv_root)),
                "create private Python environment",
                timeout=180,
            )
        elif not interpreter.is_file():
            raise ManagementError("private Python environment is incomplete")

        _run_checked(
            (
                str(interpreter),
                "-m",
                "pip",
                "install",
                "--disable-pip-version-check",
                "--no-input",
                "--require-hashes",
                "--no-deps",
                "-r",
                str(version_root / "requirements.lock"),
            ),
            "install pinned runtime requirements",
            timeout=180,
        )
        purelib_result = _run_checked(
            (
                str(interpreter),
                "-c",
                "import sysconfig; print(sysconfig.get_paths()['purelib'])",
            ),
            "resolve private Python library path",
        )
        purelib = Path(purelib_result.stdout.strip()).resolve(strict=False)
        if not _is_within(purelib, venv_root):
            raise ManagementError(
                "private Python library path is outside the install root"
            )
        pointer = purelib / "context_compactor_active.pth"
        pointer_content = (str(version_root) + os.linesep).encode("utf-8")
        previous = pointer.read_bytes() if pointer.is_file() else None
        _atomic_write_bytes(pointer, pointer_content)
        try:
            healthy = _probe_installed_python(interpreter, __version__)
        finally:
            if previous is None:
                pointer.unlink(missing_ok=True)
            else:
                _atomic_write_bytes(pointer, previous)
        if not healthy:
            raise ManagementError("installed Python self-check failed")
        return (
            interpreter.resolve(strict=True),
            pointer,
            pointer_content,
            environment_created,
        )
    except BaseException:
        if environment_created:
            shutil.rmtree(venv_root, ignore_errors=True)
        raise


def _validate_python(value: str, which: _Which) -> Path:
    resolved = _resolve_executable(value, which)
    result = _run_checked(
        (
            str(resolved),
            "-c",
            "import sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}')",
        ),
        "inspect Python version",
    )
    try:
        major, minor = (
            int(part) for part in result.stdout.strip().split(".", maxsplit=1)
        )
    except (TypeError, ValueError) as error:
        raise ManagementError("Python returned an invalid version") from error
    if (major, minor) < (3, 9):
        raise ManagementError("Python 3.9 or newer is required")
    return resolved


def _probe_installed_python(interpreter: Path, expected_version: str) -> bool:
    try:
        result = subprocess.run(
            (str(interpreter), "-m", "context_compactor", "self-check"),
            capture_output=True,
            check=False,
            text=True,
            timeout=30,
        )
        payload = json.loads(result.stdout)
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
        return False
    return (
        result.returncode == 0
        and isinstance(payload, dict)
        and payload.get("ok") is True
        and payload.get("version") == expected_version
    )


def _validate_model_command(
    value: Sequence[str],
    which: _Which,
) -> Tuple[str, ...]:
    if not value:
        raise ManagementError("a model command is required")
    command = []
    for argument in value:
        if (
            not isinstance(argument, str)
            or not argument
            or len(argument) > 4096
            or any(ord(character) < 32 for character in argument)
        ):
            raise ManagementError("model command contains an invalid argument")
        command.append(argument)
    for index, argument in enumerate(command[:-1]):
        normalized = argument.lstrip("-/").casefold().replace("_", "-")
        if normalized in _SENSITIVE_VALUE_ARGUMENTS:
            raise ManagementError(
                "model command contains a defined secret pattern"
            )
    try:
        if contains_known_secret(" ".join(command)):
            raise ManagementError(
                "model command contains a defined secret pattern"
            )
    except PrivacyFilterError as error:
        raise ManagementError("model command is not safe to persist") from error
    command[0] = str(_resolve_executable(command[0], which))
    return tuple(command)


def _host_config_path(project_root: Path, host: str) -> Path:
    if host == HOST_CODEX:
        return project_root / ".codex" / "hooks.json"
    if host == HOST_CLAUDE:
        return project_root / ".claude" / "settings.local.json"
    raise ManagementError("unsupported Host")


def _hook_commands(
    powershell: Path,
    wrapper: Path,
    project_manifest: Path,
    host: str,
) -> Tuple[str, str]:
    arguments = (
        str(powershell),
        "-NoProfile",
        "-ExecutionPolicy",
        "Bypass",
        "-File",
        str(wrapper),
        "-Manifest",
        str(project_manifest),
        "-AgentHost",
        host,
    )
    command = " ".join(_quote_hook_argument(argument) for argument in arguments)
    return command, f"& {command}" if host == HOST_CODEX else ""


def _quote_hook_argument(value: str) -> str:
    if '"' in value or any(ord(character) < 32 for character in value):
        raise ManagementError("Hook command path contains unsupported characters")
    return f'"{value}"'


def _load_hook_document(path: Path) -> Tuple[dict[str, object], bool]:
    if not path.exists():
        return {}, False
    document = _load_json_object(path, "Hook configuration")
    hooks = _hooks_object(document, create=False)
    for event in HOOK_EVENTS:
        for group in _hook_groups(hooks, event):
            _group_handlers(group, event)
    return document, True


def _add_owned_hooks(
    document: dict[str, object],
    host: str,
    definition: Mapping[str, object],
) -> None:
    hooks = _hooks_object(document, create=True)
    for event in HOOK_EVENTS:
        groups = _hook_groups(hooks, event)
        handler: dict[str, object] = {
            "type": "command",
            "command": definition["command"],
            "timeout": HOOK_TIMEOUT_SECONDS,
        }
        if host == HOST_CODEX:
            handler["statusMessage"] = "Loading bounded project context"
            handler["commandWindows"] = definition["command_windows"]
        group: dict[str, object] = {"hooks": [handler]}
        if host == HOST_CODEX and event == "SessionStart":
            group["matcher"] = "^startup$"
        groups.append(group)
        hooks[event] = groups


def _count_owned_hooks(
    document: dict[str, object],
    definition: Mapping[str, object],
) -> dict[str, int]:
    hooks = _hooks_object(document, create=False)
    counts = {}
    for event in HOOK_EVENTS:
        counts[event] = sum(
            1
            for group in _hook_groups(hooks, event)
            for handler in _group_handlers(group, event)
            if _owned_handler(handler, definition)
        )
    return counts


def _count_codex_startup_hooks(
    document: dict[str, object],
    definition: Mapping[str, object],
) -> int:
    hooks = _hooks_object(document, create=False)
    return sum(
        1
        for group in _hook_groups(hooks, "SessionStart")
        if group.get("matcher") == "^startup$"
        for handler in _group_handlers(group, "SessionStart")
        if _owned_handler(handler, definition)
    )


def _remove_owned_hooks(
    document: dict[str, object],
    definition: Mapping[str, object],
) -> None:
    hooks = _hooks_object(document, create=False)
    for event in HOOK_EVENTS:
        kept_groups = []
        for group in _hook_groups(hooks, event):
            handlers = [
                handler
                for handler in _group_handlers(group, event)
                if not _owned_handler(handler, definition)
            ]
            if handlers:
                group["hooks"] = handlers
                kept_groups.append(group)
        if kept_groups:
            hooks[event] = kept_groups
        else:
            hooks.pop(event, None)
    if not hooks:
        document.pop("hooks", None)


def _hooks_object(
    document: dict[str, object],
    *,
    create: bool,
) -> dict[str, object]:
    if "hooks" not in document:
        if create:
            hooks: dict[str, object] = {}
            document["hooks"] = hooks
            return hooks
        return {}
    raw = document["hooks"]
    if not isinstance(raw, dict):
        raise ManagementError("hooks must be a JSON object")
    return raw


def _hook_groups(
    hooks: Mapping[str, object],
    event: str,
) -> list[dict[str, object]]:
    if event not in hooks:
        return []
    raw = hooks[event]
    if not isinstance(raw, list) or any(
        not isinstance(group, dict) for group in raw
    ):
        raise ManagementError(f"hooks.{event} must contain JSON objects")
    return raw


def _group_handlers(
    group: Mapping[str, object],
    event: str,
) -> list[dict[str, object]]:
    raw = group.get("hooks")
    if not isinstance(raw, list) or any(
        not isinstance(handler, dict) for handler in raw
    ):
        raise ManagementError(f"hooks.{event} handlers must be JSON objects")
    return raw


def _owned_handler(
    handler: Mapping[str, object],
    definition: Mapping[str, object],
) -> bool:
    if (
        handler.get("type") != "command"
        or handler.get("command") != definition.get("command")
    ):
        return False
    command_windows = definition.get("command_windows")
    return not command_windows or (
        handler.get("commandWindows") == command_windows
    )


def _legacy_hook_installations(
    project_root: Path,
    selected: Sequence[str],
    include_user_legacy: bool,
) -> Tuple[Tuple[Path, str, dict[str, object]], ...]:
    installations = []
    for root in _legacy_install_roots(project_root, include_user_legacy):
        path = root / ".context-compactor" / "install.json"
        if not path.exists():
            continue
        document = _load_json_object(path, "legacy install manifest")
        if "version" not in document:
            continue
        if document.get("version") != LEGACY_MANIFEST_VERSION:
            raise ManagementError("legacy install manifest version is unsupported")
        hosts = document.get("hosts")
        if not isinstance(hosts, dict):
            raise ManagementError("legacy install manifest hosts are invalid")
        for host in selected:
            raw = hosts.get(host)
            if raw is None:
                continue
            if not isinstance(raw, dict):
                raise ManagementError(
                    "legacy install manifest host definition is invalid"
                )
            command = raw.get("command")
            command_windows = raw.get("command_windows", "")
            config_created = raw.get("config_created")
            if (
                not isinstance(command, str)
                or not command.strip()
                or len(command) > 32_768
                or any(ord(character) < 32 for character in command)
                or not isinstance(command_windows, str)
                or len(command_windows) > 32_768
                or any(ord(character) < 32 for character in command_windows)
                or not isinstance(config_created, bool)
            ):
                raise ManagementError(
                    "legacy install manifest host definition is invalid"
                )
            installations.append(
                (
                    root,
                    host,
                    {
                        "command": command,
                        "command_windows": command_windows,
                        "config_created": config_created,
                    },
                )
            )
    return tuple(installations)


def _legacy_install_roots(
    project_root: Path,
    include_user_legacy: bool,
) -> Tuple[Path, ...]:
    roots = [project_root.resolve(strict=False)]
    if not include_user_legacy:
        return tuple(roots)
    try:
        home = Path.home().resolve(strict=False)
    except (OSError, RuntimeError):
        return tuple(roots)
    if home not in roots:
        roots.append(home)
    return tuple(roots)


def _load_optional_manifest(
    path: Path,
    name: str,
) -> Optional[dict[str, object]]:
    if not path.exists():
        return None
    return _load_manifest(path, name)


def _load_manifest(path: Path, name: str) -> dict[str, object]:
    document = _load_json_object(path, f"{name} manifest")
    if document.get("schema_version") != MANIFEST_VERSION:
        raise ManagementError(f"{name} manifest version is unsupported")
    return document


def _load_json_object(path: Path, name: str) -> dict[str, object]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ManagementError(f"{name} is unavailable or invalid") from error
    if not isinstance(value, dict):
        raise ManagementError(f"{name} must be a JSON object")
    return value


def _host_definitions(
    manifest: Optional[Mapping[str, object]],
) -> dict[str, object]:
    if manifest is None:
        return {}
    value = manifest.get("hosts")
    if not isinstance(value, dict):
        raise ManagementError("project manifest hosts are invalid")
    return value


def _project_registry(manifest: Mapping[str, object]) -> dict[str, object]:
    value = manifest.get("projects")
    if not isinstance(value, dict):
        raise ManagementError("install manifest projects are invalid")
    return value


def _validate_install_root(
    manifest: Mapping[str, object],
    installation: Path,
) -> None:
    if manifest.get("schema_version") != MANIFEST_VERSION:
        raise ManagementError("install manifest version is unsupported")
    if Path(str(manifest.get("install_root", ""))) != installation:
        raise ManagementError("install manifest does not match the install root")
    _project_registry(manifest)


def _apply_file_changes(
    changes: Mapping[Path, Optional[bytes]],
) -> None:
    snapshots = {
        path: path.read_bytes() if path.is_file() else None for path in changes
    }
    try:
        for path, content in changes.items():
            if content is None:
                path.unlink(missing_ok=True)
            else:
                _atomic_write_bytes(path, content)
    except BaseException:
        for path, content in reversed(tuple(snapshots.items())):
            try:
                if content is None:
                    path.unlink(missing_ok=True)
                else:
                    _atomic_write_bytes(path, content)
            except OSError:
                pass
        raise


def _atomic_write_bytes(path: Path, content: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=f".{path.name}-",
        suffix=".tmp",
        dir=str(path.parent),
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def _json_bytes(value: object) -> bytes:
    return (
        json.dumps(
            value,
            ensure_ascii=False,
            indent=2,
            sort_keys=True,
        )
        + "\n"
    ).encode("utf-8")


def _remove_managed_installation(
    installation: Path,
    manifest: Mapping[str, object],
) -> None:
    _validate_install_root(manifest, installation)
    for directory in (
        installation / "versions",
        installation / ".venv",
        installation / "projects",
    ):
        if directory.is_dir() and _is_within(directory, installation):
            shutil.rmtree(directory)
    wrapper = installation / "context-compactor-hook.ps1"
    wrapper.unlink(missing_ok=True)
    try:
        installation.rmdir()
    except OSError:
        pass


def _current_process_uses(interpreter: Path) -> bool:
    try:
        installed = os.path.normcase(
            str(interpreter.resolve(strict=False))
        )
        current = os.path.normcase(
            str(Path(sys.executable).resolve(strict=False))
        )
        return installed == current
    except OSError:
        return False


def _resolve_install_root(value: Optional[PathValue]) -> Path:
    root = (
        default_install_root()
        if value is None
        else Path(os.path.expandvars(os.path.expanduser(os.fspath(value))))
    )
    resolved = root.resolve(strict=False)
    if resolved == Path(resolved.anchor):
        raise ManagementError("install root cannot be a filesystem root")
    return resolved


def _project_id(project_root: Path) -> str:
    identity = os.path.normcase(str(project_root)).encode("utf-8")
    return hashlib.sha256(identity).hexdigest()


def _normalize_hosts(hosts: Sequence[str]) -> Tuple[str, ...]:
    values = tuple(hosts)
    if values == ("all",):
        return SUPPORTED_HOSTS
    if not values:
        raise ManagementError("at least one Host is required")
    if "all" in values:
        raise ManagementError("all cannot be combined with another Host")
    unknown = set(values) - set(SUPPORTED_HOSTS)
    if unknown:
        raise ManagementError("unsupported Host")
    return tuple(host for host in SUPPORTED_HOSTS if host in values)


def _resolve_executable(value: str, which: _Which) -> Path:
    if not isinstance(value, str) or not value.strip():
        raise ManagementError("executable path is required")
    expanded = os.path.expandvars(os.path.expanduser(value.strip()))
    candidate = Path(expanded)
    if candidate.is_absolute() or candidate.parent != Path("."):
        if not candidate.is_file():
            raise ManagementError("required executable is unavailable")
        return candidate.resolve(strict=True)
    resolved = which(expanded)
    if not resolved or not Path(resolved).is_file():
        raise ManagementError("required executable is unavailable")
    return Path(resolved).resolve(strict=True)


def _required_executable(
    names: Sequence[str],
    which: _Which,
) -> Path:
    for name in names:
        resolved = which(name)
        if resolved and Path(resolved).is_file():
            return Path(resolved).resolve(strict=True)
    raise ManagementError(f"required {names[0]} executable is unavailable")


def _executable_available(name: str, which: _Which) -> bool:
    resolved = which(name)
    return bool(resolved) and Path(str(resolved)).is_file()


def _any_executable_available(
    names: Sequence[str],
    which: _Which,
) -> bool:
    return any(_executable_available(name, which) for name in names)


def _command_available(value: object, which: _Which) -> bool:
    if not isinstance(value, list) or not value or not isinstance(value[0], str):
        return False
    try:
        _resolve_executable(value[0], which)
    except ManagementError:
        return False
    return True


def _model_command_available(
    value: object,
    model_adapter: object,
    which: _Which,
) -> bool:
    if not _command_available(value, which):
        return False
    if model_adapter != "bundled_codex":
        return True
    if not isinstance(value, list) or value[1:3] != [
        "-m",
        "context_compactor.codex_adapter",
    ]:
        return False
    try:
        option = value.index("--codex-command", 3)
        command = value[option + 1]
    except (IndexError, ValueError):
        return False
    if not isinstance(command, str):
        return False
    try:
        _resolve_executable(command, which)
    except ManagementError:
        return False
    return True


def _venv_python(venv_root: Path) -> Path:
    windows = venv_root / "Scripts" / "python.exe"
    return windows if os.name == "nt" else venv_root / "bin" / "python"


def _run_checked(
    command: Sequence[str],
    operation: str,
    *,
    timeout: int = 30,
) -> subprocess.CompletedProcess[str]:
    try:
        result = subprocess.run(
            tuple(command),
            capture_output=True,
            check=False,
            text=True,
            timeout=timeout,
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise ManagementError(f"failed to {operation}") from error
    if result.returncode != 0:
        raise ManagementError(f"failed to {operation}")
    return result


def _parse_optional_time(value: object) -> Optional[datetime]:
    if value is None:
        return None
    parsed = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamp must include a timezone")
    return parsed.astimezone(timezone.utc)


def _optional_time_text(value: Optional[datetime]) -> Optional[str]:
    return _format_time(value) if value is not None else None


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        raise ManagementError("management timestamp must include a timezone")
    return value.astimezone(timezone.utc)


def _format_time(value: datetime) -> str:
    return value.isoformat(timespec="microseconds").replace("+00:00", "Z")


def _is_within(path: Path, parent: Path) -> bool:
    try:
        path.resolve(strict=False).relative_to(parent.resolve(strict=False))
    except ValueError:
        return False
    return True


def _unique(values: Sequence[str]) -> list[str]:
    return list(dict.fromkeys(values))
