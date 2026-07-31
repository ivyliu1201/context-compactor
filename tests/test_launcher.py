from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import time
import unittest
from datetime import datetime, timezone
from pathlib import Path
from typing import List

from context_compactor.journal import EnqueueRequest, Journal, digest_text
from context_compactor.launcher import (
    WINDOWS_CREATE_BREAKAWAY_FROM_JOB,
    WINDOWS_CREATE_NEW_PROCESS_GROUP,
    WINDOWS_CREATE_NO_WINDOW,
    WINDOWS_DETACHED_PROCESS,
    WINDOWS_FALLBACK_CREATION_FLAGS,
    WINDOWS_PRIMARY_CREATION_FLAGS,
    detached_process_options,
    launch_worker,
    start_detached_process,
)


def process_is_running(process_id: int) -> bool:
    if os.name == "nt":
        import ctypes

        synchronize = 0x00100000
        wait_timeout = 0x00000102
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel32.OpenProcess.argtypes = (
            ctypes.c_uint32,
            ctypes.c_int,
            ctypes.c_uint32,
        )
        kernel32.OpenProcess.restype = ctypes.c_void_p
        kernel32.WaitForSingleObject.argtypes = (
            ctypes.c_void_p,
            ctypes.c_uint32,
        )
        kernel32.WaitForSingleObject.restype = ctypes.c_uint32
        kernel32.CloseHandle.argtypes = (ctypes.c_void_p,)
        kernel32.CloseHandle.restype = ctypes.c_int
        handle = kernel32.OpenProcess(synchronize, False, process_id)
        if not handle:
            return False
        try:
            return kernel32.WaitForSingleObject(handle, 0) == wait_timeout
        finally:
            kernel32.CloseHandle(handle)
    try:
        os.kill(process_id, 0)
    except OSError:
        return False
    return True


class FakeProcess:
    def __init__(self, process_id: int = 321) -> None:
        self.pid = process_id

    def wait(self) -> int:
        return 0


class LauncherTests(unittest.TestCase):
    def test_windows_options_hide_window_and_inherit_no_streams(self) -> None:
        options = detached_process_options("nt")
        flags = int(options["creationflags"])

        self.assertEqual(options["stdin"], subprocess.DEVNULL)
        self.assertEqual(options["stdout"], subprocess.DEVNULL)
        self.assertEqual(options["stderr"], subprocess.DEVNULL)
        self.assertTrue(options["close_fds"])
        self.assertEqual(flags, WINDOWS_PRIMARY_CREATION_FLAGS)
        self.assertTrue(flags & WINDOWS_DETACHED_PROCESS)
        self.assertTrue(flags & WINDOWS_CREATE_NEW_PROCESS_GROUP)
        self.assertTrue(flags & WINDOWS_CREATE_BREAKAWAY_FROM_JOB)
        self.assertTrue(flags & WINDOWS_CREATE_NO_WINDOW)
        if os.name == "nt":
            startup = options["startupinfo"]
            self.assertTrue(startup.dwFlags & subprocess.STARTF_USESHOWWINDOW)
            self.assertEqual(startup.wShowWindow, subprocess.SW_HIDE)

    def test_windows_access_denied_retries_without_breakaway(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            calls: List[dict[str, object]] = []

            def popen(
                _command: object,
                **options: object,
            ) -> FakeProcess:
                calls.append(options)
                if len(calls) == 1:
                    error = PermissionError("synthetic access denied")
                    error.winerror = 5
                    raise error
                return FakeProcess()

            process_id = start_detached_process(
                (sys.executable, "-c", "pass"),
                cwd=directory,
                popen_factory=popen,
                platform_name="nt",
            )

            self.assertEqual(process_id, 321)
            self.assertEqual(len(calls), 2)
            self.assertEqual(
                calls[0]["creationflags"],
                WINDOWS_PRIMARY_CREATION_FLAGS,
            )
            self.assertEqual(
                calls[1]["creationflags"],
                WINDOWS_FALLBACK_CREATION_FLAGS,
            )
            self.assertTrue(
                int(calls[1]["creationflags"]) & WINDOWS_CREATE_NO_WINDOW
            )
            self.assertEqual(calls[1]["stdin"], subprocess.DEVNULL)
            self.assertEqual(calls[1]["stdout"], subprocess.DEVNULL)
            self.assertEqual(calls[1]["stderr"], subprocess.DEVNULL)

    def test_short_lived_parent_exits_before_detached_child_completes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            model_script = root / "delayed model.py"
            model_script.write_text(
                "\n".join(
                    (
                        "import json",
                        "import sys",
                        "import time",
                        "json.load(sys.stdin)",
                        "time.sleep(1.2)",
                        "print(json.dumps({'outcome': 'no_change'}))",
                    )
                ),
                encoding="utf-8",
            )
            now = datetime.now(timezone.utc)
            with Journal.open(root) as journal:
                journal.enqueue(
                    EnqueueRequest(
                        event_id="event-parent-exit",
                        kind="user_prompt",
                        occurred_at=now,
                        content_digest=digest_text("parent exit event"),
                        prompt="Explain why the worker must outlive the Hook.",
                        redaction_count=0,
                        enqueued_at=now,
                    )
                )
            source_root = Path(__file__).resolve().parent.parent
            parent_code = (
                "import sys;"
                f"sys.path.insert(0,{str(source_root)!r});"
                "from context_compactor.launcher import launch_worker;"
                "result=launch_worker("
                f"{str(root)!r},(sys.executable,{str(model_script)!r}));"
                "print(result.process_id)"
            )

            parent = subprocess.run(
                (sys.executable, "-c", parent_code),
                cwd=str(root),
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=10,
                check=False,
            )

            self.assertEqual(parent.returncode, 0, parent.stderr.decode("utf-8"))
            process_id = int(parent.stdout.decode("utf-8").strip())
            self.assertTrue(process_is_running(process_id))
            deadline = time.monotonic() + 15
            snapshot = None
            while time.monotonic() < deadline:
                with Journal.open(root) as journal:
                    snapshot = journal.job_snapshot("event-parent-exit")
                if snapshot.status == "completed":
                    break
                time.sleep(0.05)
            self.assertIsNotNone(snapshot)
            self.assertEqual(snapshot.status, "completed")
            self.assertEqual(snapshot.outcome, "no_change")
            self.assertFalse(snapshot.prompt_present)
            process_deadline = time.monotonic() + 10
            while (
                process_is_running(process_id)
                and time.monotonic() < process_deadline
            ):
                time.sleep(0.05)
            self.assertFalse(process_is_running(process_id))

    def test_concurrent_launches_result_in_one_model_drain(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            model_script = root / "fake model.py"
            counter = root / "model-calls.txt"
            model_script.write_text(
                "\n".join(
                    (
                        "import json",
                        "import pathlib",
                        "import sys",
                        "import time",
                        "json.load(sys.stdin)",
                        f"counter = pathlib.Path({str(counter)!r})",
                        "with counter.open('a', encoding='utf-8') as stream:",
                        "    stream.write('called\\n')",
                        "    stream.flush()",
                        "time.sleep(0.5)",
                        "print(json.dumps({'outcome': 'no_change'}))",
                    )
                ),
                encoding="utf-8",
            )
            now = datetime.now(timezone.utc)
            with Journal.open(root) as journal:
                journal.enqueue(
                    EnqueueRequest(
                        event_id="event-concurrent-launch",
                        kind="user_prompt",
                        occurred_at=now,
                        content_digest=digest_text("raw synthetic event"),
                        prompt="Explain the current architecture.",
                        redaction_count=0,
                        enqueued_at=now,
                    )
                )

            results = [
                launch_worker(
                    root,
                    (sys.executable, str(model_script)),
                )
                for _ in range(4)
            ]
            self.assertTrue(any(result.launched for result in results))

            deadline = time.monotonic() + 15
            snapshot = None
            while time.monotonic() < deadline:
                with Journal.open(root) as journal:
                    snapshot = journal.job_snapshot("event-concurrent-launch")
                if snapshot.status == "completed":
                    break
                time.sleep(0.05)

            self.assertIsNotNone(snapshot)
            self.assertEqual(snapshot.status, "completed")
            self.assertEqual(snapshot.outcome, "no_change")
            self.assertEqual(snapshot.attempt_count, 1)
            self.assertFalse(snapshot.prompt_present)
            calls = counter.read_text(encoding="utf-8").splitlines()
            self.assertEqual(calls, ["called"])
            launched_processes = [
                result.process_id
                for result in results
                if result.process_id is not None
            ]
            process_deadline = time.monotonic() + 15
            while (
                any(process_is_running(process_id) for process_id in launched_processes)
                and time.monotonic() < process_deadline
            ):
                time.sleep(0.05)
            self.assertFalse(
                any(process_is_running(process_id) for process_id in launched_processes)
            )

    def test_non_windows_options_start_a_new_session(self) -> None:
        options = detached_process_options("posix")

        self.assertTrue(options["start_new_session"])
        self.assertNotIn("creationflags", options)
        self.assertEqual(options["stdin"], subprocess.DEVNULL)
        self.assertEqual(options["stdout"], subprocess.DEVNULL)
        self.assertEqual(options["stderr"], subprocess.DEVNULL)


if __name__ == "__main__":
    unittest.main()
