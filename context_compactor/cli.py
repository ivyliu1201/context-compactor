from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Optional, Sequence

from . import __version__
from .paths import project_paths, resolve_project_root
from .state import ProjectState, load_state, load_state_file, publish_state


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
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = _parser().parse_args(argv)
    if args.command == "self-check":
        print(json.dumps({"ok": True, "version": __version__}, separators=(",", ":")))
        return 0

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

    print("unsupported command", file=sys.stderr)
    return 2
