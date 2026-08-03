from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import List, Sequence

from context_compactor.host import (
    HOST_CLAUDE,
    HOST_CODEX,
    EVENT_POST_COMPACT,
    EVENT_SESSION_START,
    HookError,
    diagnostic_line,
    encode_host_output,
    handle_hook,
    normalize_hook,
)
from context_compactor.launcher import LaunchResult
from context_compactor.paths import PathValue, project_paths
from context_compactor.privacy import REDACTION_MARKER
from context_compactor.state import ProjectState, StateEntry, publish_state

NOW = datetime(2026, 7, 31, 4, 0, tzinfo=timezone.utc)


def codex_payload(event_name: str, **changes: object) -> bytes:
    values = {
        "session_id": "session-1",
        "transcript_path": None,
        "cwd": "C:\\repo",
        "hook_event_name": event_name,
        "model": "gpt-5",
    }
    if event_name == "SessionStart":
        values.update(permission_mode="default", source="resume")
    elif event_name == "UserPromptSubmit":
        values.update(
            turn_id="turn-7",
            permission_mode="acceptEdits",
            prompt="keep this transient",
        )
    elif event_name == "SubagentStart":
        values.update(
            turn_id="turn-7",
            permission_mode="plan",
            agent_id="agent-1",
            agent_type="worker",
        )
    else:
        values.update(turn_id="turn-7", trigger="auto")
    values.update(changes)
    return json.dumps(values).encode("utf-8")


def claude_payload(event_name: str, **changes: object) -> bytes:
    values = {
        "session_id": "session-1",
        "prompt_id": "prompt-7",
        "transcript_path": "C:\\private.jsonl",
        "cwd": "C:\\repo",
        "hook_event_name": event_name,
    }
    if event_name == "SessionStart":
        values.update(source="resume", model="claude-sonnet")
    elif event_name == "UserPromptSubmit":
        values.update(
            permission_mode="auto",
            effort={"level": "xhigh"},
            prompt="keep this transient",
        )
    elif event_name == "SubagentStart":
        values.update(agent_id="agent-1", agent_type="Explore")
    elif event_name == "PreCompact":
        values.update(trigger="manual", custom_instructions="retain focus")
    else:
        values.update(trigger="auto", compact_summary="derived summary")
    values.update(changes)
    return json.dumps(values).encode("utf-8")


class RecordingLauncher:
    def __init__(self) -> None:
        self.calls: List[tuple[PathValue, tuple[str, ...]]] = []

    def __call__(
        self,
        project_root: PathValue,
        model_command: Sequence[str],
    ) -> LaunchResult:
        self.calls.append((project_root, tuple(model_command)))
        return LaunchResult(launched=True, already_running=False, process_id=321)


class HostTests(unittest.TestCase):
    def test_normalizes_supported_codex_and_claude_events(self) -> None:
        for host, factory in (
            (HOST_CODEX, codex_payload),
            (HOST_CLAUDE, claude_payload),
        ):
            for event_name, kind in (
                ("SessionStart", "session_start"),
                ("UserPromptSubmit", "user_prompt"),
                ("SubagentStart", "subagent_start"),
                ("PreCompact", "pre_compact"),
                ("PostCompact", "post_compact"),
            ):
                with self.subTest(host=host, event=event_name):
                    event = normalize_hook(
                        host,
                        factory(event_name),
                        received_at=NOW,
                    )
                    self.assertEqual(event.host, host)
                    self.assertEqual(event.kind, kind)
                    self.assertEqual(event.occurred_at, NOW)
                    self.assertRegex(
                        event.event_id,
                        r"^(codex|claude)-[0-9a-f]{64}$",
                    )

    def test_rejects_unknown_missing_duplicate_and_cross_event_fields(self) -> None:
        invalid = (
            (HOST_CODEX, codex_payload("SessionStart", extra=True)),
            (
                HOST_CODEX,
                b'{"session_id":"one","session_id":"two",'
                b'"hook_event_name":"SessionStart"}',
            ),
            (
                HOST_CODEX,
                codex_payload("UserPromptSubmit", prompt=None),
            ),
            (
                HOST_CLAUDE,
                claude_payload("SessionStart", transcript_path=None),
            ),
            (
                HOST_CLAUDE,
                claude_payload("UserPromptSubmit", permission_mode="manual"),
            ),
            (
                HOST_CLAUDE,
                claude_payload("PostCompact", compact_summary=None),
            ),
        )
        for host, payload in invalid:
            with self.subTest(host=host, payload=payload):
                with self.assertRaises(HookError):
                    normalize_hook(host, payload, received_at=NOW)

    def test_prompt_is_redacted_bounded_idempotent_and_launched_without_waiting(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = RecordingLauncher()
            synthetic_secret = "sk-" + "SYNTHETIC123456789"
            prompt = (
                f"Authorization: Bearer {synthetic_secret}; "
                f"password={synthetic_secret}; continue"
            )
            payload = codex_payload("UserPromptSubmit", prompt=prompt)

            started = time.monotonic()
            first = handle_hook(
                HOST_CODEX,
                payload,
                ("model-command",),
                project_root=root,
                received_at=NOW,
                launcher=launcher,
            )
            elapsed = time.monotonic() - started
            second = handle_hook(
                HOST_CODEX,
                payload,
                ("model-command",),
                project_root=root,
                received_at=NOW + timedelta(seconds=10),
                launcher=launcher,
            )

            self.assertLess(elapsed, 0.25)
            self.assertTrue(first.enqueued)
            self.assertTrue(first.worker_started)
            self.assertFalse(second.enqueued)
            self.assertEqual(len(launcher.calls), 2)
            self.assertEqual(first.output, b"")
            self.assertEqual(second.output, b"")

            paths = project_paths(root)
            connection = sqlite3.connect(str(paths.journal))
            try:
                row = connection.execute(
                    "SELECT prompt_text, redaction_count FROM jobs"
                ).fetchone()
                count = connection.execute("SELECT COUNT(*) FROM events").fetchone()[0]
            finally:
                connection.close()
            self.assertEqual(count, 1)
            self.assertNotIn(synthetic_secret, row[0])
            self.assertIn(REDACTION_MARKER, row[0])
            self.assertGreaterEqual(row[1], 2)

    def test_repeated_session_start_is_read_only_and_never_launches(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = RecordingLauncher()
            payload = codex_payload(EVENT_SESSION_START, source="startup")

            first = handle_hook(
                HOST_CODEX,
                payload,
                ("unused-model",),
                project_root=root,
                received_at=NOW,
                launcher=launcher,
            )
            second = handle_hook(
                HOST_CODEX,
                payload,
                ("unused-model",),
                project_root=root,
                received_at=NOW + timedelta(seconds=1),
                launcher=launcher,
            )

            self.assertEqual(first.output, b"")
            self.assertEqual(second.output, b"")
            self.assertFalse(first.enqueued)
            self.assertFalse(second.enqueued)
            self.assertFalse(first.worker_started)
            self.assertFalse(second.worker_started)
            self.assertEqual(launcher.calls, [])
            self.assertFalse(project_paths(root).journal.exists())

    def test_injects_last_state_using_both_host_output_protocols(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            publish_state(
                root,
                ProjectState(
                    project_summary="Local-first memory tool.",
                    current_focus="Finish the Host adapter.",
                    goals=(StateEntry("Keep foreground latency bounded."),),
                ),
            )
            for host, payload in (
                (HOST_CODEX, codex_payload(EVENT_SESSION_START)),
                (HOST_CLAUDE, claude_payload(EVENT_SESSION_START)),
            ):
                with self.subTest(host=host):
                    result = handle_hook(
                        host,
                        payload,
                        ("unused-model",),
                        project_root=root,
                        received_at=NOW,
                        launcher=RecordingLauncher(),
                    )
                    decoded = json.loads(result.output)
                    self.assertTrue(decoded["continue"])
                    specific = decoded["hookSpecificOutput"]
                    self.assertEqual(
                        specific["hookEventName"],
                        EVENT_SESSION_START,
                    )
                    context = specific["additionalContext"]
                    self.assertIn('authority="derived"', context)
                    self.assertIn("Finish the Host adapter.", context)
                    self.assertIn("latest user instruction", context)

    def test_empty_or_invalid_state_emits_zero_stdout_bytes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            empty = handle_hook(
                HOST_CODEX,
                codex_payload(EVENT_SESSION_START),
                ("unused-model",),
                project_root=root,
                received_at=NOW,
                launcher=RecordingLauncher(),
            )
            self.assertEqual(empty.output, b"")
            self.assertEqual(empty.diagnostic_categories, ())

            paths = project_paths(root)
            paths.data_dir.mkdir()
            paths.state.write_text("not: supported\n", encoding="utf-8")
            invalid = handle_hook(
                HOST_CLAUDE,
                claude_payload(EVENT_SESSION_START),
                ("unused-model",),
                project_root=root,
                received_at=NOW,
                launcher=RecordingLauncher(),
            )
            self.assertEqual(invalid.output, b"")
            self.assertEqual(invalid.diagnostic_categories, ("state_invalid",))

    def test_compact_events_cannot_emit_context(self) -> None:
        self.assertEqual(
            encode_host_output(HOST_CODEX, EVENT_POST_COMPACT, ""),
            b"",
        )
        with self.assertRaises(HookError):
            encode_host_output(
                HOST_CLAUDE,
                EVENT_POST_COMPACT,
                "not supported",
            )

    def test_diagnostics_are_identifier_only_and_secret_free(self) -> None:
        synthetic_secret = "sk-" + "SYNTHETIC123456789"
        line = diagnostic_line(synthetic_secret, synthetic_secret)
        self.assertEqual(
            line,
            "context-compactor event_id=unknown category=unknown",
        )
        self.assertNotIn(synthetic_secret, line)

    def test_hook_cli_reserves_stdout_and_redacts_failure_diagnostics(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source_root = Path(__file__).resolve().parent.parent
            environment = os.environ.copy()
            environment["PYTHONDONTWRITEBYTECODE"] = "1"
            command = (
                sys.executable,
                "-m",
                "context_compactor",
                "hook",
                "--host",
                HOST_CODEX,
                "--project-root",
                str(root),
                "--model-command",
                "unused-model",
            )
            success = subprocess.run(
                command,
                cwd=str(source_root),
                env=environment,
                input=codex_payload(EVENT_SESSION_START),
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=10,
                check=False,
            )

            self.assertEqual(success.returncode, 0)
            self.assertEqual(success.stdout, b"")
            self.assertEqual(success.stderr, b"")

            synthetic_secret = "sk-" + "SYNTHETIC123456789"
            failure = subprocess.run(
                command,
                cwd=str(source_root),
                env=environment,
                input=json.dumps(
                    {
                        "hook_event_name": EVENT_SESSION_START,
                        "prompt": synthetic_secret,
                    }
                ).encode("utf-8"),
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=10,
                check=False,
            )

            diagnostic = failure.stderr.decode("utf-8")
            self.assertEqual(failure.returncode, 1)
            self.assertEqual(failure.stdout, b"")
            self.assertEqual(
                diagnostic.strip(),
                "context-compactor event_id=unknown category=invalid_payload",
            )
            self.assertNotIn(synthetic_secret, diagnostic)

    def test_multiple_sessions_share_one_project_journal(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = RecordingLauncher()
            results = []
            for index in range(3):
                results.append(
                    handle_hook(
                        HOST_CODEX,
                        codex_payload(
                            "UserPromptSubmit",
                            session_id=f"session-{index}",
                            turn_id="turn-1",
                            prompt=f"remember project decision {index}",
                        ),
                        ("model-command",),
                        project_root=root,
                        received_at=NOW + timedelta(seconds=index),
                        launcher=launcher,
                    )
                )

            self.assertTrue(all(result.enqueued for result in results))
            self.assertEqual(
                [Path(call[0]).resolve() for call in launcher.calls],
                [root.resolve()] * 3,
            )
            paths = project_paths(root)
            self.assertTrue(paths.data_dir.is_dir())
            connection = sqlite3.connect(str(paths.journal))
            try:
                event_count = connection.execute(
                    "SELECT COUNT(*) FROM events"
                ).fetchone()[0]
            finally:
                connection.close()
            self.assertEqual(event_count, 3)

    def test_hook_cli_uses_global_manifest_to_select_event_cwd(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            owner = temporary / "profile"
            owner.mkdir()
            target = temporary / "project"
            target.mkdir()
            installation = temporary / "install"
            projects = installation / "projects"
            projects.mkdir(parents=True)

            owner_root = owner.resolve()
            owner_id = hashlib.sha256(
                os.path.normcase(str(owner_root)).encode("utf-8")
            ).hexdigest()
            owner_manifest_path = projects / f"{owner_id}.json"
            owner_manifest_path.write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "project_id": owner_id,
                        "project_root": str(owner_root),
                        "install_root": str(installation.resolve()),
                        "python_interpreter": sys.executable,
                        "model_command": ["unused-model"],
                        "model_adapter": "external",
                        "hosts": {
                            "codex": {
                                "hook_scope": "global",
                                "config_path": str(
                                    owner_root / ".codex" / "hooks.json"
                                ),
                                "command": "unused-command",
                                "command_windows": "unused-command",
                                "config_created": True,
                            }
                        },
                    }
                ),
                encoding="utf-8",
            )
            (installation / "install.json").write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "install_root": str(installation.resolve()),
                        "projects": {
                            owner_id: str(owner_manifest_path.resolve())
                        },
                    }
                ),
                encoding="utf-8",
            )
            publish_state(
                target,
                ProjectState(
                    current_focus="Use the event cwd project.",
                ),
            )

            source_root = Path(__file__).resolve().parent.parent
            environment = os.environ.copy()
            environment.update(
                {
                    "CONTEXT_COMPACTOR_PROJECT_MANIFEST": str(
                        owner_manifest_path
                    ),
                    "PYTHONDONTWRITEBYTECODE": "1",
                }
            )
            completed = subprocess.run(
                (
                    sys.executable,
                    "-m",
                    "context_compactor",
                    "hook",
                    "--host",
                    HOST_CODEX,
                    "--project-root",
                    str(owner),
                    "--model-command",
                    "unused-model",
                ),
                cwd=str(source_root),
                env=environment,
                input=codex_payload(
                    EVENT_SESSION_START,
                    cwd=str(target),
                    source="startup",
                ),
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=10,
                check=False,
            )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            decoded = json.loads(completed.stdout)
            context = decoded["hookSpecificOutput"]["additionalContext"]
            self.assertIn("Use the event cwd project.", context)
            self.assertEqual(len(tuple(projects.glob("*.json"))), 1)


if __name__ == "__main__":
    unittest.main()
