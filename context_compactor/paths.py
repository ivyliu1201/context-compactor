from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path
from typing import Optional, Union

PathValue = Union[str, os.PathLike]


class ProjectPathError(ValueError):
    """Raised when a project root cannot be resolved safely."""


@dataclass(frozen=True)
class ProjectPaths:
    root: Path
    data_dir: Path
    state: Path
    state_backup: Path
    journal: Path
    worker_lock: Path


def resolve_project_root(
    value: Optional[PathValue] = None,
    *,
    cwd: Optional[PathValue] = None,
) -> Path:
    raw = value if value is not None else cwd
    if raw is None:
        raw = os.getcwd()
    text = os.path.expandvars(os.path.expanduser(os.fspath(raw))).strip()
    if not text:
        raise ProjectPathError("project root is empty")
    path = Path(text).resolve(strict=False)
    if not path.exists():
        raise ProjectPathError("project root does not exist")
    if not path.is_dir():
        raise ProjectPathError("project root is not a directory")
    return path


def project_paths(
    value: Optional[PathValue] = None,
    *,
    cwd: Optional[PathValue] = None,
) -> ProjectPaths:
    root = resolve_project_root(value, cwd=cwd)
    data_dir = root / ".context-compactor"
    return ProjectPaths(
        root=root,
        data_dir=data_dir,
        state=data_dir / "state.yaml",
        state_backup=data_dir / "state.backup.yaml",
        journal=data_dir / "events.sqlite",
        worker_lock=data_dir / "worker.lock",
    )
