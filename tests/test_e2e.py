from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

SOURCE_ROOT = Path(__file__).resolve().parents[1]


class FreshProjectE2ETests(unittest.TestCase):
    @unittest.skipUnless(
        os.name == "nt"
        and shutil.which("powershell.exe") is not None
        and shutil.which("git") is not None,
        "Windows PowerShell and Git are required",
    )
    def test_installed_fresh_project_flow_passes_all_gates(self) -> None:
        environment = os.environ.copy()
        environment["PYTHONDONTWRITEBYTECODE"] = "1"
        completed = subprocess.run(
            (
                sys.executable,
                "-B",
                str(SOURCE_ROOT / "scripts" / "fresh_project_e2e.py"),
                "--source-root",
                str(SOURCE_ROOT),
            ),
            cwd=str(SOURCE_ROOT),
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=300,
            check=False,
        )
        report = json.loads(completed.stdout.decode("utf-8"))

        self.assertEqual(
            completed.returncode,
            0,
            completed.stdout.decode("utf-8")
            + completed.stderr.decode("utf-8"),
        )
        self.assertEqual(completed.stderr, b"")
        self.assertTrue(report["ok"])
        self.assertEqual(report["scenario"], "fresh-project-standard")
        self.assertEqual(report["mode"], "standard")
        self.assertTrue(all(report["checks"].values()))
        self.assertTrue(
            all(value == 0 for value in report["privacy_matches"].values())
        )
        self.assertEqual(
            report["counts"],
            {
                "events": 2,
                "pending": 0,
                "processing": 0,
                "completed": 2,
                "failed": 0,
                "attempts": 2,
                "updated": 1,
                "no_change": 1,
                "retained_prompts": 0,
                "redactions": 4,
                "state_publications": 1,
                "injection_bytes": report["counts"]["injection_bytes"],
                "console_observations": 2,
                "visible_console_windows": 0,
            },
        )
        self.assertGreater(report["counts"]["injection_bytes"], 0)
        self.assertEqual(len(report["latency_ms"]["hook"]), 3)
        self.assertEqual(len(report["latency_ms"]["background"]), 2)


if __name__ == "__main__":
    unittest.main()
