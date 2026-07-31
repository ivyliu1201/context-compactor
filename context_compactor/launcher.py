from __future__ import annotations

import os
import subprocess
import sys
import threading
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Mapping, MutableMapping, Optional, Sequence

from .journal import Journal
from .paths import PathValue, resolve_project_root
from .privacy import PrivacyFilterError, contains_known_secret

WINDOWS_DETACHED_PROCESS = 0x00000008
WINDOWS_CREATE_NEW_PROCESS_GROUP = 0x00000200
WINDOWS_CREATE_BREAKAWAY_FROM_JOB = 0x01000000
WINDOWS_CREATE_NO_WINDOW = 0x08000000
WINDOWS_PRIMARY_CREATION_FLAGS = (
    WINDOWS_DETACHED_PROCESS
    | WINDOWS_CREATE_NEW_PROCESS_GROUP
    | WINDOWS_CREATE_BREAKAWAY_FROM_JOB
    | WINDOWS_CREATE_NO_WINDOW
)
WINDOWS_FALLBACK_CREATION_FLAGS = (
    WINDOWS_DETACHED_PROCESS
    | WINDOWS_CREATE_NEW_PROCESS_GROUP
    | WINDOWS_CREATE_NO_WINDOW
)


class LaunchError(RuntimeError):
    """Raised when a detached worker cannot be started safely."""


@dataclass(frozen=True)
class LaunchResult:
    launched: bool
    already_running: bool
    process_id: Optional[int] = None

    def as_mapping(self) -> dict[str, object]:
        return {
            "launched": self.launched,
            "already_running": self.already_running,
            "process_id": self.process_id,
        }


def launch_worker(
    project_root: PathValue,
    model_command: Sequence[str],
    *,
    python_executable: Optional[PathValue] = None,
    model_timeout_seconds: float = 120.0,
    max_attempts: int = 3,
    popen_factory: Callable[..., object] = subprocess.Popen,
    platform_name: Optional[str] = None,
) -> LaunchResult:
    root = resolve_project_root(project_root)
    command = tuple(str(argument) for argument in model_command)
    if not command or not command[0].strip():
        raise LaunchError("model command is empty")
    try:
        if contains_known_secret(" ".join(command)):
            raise LaunchError("model command contains a defined secret pattern")
    except PrivacyFilterError as error:
        raise LaunchError("model command is not safe") from error
    if model_timeout_seconds <= 0:
        raise LaunchError("model timeout must be positive")
    if max_attempts <= 0:
        raise LaunchError("max_attempts must be positive")

    with Journal.open(root) as journal:
        runtime = journal.runtime_snapshot(datetime.now(timezone.utc))
        if runtime.worker_lease_active:
            return LaunchResult(launched=False, already_running=True)

    executable = Path(
        python_executable if python_executable is not None else sys.executable
    ).resolve(strict=False)
    if not executable.is_file():
        raise LaunchError("Python executable is unavailable")
    arguments = (
        str(executable),
        "-m",
        "context_compactor",
        "worker",
        "run",
        "--project-root",
        str(root),
        "--model-timeout-seconds",
        str(model_timeout_seconds),
        "--max-attempts",
        str(max_attempts),
        "--model-command",
        *command,
    )
    environment = _worker_environment()
    process_id = start_detached_process(
        arguments,
        cwd=root,
        environment=environment,
        popen_factory=popen_factory,
        platform_name=platform_name,
    )
    return LaunchResult(
        launched=True,
        already_running=False,
        process_id=process_id,
    )


def start_detached_process(
    command: Sequence[str],
    *,
    cwd: PathValue,
    environment: Optional[Mapping[str, str]] = None,
    popen_factory: Callable[..., object] = subprocess.Popen,
    platform_name: Optional[str] = None,
) -> int:
    normalized = tuple(str(argument) for argument in command)
    if not normalized or not normalized[0].strip():
        raise LaunchError("detached command is empty")
    working_directory = resolve_project_root(cwd)
    platform = os.name if platform_name is None else platform_name
    options = detached_process_options(platform)
    options.update(
        {
            "cwd": str(working_directory),
            "env": dict(environment) if environment is not None else None,
        }
    )
    try:
        process = popen_factory(normalized, **options)
    except OSError as error:
        if platform != "nt" or getattr(error, "winerror", None) != 5:
            raise LaunchError("detached process did not start") from error
        fallback = detached_process_options(
            platform,
            creation_flags=WINDOWS_FALLBACK_CREATION_FLAGS,
        )
        fallback.update(
            {
                "cwd": str(working_directory),
                "env": dict(environment) if environment is not None else None,
            }
        )
        try:
            process = popen_factory(normalized, **fallback)
        except OSError as fallback_error:
            raise LaunchError(
                "detached process fallback did not start"
            ) from fallback_error

    process_id = getattr(process, "pid", None)
    if not isinstance(process_id, int) or process_id <= 0:
        raise LaunchError("detached process has no valid process id")
    _start_reaper(process)
    return process_id


def detached_process_options(
    platform_name: str,
    *,
    creation_flags: int = WINDOWS_PRIMARY_CREATION_FLAGS,
) -> dict[str, object]:
    options: dict[str, object] = {
        "stdin": subprocess.DEVNULL,
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
        "close_fds": True,
    }
    if platform_name == "nt":
        options["creationflags"] = creation_flags
        if os.name == "nt":
            startup = subprocess.STARTUPINFO()
            startup.dwFlags |= subprocess.STARTF_USESHOWWINDOW
            startup.wShowWindow = subprocess.SW_HIDE
            options["startupinfo"] = startup
    else:
        options["start_new_session"] = True
    return options


def _worker_environment() -> MutableMapping[str, str]:
    environment = os.environ.copy()
    source_root = str(Path(__file__).resolve().parent.parent)
    current = environment.get("PYTHONPATH", "")
    environment["PYTHONPATH"] = (
        source_root if not current else source_root + os.pathsep + current
    )
    environment["PYTHONDONTWRITEBYTECODE"] = "1"
    return environment


def _start_reaper(process: object) -> None:
    wait = getattr(process, "wait", None)
    if not callable(wait):
        return

    def reap() -> None:
        try:
            wait()
        except BaseException:
            return

    threading.Thread(
        target=reap,
        name="context-compactor-worker-reaper",
        daemon=True,
    ).start()
