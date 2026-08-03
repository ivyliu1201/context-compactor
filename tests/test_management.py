from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
import zipfile
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Optional
from unittest.mock import patch

from context_compactor.journal import EnqueueRequest, Journal, digest_text
from context_compactor.management import (
    HOOK_EVENTS,
    ManagementError,
    doctor,
    install_source,
    resolve_hook_project,
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


def _is_within(path: Path, parent: Path) -> bool:
    try:
        path.resolve(strict=False).relative_to(parent.resolve(strict=False))
    except ValueError:
        return False
    return True


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
            profile = temporary / "profile"
            profile.mkdir()
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
                    "HOME": str(profile),
                    "USERPROFILE": str(profile),
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

    @unittest.skipUnless(
        os.name == "nt"
        and shutil.which("powershell.exe") is not None
        and shutil.which("git") is not None,
        "Windows PowerShell and Git are required",
    )
    def test_powershell_installer_uses_bundled_adapter_by_default(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "fresh project"
            project.mkdir()
            installation = temporary / "Local App Data" / "context-compactor"
            tools = temporary / "tools"
            tools.mkdir()
            profile = temporary / "profile"
            profile.mkdir()
            codex = tools / "codex.cmd"
            codex.write_text("@exit /b 0\r\n", encoding="ascii")

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
                    "HOME": str(profile),
                    "USERPROFILE": str(profile),
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
                    "-AgentHost codex -Python $env:CC_PYTHON",
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
            self.assertEqual(report["model_adapter"], "bundled_codex")

            install_manifest = json.loads(
                (installation / "install.json").read_text(encoding="utf-8")
            )
            project_manifest = json.loads(
                Path(next(iter(install_manifest["projects"].values()))).read_text(
                    encoding="utf-8"
                )
            )
            command = project_manifest["model_command"]
            self.assertEqual(
                command[1:3],
                ["-m", "context_compactor.codex_adapter"],
            )
            self.assertEqual(Path(command[4]), codex.resolve())

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
            self.assertFalse(installation.exists())

    @unittest.skipUnless(
        os.name == "nt"
        and shutil.which("powershell.exe") is not None
        and shutil.which("git") is not None,
        "Windows PowerShell and Git are required",
    )
    def test_powershell_bootstrap_installs_then_updates_same_command(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            archive_root = temporary / "archive"
            release_root = archive_root / "ivyliu1201-context-compactor-test"
            package_root = release_root / "context_compactor"
            source_package = SOURCE_ROOT / "context_compactor"
            for source in source_package.rglob("*.py"):
                destination = package_root / source.relative_to(source_package)
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, destination)
            version_file = package_root / "__init__.py"
            version_source = version_file.read_text(encoding="utf-8")
            version_line = next(
                line
                for line in version_source.splitlines()
                if line.startswith("__version__ = ")
            )
            self.assertEqual(version_source.count(version_line), 1)
            version_file.write_text(
                version_source.replace(
                    version_line,
                    '__version__ = "3.2.0"',
                ),
                encoding="utf-8",
            )
            scripts_root = release_root / "scripts"
            scripts_root.mkdir(parents=True)
            for name in ("install.ps1", "context-compactor-hook.ps1"):
                shutil.copyfile(
                    SOURCE_ROOT / "scripts" / name,
                    scripts_root / name,
                )
            shutil.copyfile(
                SOURCE_ROOT / "requirements.lock",
                release_root / "requirements.lock",
            )

            archive = temporary / "release.zip"
            with zipfile.ZipFile(
                archive,
                "w",
                compression=zipfile.ZIP_DEFLATED,
            ) as bundle:
                for path in release_root.rglob("*"):
                    if path.is_file():
                        bundle.write(path, path.relative_to(archive_root))

            updated_archive_root = temporary / "updated archive"
            updated_release_root = (
                updated_archive_root
                / "ivyliu1201-context-compactor-test"
            )
            shutil.copytree(release_root, updated_release_root)
            updated_version_file = (
                updated_release_root / "context_compactor" / "__init__.py"
            )
            updated_version_source = updated_version_file.read_text(
                encoding="utf-8"
            )
            self.assertEqual(
                updated_version_source.count('__version__ = "3.2.0"'),
                1,
            )
            updated_version_file.write_text(
                updated_version_source.replace(
                    '__version__ = "3.2.0"',
                    '__version__ = "3.2.1"',
                ),
                encoding="utf-8",
            )
            updated_archive = temporary / "updated-release.zip"
            with zipfile.ZipFile(
                updated_archive,
                "w",
                compression=zipfile.ZIP_DEFLATED,
            ) as bundle:
                for path in updated_release_root.rglob("*"):
                    if path.is_file():
                        bundle.write(
                            path,
                            path.relative_to(updated_archive_root),
                        )

            project = temporary / "fresh project"
            project.mkdir()
            tools = temporary / "tools"
            tools.mkdir()
            profile = temporary / "profile"
            profile.mkdir()
            bootstrap_temp = temporary / "bootstrap temp"
            bootstrap_temp.mkdir()
            (tools / "codex.cmd").write_text(
                "@exit /b 0\r\n",
                encoding="ascii",
            )

            environment = os.environ.copy()
            environment.update(
                {
                    "PATH": str(tools)
                    + os.pathsep
                    + environment.get("PATH", ""),
                    "CC_BOOTSTRAP": str(
                        SOURCE_ROOT / "scripts" / "bootstrap.ps1"
                    ),
                    "LOCALAPPDATA": str(temporary / "Local App Data"),
                    "HOME": str(profile),
                    "USERPROFILE": str(profile),
                    "TEMP": str(bootstrap_temp),
                    "TMP": str(bootstrap_temp),
                }
            )
            powershell = str(shutil.which("powershell.exe"))
            command = """
function Invoke-RestMethod {
    [CmdletBinding()]
    param([string]$Uri, $Headers, [string]$Method)
    if ($Uri -eq 'https://raw.githubusercontent.com/ivyliu1201/context-compactor/v3.2.0/scripts/bootstrap.ps1') {
        return Get-Content -Raw -Encoding UTF8 $env:CC_BOOTSTRAP
    }
    if ($Uri -ne 'https://api.github.com/repos/ivyliu1201/context-compactor/releases/latest') {
        throw 'unexpected release API URL'
    }
    [pscustomobject]@{
        tag_name = $env:CC_RELEASE_TAG
        zipball_url = 'https://api.github.com/repos/ivyliu1201/context-compactor/zipball/' + $env:CC_RELEASE_TAG
        draft = $false
        prerelease = $false
    }
}
function Invoke-WebRequest {
    [CmdletBinding()]
    param(
        [string]$Uri,
        $Headers,
        [switch]$UseBasicParsing,
        [string]$OutFile
    )
    $expected = 'https://api.github.com/repos/ivyliu1201/context-compactor/zipball/' + $env:CC_RELEASE_TAG
    if ($Uri -ne $expected) {
        throw 'unexpected release download URL'
    }
    Copy-Item -LiteralPath $env:CC_ARCHIVE -Destination $OutFile
}
& ([scriptblock]::Create((Invoke-RestMethod 'https://raw.githubusercontent.com/ivyliu1201/context-compactor/v3.2.0/scripts/bootstrap.ps1')))
"""

            def run_bootstrap(
                tag: str,
                release_archive: Path,
            ) -> subprocess.CompletedProcess[str]:
                run_environment = environment.copy()
                run_environment.update(
                    {
                        "CC_ARCHIVE": str(release_archive),
                        "CC_RELEASE_TAG": tag,
                    }
                )
                return subprocess.run(
                    (
                        powershell,
                        "-NoProfile",
                        "-ExecutionPolicy",
                        "Bypass",
                        "-Command",
                        command,
                    ),
                    capture_output=True,
                    check=False,
                    cwd=project,
                    env=run_environment,
                    text=True,
                    timeout=180,
                )

            first = run_bootstrap("v3.2.0", archive)
            self.assertEqual(first.returncode, 0, first.stderr)
            self.assertIn(
                "[1/4] Checking the latest stable release...",
                first.stdout,
            )
            self.assertIn(
                "[2/4] Downloading context-compactor v3.2.0...",
                first.stdout,
            )
            self.assertIn(
                "[3/4] Verifying the release package...",
                first.stdout,
            )
            self.assertIn("[4/4] Installing for codex...", first.stdout)
            self.assertIn(
                "[OK] context-compactor v3.2.0 is ready.",
                first.stdout,
            )
            self.assertIn("[RESULT] Installed", first.stdout)
            self.assertIn(f"Project: {project.resolve()}", first.stdout)
            self.assertIn("[NEXT] Start your coding agent", first.stdout)
            self.assertNotIn('"installed":', first.stdout)
            self.assertNotIn('"ok":', first.stdout)

            installation = (
                temporary / "Local App Data" / "context-compactor"
            )
            install_location_prefix = "Install location: "
            install_location_line = next(
                line
                for line in first.stdout.splitlines()
                if line.startswith(install_location_prefix)
            )
            displayed_installation = Path(
                install_location_line[len(install_location_prefix) :]
            )
            self.assertTrue(
                os.path.samefile(displayed_installation, installation),
                first.stdout,
            )
            first_manifest = json.loads(
                (installation / "install.json").read_text(encoding="utf-8")
            )
            self.assertEqual(first_manifest["package_version"], "3.2.0")
            first_version_root = first_manifest["version_root"]

            second = run_bootstrap("v3.2.1", updated_archive)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertIn(
                "[2/4] Downloading context-compactor v3.2.1...",
                second.stdout,
            )
            self.assertIn(
                "[OK] context-compactor v3.2.1 is ready.",
                second.stdout,
            )
            self.assertIn("[RESULT] Updated", second.stdout)
            self.assertIn("[NEXT] Start your coding agent", second.stdout)
            self.assertNotIn('"installed":', second.stdout)
            self.assertNotIn('"ok":', second.stdout)
            second_manifest = json.loads(
                (installation / "install.json").read_text(encoding="utf-8")
            )
            self.assertEqual(second_manifest["package_version"], "3.2.1")
            second_version_root = second_manifest["version_root"]
            self.assertNotEqual(second_version_root, first_version_root)

            third = run_bootstrap("v3.2.1", updated_archive)
            self.assertEqual(third.returncode, 0, third.stderr)
            self.assertIn(
                "[OK] context-compactor v3.2.1 is ready.",
                third.stdout,
            )
            self.assertIn("[RESULT] Already up to date", third.stdout)
            self.assertIn("[NEXT] Start your coding agent", third.stdout)
            self.assertNotIn('"installed":', third.stdout)
            self.assertNotIn('"ok":', third.stdout)
            third_manifest = json.loads(
                (installation / "install.json").read_text(encoding="utf-8")
            )
            self.assertEqual(
                third_manifest["version_root"],
                second_version_root,
            )
            self.assertFalse(
                any(bootstrap_temp.glob("context-compactor-bootstrap-*"))
            )

    @unittest.skipUnless(
        os.name == "nt" and shutil.which("powershell.exe") is not None,
        "Windows PowerShell is required",
    )
    def test_powershell_bootstrap_rejects_unexpected_download_host(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "project"
            project.mkdir()
            environment = os.environ.copy()
            environment.update(
                {
                    "CC_BOOTSTRAP": str(
                        SOURCE_ROOT / "scripts" / "bootstrap.ps1"
                    ),
                    "CC_PROJECT_ROOT": str(project),
                }
            )
            command = """
function Invoke-RestMethod {
    [CmdletBinding()]
    param([string]$Uri, $Headers, [string]$Method)
    [pscustomobject]@{
        tag_name = 'v3.0.0'
        zipball_url = 'https://example.test/context-compactor.zip'
        draft = $false
        prerelease = $false
    }
}
& $env:CC_BOOTSTRAP -ProjectRoot $env:CC_PROJECT_ROOT
"""
            rejected = subprocess.run(
                (
                    str(shutil.which("powershell.exe")),
                    "-NoProfile",
                    "-ExecutionPolicy",
                    "Bypass",
                    "-Command",
                    command,
                ),
                capture_output=True,
                check=False,
                cwd=project,
                env=environment,
                text=True,
                timeout=60,
            )

            self.assertNotEqual(rejected.returncode, 0)
            self.assertIn("outside the expected repository", rejected.stderr)

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
                _is_within(private_python, installation / ".venv")
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
            managed_session_groups = [
                group
                for group in document["hooks"]["SessionStart"]
                if any(
                    handler.get("command") != "user-owned-hook"
                    for handler in group["hooks"]
                )
            ]
            self.assertEqual(len(managed_session_groups), 1)
            self.assertEqual(
                managed_session_groups[0].get("matcher"),
                "^startup$",
            )
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

            managed_session_groups[0]["matcher"] = ".*"
            config_path.write_text(json.dumps(document), encoding="utf-8")
            unhealthy = status(
                project_root=project,
                install_root=installation,
                hosts=("codex",),
                now=NOW,
                which=which,
            )
            self.assertIn(
                "hook_definition_mismatch",
                unhealthy["hooks"][0]["issues"],
            )
            managed_session_groups[0]["matcher"] = "^startup$"
            config_path.write_text(json.dumps(document), encoding="utf-8")

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

    def test_source_install_defaults_to_bundled_codex_adapter(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "project"
            project.mkdir()
            installation = temporary / "install"
            tools = temporary / "tools"
            tools.mkdir()
            which = _which(tools, "codex")

            with patch(
                "context_compactor.management._prepare_private_venv",
                side_effect=_fake_private_venv,
            ):
                installed = install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=installation,
                    python=sys.executable,
                    hosts=("codex",),
                    now=NOW,
                    which=which,
                )

            self.assertEqual(installed["model_adapter"], "bundled_codex")
            install_manifest = json.loads(
                (installation / "install.json").read_text(encoding="utf-8")
            )
            project_manifest_path = Path(
                next(iter(install_manifest["projects"].values()))
            )
            project_manifest = json.loads(
                project_manifest_path.read_text(encoding="utf-8")
            )
            command = project_manifest["model_command"]
            self.assertEqual(command[0], installed["python_interpreter"])
            self.assertEqual(
                command[1:3],
                ["-m", "context_compactor.codex_adapter"],
            )
            self.assertEqual(command[3], "--codex-command")
            self.assertEqual(Path(command[4]), (tools / "codex").resolve())

            healthy = status(
                project_root=project,
                install_root=installation,
                hosts=("codex",),
                now=NOW,
                which=which,
            )
            self.assertEqual(healthy["model_adapter"], "bundled_codex")
            self.assertTrue(healthy["hooks"][0]["model_available"])

            (tools / "codex").unlink()
            missing = status(
                project_root=project,
                install_root=installation,
                hosts=("codex",),
                now=NOW,
                which=which,
            )
            self.assertFalse(missing["hooks"][0]["model_available"])

    def test_install_removes_only_manifest_owned_v2_hooks(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "project"
            project.mkdir()
            legacy_home = temporary / "legacy-home"
            legacy_state = legacy_home / ".context-compactor"
            legacy_state.mkdir(parents=True)
            installation = temporary / "install"
            tools = temporary / "tools"
            tools.mkdir()
            which = _which(tools, "codex")

            legacy_command = '"C:\\legacy\\context-compactor.exe" hook'
            legacy_command_windows = f"& {legacy_command}"
            legacy_manifest = {
                "version": 1,
                "hosts": {
                    "codex": {
                        "executable": "C:\\legacy\\context-compactor.exe",
                        "command": legacy_command,
                        "command_windows": legacy_command_windows,
                        "config_created": False,
                        "installed_at": "2026-07-30T00:00:00Z",
                    }
                },
            }
            manifest_path = legacy_state / "install.json"
            manifest_path.write_text(
                json.dumps(legacy_manifest),
                encoding="utf-8",
            )
            manifest_before = manifest_path.read_bytes()
            legacy_database = legacy_state / "context.db"
            legacy_database.write_bytes(b"legacy-database-must-remain")
            database_before = legacy_database.read_bytes()

            legacy_config_path = legacy_home / ".codex" / "hooks.json"
            legacy_config_path.parent.mkdir()
            legacy_hooks = {
                event: [
                    {
                        "hooks": [
                            {
                                "type": "command",
                                "command": legacy_command,
                                "commandWindows": legacy_command_windows,
                                "timeout": 30,
                            }
                        ]
                    }
                ]
                for event in HOOK_EVENTS
            }
            legacy_hooks["SessionStart"].append(
                {
                    "matcher": "^startup$",
                    "hooks": [
                        {
                            "type": "command",
                            "command": "user-owned-hook",
                            "timeout": 10,
                        }
                    ],
                }
            )
            legacy_config_path.write_text(
                json.dumps({"custom": True, "hooks": legacy_hooks}),
                encoding="utf-8",
            )

            with patch(
                "context_compactor.management._legacy_install_roots",
                return_value=(project, legacy_home),
            ), patch(
                "context_compactor.management._prepare_private_venv",
                side_effect=_fake_private_venv,
            ):
                report = install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=installation,
                    python=sys.executable,
                    hosts=("codex",),
                    model_command=(sys.executable, "-c", "pass"),
                    now=NOW,
                    which=which,
                )

            self.assertEqual(report["legacy_hooks_removed"], len(HOOK_EVENTS))
            cleaned = json.loads(
                legacy_config_path.read_text(encoding="utf-8")
            )
            self.assertTrue(cleaned["custom"])
            for event in HOOK_EVENTS:
                handlers = [
                    handler
                    for group in cleaned["hooks"].get(event, [])
                    for handler in group["hooks"]
                ]
                self.assertFalse(
                    any(
                        handler.get("command") == legacy_command
                        for handler in handlers
                    )
                )
            self.assertEqual(
                cleaned["hooks"]["SessionStart"][0]["hooks"][0]["command"],
                "user-owned-hook",
            )
            self.assertEqual(manifest_path.read_bytes(), manifest_before)
            self.assertEqual(legacy_database.read_bytes(), database_before)

            installed_config = json.loads(
                (project / ".codex" / "hooks.json").read_text(encoding="utf-8")
            )
            self.assertEqual(
                installed_config["hooks"]["SessionStart"][0]["matcher"],
                "^startup$",
            )

    def test_status_keeps_explicit_adapter_override_external(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            project = temporary / "project"
            project.mkdir()
            installation = temporary / "install"
            tools = temporary / "tools"
            tools.mkdir()
            which = _which(tools, "codex")

            with patch(
                "context_compactor.management._prepare_private_venv",
                side_effect=_fake_private_venv,
            ):
                installed = install_source(
                    project_root=project,
                    source_root=SOURCE_ROOT,
                    install_root=installation,
                    python=sys.executable,
                    hosts=("codex",),
                    model_command=(
                        sys.executable,
                        "-m",
                        "context_compactor.codex_adapter",
                    ),
                    now=NOW,
                    which=which,
                )

            self.assertEqual(installed["model_adapter"], "external")
            report = status(
                project_root=project,
                install_root=installation,
                hosts=("codex",),
                now=NOW,
                which=which,
            )
            self.assertTrue(report["hooks"][0]["model_available"])

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

    def test_global_codex_hook_registers_each_project_once_and_preserves_local_hooks(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            profile = temporary / "profile"
            profile.mkdir()
            automatic_project = temporary / "automatic project"
            automatic_project.mkdir()
            local_project = temporary / "local project"
            local_project.mkdir()
            installation = temporary / "install"
            tools = temporary / "tools"
            tools.mkdir()
            which = _which(tools, "codex")

            def is_global_root(root: Path) -> bool:
                return root.resolve(strict=False) == profile.resolve(
                    strict=False
                )

            with patch(
                "context_compactor.management._prepare_private_venv",
                side_effect=_fake_private_venv,
            ), patch(
                "context_compactor.management._is_global_codex_root",
                side_effect=is_global_root,
            ):
                installed = install_source(
                    project_root=profile,
                    source_root=SOURCE_ROOT,
                    install_root=installation,
                    python=sys.executable,
                    hosts=("codex",),
                    model_command=(sys.executable, "-c", "pass"),
                    now=NOW,
                    which=which,
                )

                install_manifest = json.loads(
                    (installation / "install.json").read_text(
                        encoding="utf-8"
                    )
                )
                self.assertEqual(len(install_manifest["projects"]), 1)
                owner_manifest_path = Path(
                    next(iter(install_manifest["projects"].values()))
                )
                owner_manifest = json.loads(
                    owner_manifest_path.read_text(encoding="utf-8")
                )
                self.assertEqual(
                    owner_manifest["hosts"]["codex"]["hook_scope"],
                    "global",
                )

                automatic_id = hashlib.sha256(
                    os.path.normcase(
                        str(automatic_project.resolve(strict=False))
                    ).encode("utf-8")
                ).hexdigest()
                automatic_manifest_path = (
                    installation / "projects" / f"{automatic_id}.json"
                )
                session_root = resolve_hook_project(
                    project_manifest=owner_manifest_path,
                    host="codex",
                    event_cwd=automatic_project,
                    register=False,
                    now=NOW,
                )
                self.assertEqual(session_root, automatic_project.resolve())
                self.assertFalse(automatic_manifest_path.exists())

                def register_session(_: int) -> Optional[Path]:
                    return resolve_hook_project(
                        project_manifest=owner_manifest_path,
                        host="codex",
                        event_cwd=automatic_project,
                        register=True,
                        now=NOW + timedelta(seconds=1),
                    )

                with ThreadPoolExecutor(max_workers=3) as executor:
                    registered_roots = list(
                        executor.map(register_session, range(3))
                    )
                self.assertEqual(
                    registered_roots,
                    [automatic_project.resolve()] * 3,
                )

                install_manifest = json.loads(
                    (installation / "install.json").read_text(
                        encoding="utf-8"
                    )
                )
                self.assertEqual(len(install_manifest["projects"]), 2)
                automatic_manifest = json.loads(
                    automatic_manifest_path.read_text(encoding="utf-8")
                )
                inherited = automatic_manifest["hosts"]["codex"]
                self.assertEqual(
                    inherited["hook_scope"],
                    "global_inherited",
                )
                self.assertEqual(
                    inherited["owner_project_id"],
                    owner_manifest["project_id"],
                )

                automatic_status = status(
                    project_root=automatic_project,
                    install_root=installation,
                    hosts=("codex",),
                    now=NOW,
                    which=which,
                )
                self.assertTrue(
                    automatic_status["hooks"][0]["definition_healthy"]
                )
                self.assertFalse(
                    automatic_status["hooks"][0][
                        "manual_trust_required"
                    ]
                )
                self.assertEqual(
                    automatic_status["hooks"][0]["state"],
                    "definition_ready",
                )

                global_config = profile / ".codex" / "hooks.json"
                global_config_before = global_config.read_bytes()
                install_source(
                    project_root=local_project,
                    source_root=SOURCE_ROOT,
                    install_root=installation,
                    python=sys.executable,
                    hosts=("codex",),
                    model_command=(sys.executable, "-c", "pass"),
                    now=NOW + timedelta(seconds=2),
                    which=which,
                )
                self.assertIsNone(
                    resolve_hook_project(
                        project_manifest=owner_manifest_path,
                        host="codex",
                        event_cwd=local_project,
                        register=True,
                        now=NOW + timedelta(seconds=3),
                    )
                )

                removed_automatic = uninstall(
                    project_root=automatic_project,
                    install_root=installation,
                    hosts=("codex",),
                )
                self.assertFalse(
                    removed_automatic["installation_removed"]
                )
                self.assertEqual(
                    global_config.read_bytes(),
                    global_config_before,
                )
                resolve_hook_project(
                    project_manifest=owner_manifest_path,
                    host="codex",
                    event_cwd=automatic_project,
                    register=True,
                    now=NOW + timedelta(seconds=4),
                )

                removed_global = uninstall(
                    project_root=profile,
                    install_root=installation,
                    hosts=("codex",),
                )
                self.assertEqual(
                    removed_global["auto_projects_removed"],
                    1,
                )
                self.assertFalse(removed_global["installation_removed"])
                self.assertFalse(automatic_manifest_path.exists())
                self.assertFalse(global_config.exists())

                local_status = status(
                    project_root=local_project,
                    install_root=installation,
                    hosts=("codex",),
                    now=NOW,
                    which=which,
                )
                self.assertTrue(
                    local_status["hooks"][0]["definition_healthy"]
                )
                removed_local = uninstall(
                    project_root=local_project,
                    install_root=installation,
                    hosts=("codex",),
                )
                self.assertTrue(removed_local["installation_removed"])
                self.assertFalse(installation.exists())


if __name__ == "__main__":
    unittest.main()
