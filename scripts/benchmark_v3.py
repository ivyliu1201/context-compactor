from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Optional, Sequence

SOURCE_ROOT = Path(__file__).resolve().parents[1]
if str(SOURCE_ROOT) not in sys.path:
    sys.path.insert(0, str(SOURCE_ROOT))

from context_compactor.benchmark import (
    BenchmarkError,
    CodexForegroundClient,
    render_markdown_report,
    run_stage1_benchmark,
    validate_report,
)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Run the formal context-compactor Benchmark Plan v3 Gate.",
    )
    parser.add_argument(
        "--repository-root",
        type=Path,
        default=SOURCE_ROOT,
    )
    parser.add_argument(
        "--output",
        type=Path,
        required=True,
    )
    parser.add_argument(
        "--report",
        type=Path,
        required=True,
    )
    parser.add_argument(
        "--codex-command",
        default=None,
    )
    parser.add_argument(
        "--model",
        default="gpt-5.6-sol",
    )
    parser.add_argument(
        "--reasoning-effort",
        default="high",
    )
    parser.add_argument(
        "--timeout-seconds",
        type=int,
        default=300,
    )
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = _parser().parse_args(argv)
    try:
        client = CodexForegroundClient(
            command=args.codex_command,
            model=args.model,
            reasoning_effort=args.reasoning_effort,
            timeout_seconds=args.timeout_seconds,
        )
        result = run_stage1_benchmark(args.repository_root, client)
        validate_report(result)
        encoded = (
            json.dumps(
                result,
                ensure_ascii=False,
                indent=2,
                sort_keys=True,
            )
            + "\n"
        )
        markdown = render_markdown_report(result)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded, encoding="utf-8")
        args.report.write_text(markdown, encoding="utf-8")
    except (BenchmarkError, OSError) as error:
        print(f"benchmark_v3: {error}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "status": result["status"],
                "output": str(args.output),
                "report": str(args.report),
            },
            ensure_ascii=False,
            separators=(",", ":"),
        )
    )
    return 0 if result["status"] == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(main())
