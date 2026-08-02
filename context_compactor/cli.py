from __future__ import annotations

import argparse
import json
import sys
from datetime import timedelta
from pathlib import Path
from typing import Optional, Sequence

from . import __version__
from .host import (
    HOST_CLAUDE,
    HOST_CODEX,
    MAX_HOOK_PAYLOAD_BYTES,
    HookError,
    diagnostic_line,
    handle_hook,
)
from .paths import ProjectPathError, resolve_project_root

MANAGED_HOST_CODEX = "codex"
MANAGED_HOST_CLAUDE = "claude"


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

    migration = commands.add_parser("migrate")
    migration_commands = migration.add_subparsers(
        dest="migration_command",
        required=True,
    )
    for name in ("preview", "apply"):
        command = migration_commands.add_parser(name)
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

    hook = commands.add_parser("hook")
    hook.add_argument("--host", choices=(HOST_CODEX, HOST_CLAUDE), required=True)
    hook.add_argument("--project-root", type=Path)
    hook.add_argument("--model-command", nargs=argparse.REMAINDER, required=True)

    for name in ("install", "update"):
        management = commands.add_parser(name)
        management.add_argument("--project-root", type=Path)
        management.add_argument("--source-root", type=Path, required=True)
        management.add_argument("--install-root", type=Path)
        management.add_argument("--python", default=sys.executable)
        management.add_argument(
            "--host",
            choices=(MANAGED_HOST_CODEX, MANAGED_HOST_CLAUDE, "all"),
            default=MANAGED_HOST_CODEX,
        )
        management.add_argument(
            "--model-command",
            nargs=argparse.REMAINDER,
        )

    for name in ("uninstall", "status", "doctor"):
        management = commands.add_parser(name)
        management.add_argument("--project-root", type=Path)
        management.add_argument("--install-root", type=Path)
        management.add_argument(
            "--host",
            choices=(MANAGED_HOST_CODEX, MANAGED_HOST_CLAUDE, "all"),
            default=MANAGED_HOST_CODEX,
        )
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = _parser().parse_args(argv)
    if args.command == "self-check":
        print(json.dumps({"ok": True, "version": __version__}, separators=(",", ":")))
        return 0

    if args.command == "state":
        from .paths import project_paths
        from .state import ProjectState, load_state, load_state_file, publish_state

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

    if args.command == "migrate":
        from .migration import (
            MigrationError,
            apply_legacy_migration,
            preview_legacy_migration,
        )

        try:
            project_root = resolve_project_root(args.project_root)
            if args.migration_command == "preview":
                report = preview_legacy_migration(project_root)
            else:
                report = apply_legacy_migration(project_root)
        except (MigrationError, ProjectPathError) as error:
            print(
                json.dumps(
                    {"ok": False, "error": str(error)},
                    separators=(",", ":"),
                ),
                file=sys.stderr,
            )
            return 1
        print(json.dumps(report, separators=(",", ":")))
        return 0

    if args.command == "worker" and args.worker_command == "run":
        from .journal import Journal
        from .model import CommandModel
        from .worker import MemoryWorker

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
        from .launcher import launch_worker

        result = launch_worker(
            args.project_root,
            args.model_command,
            model_timeout_seconds=args.model_timeout_seconds,
            max_attempts=args.max_attempts,
        )
        print(json.dumps(result.as_mapping(), separators=(",", ":")))
        return 0

    if args.command == "hook":
        payload = sys.stdin.buffer.read(MAX_HOOK_PAYLOAD_BYTES + 1)
        try:
            result = handle_hook(
                args.host,
                payload,
                args.model_command,
                project_root=args.project_root,
            )
        except HookError as error:
            print(error.diagnostic(), file=sys.stderr)
            return 1
        sys.stdout.buffer.write(result.output)
        sys.stdout.buffer.flush()
        for category in result.diagnostic_categories:
            print(diagnostic_line(result.event_id, category), file=sys.stderr)
        return 0

    if args.command in {"install", "update", "uninstall", "status", "doctor"}:
        from .management import (
            ManagementError,
            doctor,
            install_source,
            status,
            uninstall,
            update_source,
        )

        try:
            common = {
                "project_root": resolve_project_root(args.project_root),
                "install_root": args.install_root,
                "hosts": (args.host,),
            }
            if args.command == "install":
                report = install_source(
                    **common,
                    source_root=args.source_root,
                    python=args.python,
                    model_command=args.model_command,
                    include_user_legacy=True,
                )
            elif args.command == "update":
                report = update_source(
                    **common,
                    source_root=args.source_root,
                    python=args.python,
                    model_command=args.model_command,
                    include_user_legacy=True,
                )
            elif args.command == "uninstall":
                report = uninstall(**common)
            elif args.command == "status":
                report = status(**common)
            else:
                report = doctor(**common)
        except (ManagementError, ProjectPathError) as error:
            print(
                json.dumps(
                    {"ok": False, "error": str(error)},
                    separators=(",", ":"),
                ),
                file=sys.stderr,
            )
            return 1
        print(json.dumps(report, separators=(",", ":")))
        if args.command == "doctor" and not report["healthy"]:
            return 1
        return 0

    print("unsupported command", file=sys.stderr)
    return 2
