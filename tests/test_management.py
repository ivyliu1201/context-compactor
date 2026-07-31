from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

from context_compactor.journal import EnqueueRequest, Journal, digest_text
from context_compactor.management import (
    HOOK_EVENTS,
    ManagementError,
    doctor,
    install_source,
    status,
    uninstall,
    update_source,
)

NOW = datetime(2026, 7, 31, 6, 0, tzinfo=timezone.utc)
SOURCE_ROOT = Path(__file__).resolve().parents[1]


def _which(directory: Path, *available_hosts: str):
    executables = {}
    for name in ("powershell.exe", *available_hosts):
        path = directory / name
        path.write_text("", encoding="utf-8")
        executables[name] = str(path)
    executables["powershell"] = executables["powershell.exe"]

    def resolve(name: str):
        return executables.get(name)

    return resolve


def _fake_private_venv(
    installation: Path,
    _system_python: Path,
    version_root: Path,
):
    name = (
        installation / ".venv" / "Scripts" / "python.exe"
        if os.name == "nt"
        else installation / ".venv" / "bin" / "python"
    )
    name.parent.mkdir(parents=True, exist_ok=True)
    name.write_text("test interpreter", encoding="utf-8")
    pointer = installation / ".venv" / "purelib" / "active.pth"
    return (
        name.resolve(strict=True),
        pointer,
        (str(version_root) + os.linesep).encode("utf-8"),
        True,
    )


def _request(index: int, enqueued_at: datetime) -> EnqueueRequest:
    return EnqueueRequest(
        event_id=f"management-event-{index}",
        kind="user_prompt",
        occurred_at=enqueued_at,
        content_digest=digest_text(f"management-raw-{index}"),
        prompt=f"bounded prompt {index}",
        redaction_count=0,
        enqueued_at=enqueued_at,
    )


class ManagementTests(unittest.TestCase):
    @unittest.skipUnless(
        os.name == "nt"
        and shutil.which("powershell.exe") is not None
        and shutil.which("git") is not None,
        "Windows PowerShell and Git are required",
    )
    def test_powershell_installer_accepts_model_command_json_array(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "fresh project"
            project.mkdir()
            installation = temporary / "Local App Data" / "context-compactor"
            tools = temporary / "tools"
            tools.mkdir()
            (tools / "codex.cmd").write_text("@exit /b 0\r\n", encoding="ascii")

            environment = os.environ.copy()
            environment.update(
                {
                    "PATH": str(tools)
                    + os.pathsep
                    + environment.get("PATH", ""),
                    "CC_INSTALL_SCRIPT": str(
                        SOURCE_ROOT / "scripts" / "install.ps1"
                    ),
                    "CC_PROJECT_ROOT": str(project),
                    "CC_INSTALL_ROOT": str(installation),
                    "CC_PYTHON": sys.executable,
                    "CC_MODEL_COMMAND": json.dumps(
                        [sys.executable, "-c", "pass"]
                    ),
                }
            )
            powershell = str(shutil.which("powershell.exe"))
            installed = subprocess.run(
                (
                    powershell,
                    "-NoProfile",
                    "-ExecutionPolicy",
                    "Bypass",
                    "-Command",
                    "& $env:CC_INSTALL_SCRIPT -Action install "
                    "-ProjectRoot $env:CC_PROJECT_ROOT "
                    "-InstallDirectory $env:CC_INSTALL_ROOT "
                    "-AgentHost codex -Python $env:CC_PYTHON "
                    "-ModelCommandJson $env:CC_MODEL_COMMAND",
                ),
                capture_output=True,
                check=False,
                cwd=SOURCE_ROOT,
                env=environment,
                text=True,
                timeout=180,
            )
            self.assertEqual(installed.returncode, 0, installed.stderr)
            report = json.loads(installed.stdout)
            self.assertTrue(report["installed"])
            self.assertIn(".venv", report["python_interpreter"])

            removed = subprocess.run(
                (
                    powershell,
                    "-NoProfile",
                    "-ExecutionPolicy",
                    "Bypass",
                    "-Command",
                    "& $env:CC_INSTALL_SCRIPT -Action uninstall "
                    "-ProjectRoot $env:CC_PROJECT_ROOT "
                    "-InstallDirectory $env:CC_INSTALL_ROOT "
                    "-AgentHost codex -Python $env:CC_PYTHON",
                ),
                capture_output=True,
                check=False,
                cwd=SOURCE_ROOT,
                env=environment,
                text=True,
                timeout=60,
            )
            self.assertEqual(removed.returncode, 0, removed.stderr)
            self.assertTrue(json.loads(removed.stdout)["installation_removed"])
            self.assertFalse(installation.exists())

    def test_source_install_repeated_update_and_uninstall_preserve_user_hooks(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "project with spaces"
            project.mkdir()
            installation = temporary / "Local App Data" / "context-compactor"
            tools = temporary / "tools"
            tools.mkdir()
            which = _which(tools, "codex")

            config_path = project / ".codex" / "hooks.json"
            config_path.parent.mkdir()
            original = {
                "custom": True,
                "hooks": {
                    "SessionStart": [
                        {
                            "hooks": [
                                {
                                    "type": "command",
                                    "command": "user-owned-hook",
                                    "timeout": 10,
                                }
                            ]
                        }
                    ]
                },
            }
            config_path.write_text(
                json.dumps(original),
                encoding="utf-8",
            )

            installed = install_source(
                project_root=project,
                source_root=SOURCE_ROOT,
                install_root=installation,
                python=sys.executable,
                hosts=("codex",),
                model_command=(sys.executable, "-c", "pass"),
                now=NOW,
                which=which,
            )
            self.assertTrue(installed["installed"])
            self.assertTrue(installed["source_created"])
            self.assertTrue(installed["source_changed"])
            self.assertEqual(
                installed["hooks"][0]["state"],
                "awaiting_manual_trust",
            )
            self.assertTrue(installed["hooks"][0]["manual_trust_required"])
            private_python = Path(installed["python_interpreter"])
            self.assertTrue(private_python.is_file())
            self.assertTrue(
                str(private_python).startswith(str(installation / ".venv"))
            )
            source_path = Path(installed["active_source"]["path"])
            self.assertTrue((source_path / "context_compactor").is_dir())
            self.assertTrue((source_path / "requirements.lock").is_file())
            self.assertFalse(
                any(
                    path.name.startswith("context-compactor")
                    and path.suffix == ".exe"
                    for path in installation.rglob("*.exe")
                )
            )

            updated = update_source(
                project_root=project,
                source_root=SOURCE_ROOT,
                install_root=installation,
                python=sys.executable,
                hosts=("codex",),
                model_command=(sys.executable, "-c", "pass"),
                now=NOW + timedelta(minutes=1),
                which=which,
            )
            self.assertFalse(updated["source_created"])
            self.assertFalse(updated["source_changed"])
            for event in HOOK_EVENTS:
                self.assertEqual(updated["hooks"][0]["events"][event], 1)

            document = json.loads(config_path.read_text(encoding="utf-8"))
            session_handlers = [
                handler
                for group in document["hooks"]["SessionStart"]
                for handler in group["hooks"]
            ]
            self.assertEqual(
                sum(
                    handler.get("command") == "user-owned-hook"
                    for handler in session_handlers
                ),
                1,
            )

            install_manifest = json.loads(
                (installation / "install.json").read_text(encoding="utf-8")
            )
            project_manifest_path = Path(
                next(iter(install_manifest["projects"].values()))
            )
            project_manifest = json.loads(
                project_manifest_path.read_text(encoding="utf-8")
            )
            original_config_path = project_manifest["hosts"]["codex"][
                "config_path"
            ]
            outside = temporary / "outside.json"
            outside.write_text(json.dumps(document), encoding="utf-8")
            outside_before = outside.read_bytes()
            project_manifest["hosts"]["codex"]["config_path"] = str(outside)
            project_manifest_path.write_text(
                json.dumps(project_manifest),
                encoding="utf-8",
            )
            tampered = status(
                project_root=project,
                install_root=installation,
                hosts=("codex",),
                now=NOW,
                which=which,
            )
            self.assertIn(
                "hook_config_path_mismatch",
                tampered["hooks"][0]["issues"],
            )
            with self.assertRaisesRegex(
                ManagementError,
                "configuration path is invalid",
            ):
                uninstall(
                    project_root=project,
                    install_root=installation,
                    hosts=("codex",),
                )
            self.assertEqual(outside.read_bytes(), outside_before)
            project_manifest["hosts"]["codex"][
                "config_path"
            ] = original_config_path
            project_manifest_path.write_text(
                json.dumps(project_manifest),
                encoding="utf-8",
            )

            removed = uninstall(
                project_root=project,
                install_root=installation,
                hosts=("codex",),
            )
            self.assertTrue(removed["installation_removed"])
            self.assertFalse(installation.exists())
            self.assertEqual(
                json.loads(config_path.read_text(encoding="utf-8")),
                original,
            )

    def test_status_is_read_only_and_doctor_reports_runtime_health(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "project"
            project.mkdir()
            installation = temporary / "install"
            tools = temporary / "tools"
            tools.mkdir()
            which = _which(tools, "codex", "claude")

            with patch(
                "context_compactor.management._prepare_private_venv",
                side_effect=_fake_private_venv,
            ):
                install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=installation,
                    python=sys.executable,
                    hosts=("codex", "claude"),
                    model_command=(sys.executable, "-c", "pass"),
                    now=NOW,
                    which=which,
                )

            completed_at = NOW - timedelta(minutes=2)
            with Journal.open(project) as journal:
                journal.enqueue(_request(1, completed_at - timedelta(seconds=2)))
                claimed = journal.claim_next(
                    completed_at - timedelta(seconds=1),
                    lease_duration=timedelta(minutes=1),
                )
                self.assertIsNotNone(claimed)
                journal.complete(
                    claimed.event_id,
                    claimed.lease_token,
                    "no_change",
                    completed_at,
                )
                journal.enqueue(_request(2, NOW - timedelta(minutes=1)))
                journal_path = journal.path

            claude_config = project / ".claude" / "settings.local.json"
            claude_document = json.loads(
                claude_config.read_text(encoding="utf-8")
            )
            claude_document["disableAllHooks"] = True
            claude_config.write_text(
                json.dumps(claude_document),
                encoding="utf-8",
            )
            before = hashlib.sha256(journal_path.read_bytes()).hexdigest()
            report = status(
                project_root=project,
                install_root=installation,
                hosts=("codex", "claude"),
                now=NOW,
                which=which,
            )
            after = hashlib.sha256(journal_path.read_bytes()).hexdigest()
            self.assertEqual(before, after)
            self.assertEqual(report["journal"]["completed_no_change"], 1)
            self.assertEqual(report["journal"]["pending_jobs"], 1)
            self.assertTrue(report["journal"]["worker_not_running"])
            self.assertEqual(report["state"]["status"], "missing")
            self.assertNotIn("bounded prompt", json.dumps(report))
            self.assertFalse(report["hooks"][1]["definition_healthy"])
            self.assertIn("hooks_disabled", report["hooks"][1]["issues"])

            with patch(
                "context_compactor.management._probe_installed_python",
                return_value=True,
            ):
                diagnosed = doctor(
                    project_root=project,
                    install_root=installation,
                    hosts=("codex", "claude"),
                    now=NOW,
                    which=which,
                )
            self.assertFalse(diagnosed["healthy"])
            self.assertIn("claude_hook_unhealthy", diagnosed["issues"])
            self.assertIn("worker_not_running", diagnosed["issues"])
            self.assertIn("state_missing", diagnosed["notices"])

    def test_install_fails_for_missing_python_host_or_known_secret(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "project"
            project.mkdir()
            tools = temporary / "tools"
            tools.mkdir()
            powershell_only = _which(tools)

            with self.assertRaisesRegex(
                ManagementError,
                "required executable is unavailable",
            ):
                install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=temporary / "missing-python",
                    python=str(temporary / "python-does-not-exist.exe"),
                    hosts=("codex",),
                    model_command=(sys.executable,),
                    which=powershell_only,
                )

            with self.assertRaisesRegex(
                ManagementError,
                "required codex executable is unavailable",
            ):
                install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=temporary / "missing-host",
                    python=sys.executable,
                    hosts=("codex",),
                    model_command=(sys.executable,),
                    which=powershell_only,
                )

            host_tools = _which(tools, "codex")
            with self.assertRaisesRegex(
                ManagementError,
                "defined secret pattern",
            ):
                install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=temporary / "secret",
                    python=sys.executable,
                    hosts=("codex",),
                    model_command=(
                        sys.executable,
                        "--password=synthetic-credential",
                    ),
                    which=host_tools,
                )
            self.assertFalse((temporary / "secret").exists())
            with self.assertRaisesRegex(
                ManagementError,
                "defined secret pattern",
            ):
                install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=temporary / "separate-secret",
                    python=sys.executable,
                    hosts=("codex",),
                    model_command=(
                        sys.executable,
                        "--password",
                        "synthetic-credential",
                    ),
                    which=host_tools,
                )
            self.assertFalse((temporary / "separate-secret").exists())

            claude_config = project / ".claude" / "settings.local.json"
            claude_config.parent.mkdir()
            claude_config.write_text(
                json.dumps({"disableAllHooks": True}),
                encoding="utf-8",
            )
            claude_tools = _which(tools, "claude")
            with patch(
                "context_compactor.management._prepare_private_venv"
            ) as prepare:
                with self.assertRaisesRegex(
                    ManagementError,
                    "Claude hooks are disabled",
                ):
                    install_source(
                        project_root=project,
                        source_root=SOURCE_ROOT,
                        install_root=temporary / "disabled-hooks",
                        python=sys.executable,
                        hosts=("claude",),
                        model_command=(sys.executable,),
                        which=claude_tools,
                    )
            prepare.assert_not_called()
            self.assertFalse((temporary / "disabled-hooks").exists())

            codex_config = project / ".codex" / "hooks.json"
            codex_config.parent.mkdir()
            codex_config.write_text('{"hooks":null}', encoding="utf-8")
            with patch(
                "context_compactor.management._prepare_private_venv"
            ) as prepare:
                with self.assertRaisesRegex(
                    ManagementError,
                    "hooks must be a JSON object",
                ):
                    install_source(
                        project_root=project,
                        source_root=SOURCE_ROOT,
                        install_root=temporary / "invalid-hooks",
                        python=sys.executable,
                        hosts=("codex",),
                        model_command=(sys.executable,),
                        which=host_tools,
                    )
            prepare.assert_not_called()
            self.assertEqual(
                codex_config.read_text(encoding="utf-8"),
                '{"hooks":null}',
            )
            self.assertFalse((temporary / "invalid-hooks").exists())
            codex_config.unlink()

            rollback_root = temporary / "rollback"
            with patch(
                "context_compactor.management._prepare_private_venv",
                side_effect=_fake_private_venv,
            ), patch(
                "context_compactor.management._apply_file_changes",
                side_effect=OSError("synthetic write failure"),
            ):
                with self.assertRaisesRegex(
                    ManagementError,
                    "failed to write installation files",
                ):
                    install_source(
                        project_root=project,
                        source_root=SOURCE_ROOT,
                        install_root=rollback_root,
                        python=sys.executable,
                        hosts=("codex",),
                        model_command=(sys.executable,),
                        which=host_tools,
                    )
            self.assertFalse((rollback_root / ".venv").exists())
            self.assertFalse((rollback_root / "install.json").exists())
            self.assertFalse(
                (rollback_root / "context-compactor-hook.ps1").exists()
            )


if __name__ == "__main__":
    unittest.main()
