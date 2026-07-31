from __future__ import annotations

import argparse
import json
import sys
from datetime import timedelta
from pathlib import Path
from typing import Optional, Sequence

from . import __version__
from .journal import Journal
from .launcher import launch_worker
from .model import CommandModel
from .paths import project_paths, resolve_project_root
from .state import ProjectState, load_state, load_state_file, publish_state
from .worker import MemoryWorker


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="context-compactor")
    parser.add_argument("--version", action="version", version=__version__)
    commands = parser.add_subparsers(dest="command", required=True)

    commands.add_parser("self-check")

    state = commands.add_parser("state")
    state_commands = state.add_subparsers(dest="state_command", required=True)
    for name in ("init", "validate"):
        command = state_commands.add_parser(name)
        command.add_argument("--project-root", type=Path)

    worker = commands.add_parser("worker")
    worker_commands = worker.add_subparsers(dest="worker_command", required=True)
    run = worker_commands.add_parser("run")
    run.add_argument("--project-root", type=Path)
    run.add_argument("--model-timeout-seconds", type=float, default=120.0)
    run.add_argument("--max-attempts", type=int, default=3)
    run.add_argument("--model-command", nargs=argparse.REMAINDER, required=True)
    spawn = worker_commands.add_parser("spawn")
    spawn.add_argument("--project-root", type=Path)
    spawn.add_argument("--model-timeout-seconds", type=float, default=120.0)
    spawn.add_argument("--max-attempts", type=int, default=3)
    spawn.add_argument("--model-command", nargs=argparse.REMAINDER, required=True)
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = _parser().parse_args(argv)
    if args.command == "self-check":
        print(json.dumps({"ok": True, "version": __version__}, separators=(",", ":")))
        return 0

    if args.command == "state":
        project_root = resolve_project_root(args.project_root)
        if args.state_command == "init":
            paths = project_paths(project_root)
            if paths.state.exists():
                load_state_file(paths.state)
                print(paths.state)
                return 0
            path = publish_state(project_root, ProjectState.empty())
            print(path)
            return 0
        if args.state_command == "validate":
            load_state(project_root)
            return 0

    if args.command == "worker" and args.worker_command == "run":
        project_root = resolve_project_root(args.project_root)
        model = CommandModel(
            args.model_command,
            timeout_seconds=args.model_timeout_seconds,
        )
        with Journal.open(project_root) as journal:
            result = MemoryWorker(
                journal,
                project_root,
                model,
                max_attempts=args.max_attempts,
                retry_delays=(timedelta(seconds=5), timedelta(seconds=30)),
            ).drain()
        print(json.dumps(result.as_mapping(), separators=(",", ":")))
        return 0

    if args.command == "worker" and args.worker_command == "spawn":
        result = launch_worker(
            args.project_root,
            args.model_command,
            model_timeout_seconds=args.model_timeout_seconds,
            max_attempts=args.max_attempts,
        )
        print(json.dumps(result.as_mapping(), separators=(",", ":")))
        return 0

    print("unsupported command", file=sys.stderr)
    return 2
