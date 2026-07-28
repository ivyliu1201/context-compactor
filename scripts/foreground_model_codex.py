#!/usr/bin/env python3
"""Codex CLI wrapper for context-compactor foreground benchmarks."""

from __future__ import annotations

import hashlib
import hmac
import http.client
import json
import os
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


REQUEST_PROTOCOL = "context-compactor/foreground-model-check/v1"
ADAPTER_VERSION = "context-compactor/foreground-model-codex/v1"
DEFAULT_MODEL = "gpt-5.6-sol"
DEFAULT_REASONING_EFFORT = "high"
DEFAULT_TIMEOUT_SECONDS = 300
DEFAULT_PROXY_PORT = 8765
DEFAULT_BROKER_WAIT_SECONDS = 3600
MAX_PROXY_REQUEST_BYTES = 16 * 1024 * 1024

FULL_TRANSCRIPT_PROMPT_RULES = (
    "For transcript input, scan every turn in chronological order.",
    (
        "For transcript active requirement, track establish, replace, and "
        "superseded actions across the entire transcript."
    ),
    (
        "Return the canonical requirement name from the latest establish or "
        "replace action that has not been superseded, not the whole action "
        "identifier."
    ),
    (
        "A continue or resume action does not replace the active requirement; "
        "resume transcripts require tracing the earlier establish action."
    ),
    (
        "For transcript current focus, copy the final turn's agent_response "
        "exactly; never use user_input and never summarize it."
    ),
    (
        "For transcript next action, copy the final item in the final turn's "
        "tool_activities exactly; never infer an action."
    ),
)


class RetryableTransportError(RuntimeError):
    pass


def main(argv: list[str]) -> int:
    if argv == ["--self-test"]:
        run_self_test()
        return 0
    if argv == ["--broker"]:
        serve_broker()
        return 0
    if argv == ["--client"]:
        broker_client()
        return 0
    if argv == ["--worker"]:
        run_broker_worker()
        return 0
    if argv == ["--shutdown"]:
        shutdown_broker()
        return 0
    if argv[:2] == ["--broker-run", "--"] and len(argv) > 2:
        return broker_run(argv[2:])
    if argv:
        fail(
            "usage: foreground_model_codex.py "
            "[--self-test|--broker|--client|--worker|--shutdown]"
        )

    try:
        output = evaluate_request(sys.stdin.buffer.read())
    except RetryableTransportError as exc:
        fail(str(exc))
    write_output(output)
    return 0


def evaluation_config() -> dict[str, Any]:
    model = environment_value("CODEX_FOREGROUND_MODEL", DEFAULT_MODEL)
    reasoning_effort = environment_value(
        "CODEX_FOREGROUND_REASONING_EFFORT",
        DEFAULT_REASONING_EFFORT,
    )
    timeout_seconds = parse_positive_int(
        "CODEX_FOREGROUND_TIMEOUT_SECONDS",
        DEFAULT_TIMEOUT_SECONDS,
    )
    codex_command = find_codex_command()
    codex_version = read_codex_version(codex_command)
    return {
        "model": model,
        "reasoning_effort": reasoning_effort,
        "timeout_seconds": timeout_seconds,
        "codex_command": codex_command,
        "codex_version": codex_version,
    }


def evaluate_request(
    raw: bytes,
    config: dict[str, Any] | None = None,
) -> dict[str, Any]:
    request = read_benchmark_request(raw)
    config = config or evaluation_config()
    model = str(config["model"])
    reasoning_effort = str(config["reasoning_effort"])
    codex_version = str(config["codex_version"])
    events = invoke_codex(
        codex_command=str(config["codex_command"]),
        model=model,
        reasoning_effort=reasoning_effort,
        prompt=build_prompt(request),
        timeout_seconds=int(config["timeout_seconds"]),
    )
    content, usage = extract_codex_result(events)
    token_basis = "observed" if usage is not None else "not_evaluated"
    usage = usage or {}
    return {
        "content": content,
        "input_tokens": integer_usage(usage, "input_tokens"),
        "cached_input_tokens": integer_usage(usage, "cached_input_tokens"),
        "output_tokens": integer_usage(usage, "output_tokens"),
        "reasoning_output_tokens": integer_usage(
            usage,
            "reasoning_output_tokens",
        ),
        "token_basis": token_basis,
        "provider": "openai-chatgpt-codex",
        "model": model,
        "model_revision": "unavailable",
        "reasoning_effort": reasoning_effort,
        "sampling_seed_status": "unsupported",
        "runner_version": f"{ADAPTER_VERSION};{codex_version}",
        "tool_definition_digest": tool_definition_digest(codex_version),
    }


def write_output(output: dict[str, Any]) -> None:
    json.dump(output, sys.stdout, ensure_ascii=False, separators=(",", ":"))
    sys.stdout.write("\n")


class PendingBrokerRequest:
    def __init__(self, raw: bytes) -> None:
        self.identifier = uuid.uuid4().hex
        self.raw = raw
        self.completed = threading.Event()
        self.output: bytes | None = None
        self.error = ""


class BrokerState:
    def __init__(self) -> None:
        self.requests: queue.Queue[PendingBrokerRequest] = queue.Queue()
        self.pending: dict[str, PendingBrokerRequest] = {}
        self.lock = threading.Lock()
        self.stopping = threading.Event()

    def submit(self, raw: bytes) -> PendingBrokerRequest:
        pending = PendingBrokerRequest(raw)
        with self.lock:
            self.pending[pending.identifier] = pending
        self.requests.put(pending)
        return pending

    def next_request(self) -> PendingBrokerRequest | None:
        try:
            return self.requests.get(timeout=2)
        except queue.Empty:
            return None

    def complete(
        self,
        identifier: str,
        output: bytes | None,
        error: str,
    ) -> bool:
        with self.lock:
            pending = self.pending.pop(identifier, None)
        if pending is None:
            return False
        pending.output = output
        pending.error = error
        pending.completed.set()
        return True


def serve_broker() -> None:
    token = os.environ.get("CONTEXT_COMPACTOR_PROXY_TOKEN", "").strip()
    if not token:
        fail("CONTEXT_COMPACTOR_PROXY_TOKEN is required for --broker")
    port = parse_positive_int(
        "CONTEXT_COMPACTOR_PROXY_PORT",
        DEFAULT_PROXY_PORT,
    )
    state = BrokerState()

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            provided = self.headers.get("Authorization", "")
            expected = "Bearer " + token
            if not hmac.compare_digest(provided, expected):
                self.send_error(401)
                return
            if self.path == "/v1/evaluate":
                self.handle_evaluate()
                return
            if self.path == "/v1/health":
                self.send_response(204)
                self.end_headers()
                return
            if self.path == "/v1/next":
                self.handle_next()
                return
            if self.path.startswith("/v1/result/"):
                self.handle_result(self.path.removeprefix("/v1/result/"))
                return
            if self.path == "/v1/shutdown":
                self.send_response(204)
                self.end_headers()
                state.stopping.set()
                threading.Thread(
                    target=self.server.shutdown,
                    daemon=True,
                ).start()
                return
            self.send_error(404)

        def read_body(self) -> bytes | None:
            try:
                content_length = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                self.send_error(400)
                return None
            if content_length <= 0 or content_length > MAX_PROXY_REQUEST_BYTES:
                self.send_error(413)
                return None
            return self.rfile.read(content_length)

        def handle_evaluate(self) -> None:
            raw = self.read_body()
            if raw is None:
                return
            pending = state.submit(raw)
            wait_seconds = parse_positive_int(
                "CONTEXT_COMPACTOR_BROKER_WAIT_SECONDS",
                DEFAULT_BROKER_WAIT_SECONDS,
            )
            if not pending.completed.wait(wait_seconds):
                state.complete(
                    pending.identifier,
                    None,
                    "Codex broker worker timed out",
                )
                self.send_error(504)
                return
            if pending.error or pending.output is None:
                self.send_error(502, pending.error or "Codex worker failed")
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(pending.output)))
            self.end_headers()
            self.wfile.write(pending.output)

        def handle_next(self) -> None:
            while not state.stopping.is_set():
                pending = state.next_request()
                if pending is None:
                    continue
                encoded = json.dumps(
                    {
                        "id": pending.identifier,
                        "request": json.loads(pending.raw),
                    },
                    ensure_ascii=False,
                    separators=(",", ":"),
                ).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)
                return
            self.send_response(410)
            self.end_headers()

        def handle_result(self, identifier: str) -> None:
            raw = self.read_body()
            if raw is None:
                return
            try:
                result = json.loads(raw)
            except json.JSONDecodeError:
                self.send_error(400)
                return
            if not isinstance(result, dict):
                self.send_error(400)
                return
            output_value = result.get("output")
            error = str(result.get("error") or "")
            output = None
            if isinstance(output_value, dict):
                output = json.dumps(
                    output_value,
                    ensure_ascii=False,
                    separators=(",", ":"),
                ).encode("utf-8")
            if not state.complete(identifier, output, error):
                self.send_error(404)
                return
            self.send_response(204)
            self.end_headers()

        def log_message(self, _format: str, *args: Any) -> None:
            return

    server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    print(
        f"foreground_model_codex broker listening on container port {port}",
        file=sys.stderr,
        flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


def broker_client() -> None:
    raw = sys.stdin.buffer.read()
    response = broker_request("/v1/evaluate", raw)
    sys.stdout.buffer.write(response)
    sys.stdout.buffer.write(b"\n")


def run_broker_worker() -> None:
    config = evaluation_config()
    handled_request = False
    while True:
        try:
            raw = broker_request("/v1/next", b"{}")
        except urllib.error.HTTPError as exc:
            if exc.code == 410:
                return
            raise
        except (
            ConnectionResetError,
            http.client.RemoteDisconnected,
            urllib.error.URLError,
        ):
            if handled_request:
                return
            raise
        envelope = json.loads(raw)
        identifier = str(envelope.get("id") or "")
        request = envelope.get("request")
        if not identifier or not isinstance(request, dict):
            fail("broker returned an invalid work item")
        result: dict[str, Any]
        cached_output = load_cached_output(request, config)
        while True:
            try:
                output = cached_output or evaluate_request(
                    json.dumps(
                        request,
                        ensure_ascii=False,
                        separators=(",", ":"),
                    ).encode("utf-8"),
                    config,
                )
                if cached_output is None:
                    save_cached_output(request, config, output)
                result = {"output": output}
                break
            except RetryableTransportError:
                print(
                    "foreground_model_codex: DNS unavailable; retrying "
                    "the same request in 15 seconds",
                    file=sys.stderr,
                    flush=True,
                )
                time.sleep(15)
            except SystemExit:
                result = {"error": "Codex foreground evaluation failed"}
                break
        broker_request(
            f"/v1/result/{identifier}",
            json.dumps(result, ensure_ascii=False, separators=(",", ":")).encode(
                "utf-8"
            ),
        )
        handled_request = True


def load_cached_output(
    request: dict[str, Any],
    config: dict[str, Any],
) -> dict[str, Any] | None:
    path = model_cache_path(request, config)
    if path is None or not os.path.isfile(path):
        return None
    try:
        with open(path, "r", encoding="utf-8") as handle:
            cached = json.load(handle)
    except (OSError, json.JSONDecodeError):
        return None
    if not isinstance(cached, dict) or not str(cached.get("content") or "").strip():
        return None
    return cached


def save_cached_output(
    request: dict[str, Any],
    config: dict[str, Any],
    output: dict[str, Any],
) -> None:
    path = model_cache_path(request, config)
    if path is None:
        return
    directory = os.path.dirname(path)
    os.makedirs(directory, exist_ok=True)
    temporary = path + f".{os.getpid()}.tmp"
    try:
        with open(temporary, "w", encoding="utf-8", newline="\n") as handle:
            json.dump(
                output,
                handle,
                ensure_ascii=False,
                separators=(",", ":"),
            )
            handle.write("\n")
        os.replace(temporary, path)
    finally:
        try:
            os.remove(temporary)
        except FileNotFoundError:
            pass


def model_cache_path(
    request: dict[str, Any],
    config: dict[str, Any],
) -> str | None:
    root = os.environ.get("CONTEXT_COMPACTOR_MODEL_CACHE", "").strip()
    if not root:
        return None
    encoded = json.dumps(
        {
            "adapter_version": ADAPTER_VERSION,
            "codex_version": config["codex_version"],
            "model": config["model"],
            "reasoning_effort": config["reasoning_effort"],
            "prompt_digest": prompt_digest(request),
            "request": request,
        },
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    digest = hashlib.sha256(encoded).hexdigest()
    return os.path.join(root, digest + ".json")


def prompt_digest(request: dict[str, Any]) -> str:
    prompt = build_prompt(request).encode("utf-8")
    return hashlib.sha256(prompt).hexdigest()


def shutdown_broker() -> None:
    broker_request("/v1/shutdown", b"{}")


def broker_run(command: list[str]) -> int:
    broker = subprocess.Popen(
        [sys.executable, os.path.abspath(__file__), "--broker"],
        stdin=subprocess.DEVNULL,
    )
    try:
        wait_for_broker()
        completed = subprocess.run(command, check=False)
        return completed.returncode
    finally:
        try:
            shutdown_broker()
        except (SystemExit, urllib.error.URLError, urllib.error.HTTPError):
            broker.terminate()
        try:
            broker.wait(timeout=10)
        except subprocess.TimeoutExpired:
            broker.kill()
            broker.wait()


def wait_for_broker() -> None:
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            broker_request("/v1/health", b"{}")
            return
        except (SystemExit, urllib.error.URLError, urllib.error.HTTPError):
            time.sleep(0.1)
    fail("Codex broker did not become ready")


def broker_request(path: str, body: bytes) -> bytes:
    token = os.environ.get("CONTEXT_COMPACTOR_PROXY_TOKEN", "").strip()
    if not token:
        fail("CONTEXT_COMPACTOR_PROXY_TOKEN is required for broker access")
    base_url = os.environ.get(
        "CONTEXT_COMPACTOR_BROKER_URL",
        f"http://127.0.0.1:{DEFAULT_PROXY_PORT}",
    ).rstrip("/")
    request = urllib.request.Request(
        base_url + path,
        data=body,
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
        },
        method="POST",
    )
    timeout_seconds = parse_positive_int(
        "CONTEXT_COMPACTOR_BROKER_WAIT_SECONDS",
        DEFAULT_BROKER_WAIT_SECONDS,
    )
    try:
        with urllib.request.urlopen(
            request,
            timeout=timeout_seconds,
        ) as response:
            return response.read()
    except urllib.error.HTTPError:
        raise
    except urllib.error.URLError as exc:
        fail(f"Codex broker request failed: {exc.reason}")


def read_benchmark_request(raw: bytes) -> dict[str, Any]:
    if not raw.strip():
        fail("benchmark request JSON is required on stdin")
    try:
        request = json.loads(raw)
    except json.JSONDecodeError as exc:
        fail(f"decode benchmark request: {exc}")
    if not isinstance(request, dict):
        fail("benchmark request must be a JSON object")
    if request.get("protocol") != REQUEST_PROTOCOL:
        fail("unsupported benchmark request protocol")
    rendered = request.get("rendered_input")
    if not isinstance(rendered, str) or not rendered.strip():
        fail("benchmark request rendered_input is required")
    return request


def build_prompt(request: dict[str, Any]) -> str:
    metadata = {
        "matrix": request.get("matrix"),
        "scenario": request.get("scenario"),
        "seed": request.get("seed"),
        "mode": request.get("mode"),
        "turn_number": request.get("turn_number"),
        "fixed": request.get("fixed"),
        "diagnostic": request.get("diagnostic", False),
        "diagnostic_for_turn": request.get("diagnostic_for_turn"),
        "event_reasons": request.get("event_reasons", []),
    }
    mode_instructions: list[str]
    if request.get("mode") == "full_transcript":
        mode_instructions = list(FULL_TRANSCRIPT_PROMPT_RULES)
    elif request.get("mode") == "summary_only":
        mode_instructions = [
            (
                "For summary input, copy current_progress as current focus "
                "exactly and copy last_verification as next action exactly."
            ),
        ]
    else:
        mode_instructions = [
            (
                "For compiled input, use the latest verification or "
                "test-result value as next action."
            ),
        ]
    return "\n".join(
        [
            "Evaluate one context-compactor benchmark checkpoint.",
            "Do not call tools. Use only rendered_input as evidence.",
            "Metadata identifies the cell and is not answer evidence.",
            "Preserve exact synthetic identifiers from rendered_input.",
            *mode_instructions,
            "Return concise plain text with exactly these labels:",
            "active requirement: <exact value or unknown>",
            "current focus: <exact value or unknown>",
            "next action: <exact value or unknown>",
            "superseded requirement: <exact value marked superseded/inactive, or unknown>",
            "unknown fields: <comma-separated labels or none>",
            "",
            "metadata:",
            json.dumps(metadata, ensure_ascii=False, separators=(",", ":")),
            "",
            "rendered_input:",
            str(request["rendered_input"]),
        ]
    )


def find_codex_command() -> str:
    configured = os.environ.get("CODEX_COMMAND", "").strip()
    if configured:
        return configured
    candidates = ["codex.cmd", "codex"] if os.name == "nt" else ["codex"]
    for candidate in candidates:
        resolved = shutil.which(candidate)
        if resolved:
            return resolved
    fail("Codex CLI was not found; set CODEX_COMMAND")


def read_codex_version(codex_command: str) -> str:
    completed = run_process(
        codex_command,
        ["--version"],
        input_text=None,
        timeout_seconds=30,
    )
    if completed.returncode != 0:
        fail("Codex CLI version command failed")
    version = completed.stdout.strip()
    if not version:
        fail("Codex CLI version command returned no output")
    return version


def invoke_codex(
    *,
    codex_command: str,
    model: str,
    reasoning_effort: str,
    prompt: str,
    timeout_seconds: int,
) -> list[dict[str, Any]]:
    with tempfile.TemporaryDirectory(prefix="context-compactor-benchmark-") as root:
        arguments = [
            "exec",
            "--json",
            "--ephemeral",
            "--skip-git-repo-check",
            "--ignore-user-config",
            "--ignore-rules",
            "--color",
            "never",
            "--sandbox",
            "read-only",
            "--model",
            model,
            "-c",
            f'model_reasoning_effort="{reasoning_effort}"',
            "-C",
            root,
            "-",
        ]
        try:
            completed = run_process(
                codex_command,
                arguments,
                input_text=prompt,
                timeout_seconds=timeout_seconds,
            )
        except subprocess.TimeoutExpired:
            fail(f"Codex CLI timed out after {timeout_seconds} seconds")
    if completed.returncode != 0:
        detail = (completed.stderr.strip() or completed.stdout.strip())[-2000:]
        if "os error 11001" in detail:
            raise RetryableTransportError(
                "Codex CLI could not resolve chatgpt.com (os error 11001)"
            )
        fail(
            f"Codex CLI failed with exit code {completed.returncode}: {detail}"
        )
    events: list[dict[str, Any]] = []
    for line_number, line in enumerate(completed.stdout.splitlines(), start=1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as exc:
            fail(f"decode Codex JSONL line {line_number}: {exc}")
        if not isinstance(event, dict):
            fail(f"Codex JSONL line {line_number} is not an object")
        events.append(event)
    if not events:
        fail("Codex CLI returned no JSONL events")
    return events


def run_process(
    command: str,
    arguments: list[str],
    *,
    input_text: str | None,
    timeout_seconds: int,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [command, *arguments],
        input=input_text,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=timeout_seconds,
        check=False,
    )


def extract_codex_result(
    events: list[dict[str, Any]],
) -> tuple[str, dict[str, Any] | None]:
    content = ""
    usage: dict[str, Any] | None = None
    for event in events:
        if event.get("type") == "item.completed":
            item = event.get("item")
            if isinstance(item, dict) and item.get("type") == "agent_message":
                text = item.get("text")
                if isinstance(text, str) and text.strip():
                    content = text.strip()
        if event.get("type") == "turn.completed":
            candidate = event.get("usage")
            if isinstance(candidate, dict):
                usage = candidate
    if not content:
        fail("Codex CLI returned no completed agent message")
    return content, usage


def integer_usage(usage: dict[str, Any], name: str) -> int:
    value = usage.get(name, 0)
    if isinstance(value, bool):
        return 0
    if isinstance(value, (int, float)) and value >= 0:
        return int(value)
    return 0


def tool_definition_digest(codex_version: str) -> str:
    surface = {
        "codex_version": codex_version,
        "sandbox": "read-only",
        "ephemeral": True,
        "ignore_user_config": True,
        "ignore_rules": True,
        "working_directory": "empty-temporary-directory",
        "tool_use_instruction": "forbidden",
    }
    encoded = json.dumps(
        surface,
        ensure_ascii=True,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def environment_value(name: str, default: str) -> str:
    value = os.environ.get(name, default).strip()
    if not value:
        fail(f"{name} must not be empty")
    return value


def parse_positive_int(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        fail(f"{name} must be a positive integer")
    if value <= 0:
        fail(f"{name} must be a positive integer")
    return value


def run_self_test() -> None:
    request = {
        "protocol": REQUEST_PROTOCOL,
        "matrix": "formal",
        "scenario": "resume",
        "seed": 1,
        "mode": "context_compactor_balanced",
        "turn_number": 10,
        "fixed": True,
        "rendered_input": '{"active_requirement":"resumable-work"}',
    }
    read_benchmark_request(json.dumps(request).encode("utf-8"))
    prompt = build_prompt(request)
    if "Do not call tools." not in prompt:
        fail("self-test failed: tool restriction is missing")
    run_full_transcript_prompt_self_test()
    summary_request = dict(request, mode="summary_only")
    summary_prompt = build_prompt(summary_request)
    if "copy current_progress as current focus exactly" not in summary_prompt:
        fail("self-test failed: summary current-focus rule is missing")
    expected_prompt_digest = hashlib.sha256(prompt.encode("utf-8")).hexdigest()
    if prompt_digest(request) != expected_prompt_digest:
        fail("self-test failed: prompt digest mismatch")
    content, usage = extract_codex_result(
        [
            {
                "type": "item.completed",
                "item": {
                    "type": "agent_message",
                    "text": "active requirement: resumable-work",
                },
            },
            {
                "type": "turn.completed",
                "usage": {
                    "input_tokens": 10,
                    "cached_input_tokens": 4,
                    "output_tokens": 2,
                },
            },
        ]
    )
    if content != "active requirement: resumable-work":
        fail("self-test failed: content extraction mismatch")
    if usage is None or integer_usage(usage, "cached_input_tokens") != 4:
        fail("self-test failed: usage extraction mismatch")
    state = BrokerState()
    pending = state.submit(b'{"protocol":"test"}')
    queued = state.next_request()
    if queued is not pending:
        fail("self-test failed: broker queue mismatch")
    if not state.complete(
        pending.identifier,
        b'{"content":"ok"}',
        "",
    ):
        fail("self-test failed: broker completion mismatch")
    if not pending.completed.is_set() or pending.output != b'{"content":"ok"}':
        fail("self-test failed: broker completion was not delivered")

    previous_cache = os.environ.get("CONTEXT_COMPACTOR_MODEL_CACHE")
    with tempfile.TemporaryDirectory() as cache_root:
        os.environ["CONTEXT_COMPACTOR_MODEL_CACHE"] = cache_root
        config = {
            "model": "test-model",
            "reasoning_effort": "high",
            "codex_version": "codex-cli test",
        }
        cached_output = {"content": "active requirement: resumable-work"}
        save_cached_output(request, config, cached_output)
        if load_cached_output(request, config) != cached_output:
            fail("self-test failed: response cache mismatch")
        cache_files = os.listdir(cache_root)
        if len(cache_files) != 1:
            fail("self-test failed: response cache file count mismatch")
        with open(
            os.path.join(cache_root, cache_files[0]),
            "r",
            encoding="utf-8",
        ) as handle:
            cached_text = handle.read()
        if "rendered_input" in cached_text:
            fail("self-test failed: response cache retained the request")
    if previous_cache is None:
        os.environ.pop("CONTEXT_COMPACTOR_MODEL_CACHE", None)
    else:
        os.environ["CONTEXT_COMPACTOR_MODEL_CACHE"] = previous_cache
    print("self-test ok")


def run_full_transcript_prompt_self_test() -> None:
    cases = [
        {
            "scenario": "continuous_development",
            "turns": [
                {
                    "user_input": "synthetic-establish-stable-requirement-01",
                    "agent_response": "synthetic-accept-stable-requirement-01",
                    "tool_activities": [
                        "synthetic-record-stable-requirement-01"
                    ],
                },
                {
                    "user_input": "synthetic-continue-stable-requirement-02",
                    "agent_response": "synthetic-progress-stable-requirement-02",
                    "tool_activities": [
                        "synthetic-edit-stable-requirement-02",
                        "synthetic-verify-stable-requirement-02",
                    ],
                },
            ],
            "focus": "synthetic-progress-stable-requirement-02",
            "next_action": "synthetic-verify-stable-requirement-02",
        },
        {
            "scenario": "requirement_reversal",
            "turns": [
                {
                    "user_input": "synthetic-establish-legacy-decision-01",
                    "agent_response": "synthetic-accept-legacy-decision-01",
                    "tool_activities": [
                        "synthetic-record-legacy-decision-01"
                    ],
                },
                {
                    "user_input": (
                        "synthetic-replace-legacy-with-current-decision-31"
                    ),
                    "agent_response": (
                        "synthetic-superseded-legacy-and-applied-"
                        "current-decision-31"
                    ),
                    "tool_activities": [
                        "synthetic-supersede-legacy-decision-31",
                        "synthetic-verify-current-decision-31",
                    ],
                },
                {
                    "user_input": "synthetic-continue-current-decision-32",
                    "agent_response": "synthetic-progress-current-decision-32",
                    "tool_activities": [
                        "synthetic-verify-current-decision-32"
                    ],
                },
            ],
            "focus": "synthetic-progress-current-decision-32",
            "next_action": "synthetic-verify-current-decision-32",
        },
        {
            "scenario": "resume",
            "turns": [
                {
                    "user_input": "synthetic-establish-resumable-work-01",
                    "agent_response": "synthetic-start-resumable-work-01",
                    "tool_activities": [
                        "synthetic-edit-resumable-work-01"
                    ],
                },
                {
                    "user_input": "synthetic-resume-request-31",
                    "agent_response": (
                        "synthetic-resume-preview-no-state-change-31"
                    ),
                    "tool_activities": [
                        "synthetic-read-checkpoint-31",
                        "synthetic-read-repository-state-31",
                    ],
                },
                {
                    "user_input": "synthetic-confirm-resume-32",
                    "agent_response": (
                        "synthetic-continue-after-confirmation-32"
                    ),
                    "tool_activities": [
                        "synthetic-edit-after-confirmation-32",
                        "synthetic-verify-after-confirmation-32",
                    ],
                },
            ],
            "focus": "synthetic-continue-after-confirmation-32",
            "next_action": "synthetic-verify-after-confirmation-32",
        },
    ]
    required_rules = (
        "scan every turn in chronological order",
        "track establish, replace, and superseded actions",
        "latest establish or replace action that has not been superseded",
        "resume transcripts require tracing the earlier establish action",
        "copy the final turn's agent_response exactly",
        "never use user_input and never summarize it",
        "copy the final item in the final turn's tool_activities exactly",
        "never infer an action",
    )
    for index, case in enumerate(cases, start=1):
        rendered_input = json.dumps(
            {"turns": case["turns"]},
            ensure_ascii=False,
            separators=(",", ":"),
        )
        request = {
            "protocol": REQUEST_PROTOCOL,
            "matrix": "formal",
            "scenario": case["scenario"],
            "seed": 1,
            "mode": "full_transcript",
            "turn_number": len(case["turns"]),
            "fixed": True,
            "rendered_input": rendered_input,
        }
        prompt = build_prompt(request)
        for rule in required_rules:
            if rule not in prompt:
                fail(
                    "self-test failed: transcript rule is missing "
                    f"for case {index}: {rule}"
                )
        if case["focus"] not in prompt:
            fail(
                "self-test failed: final agent response is missing "
                f"for case {index}"
            )
        if case["next_action"] not in prompt:
            fail(
                "self-test failed: final tool activity is missing "
                f"for case {index}"
            )


def fail(message: str) -> None:
    print("foreground_model_codex:", message, file=sys.stderr)
    raise SystemExit(1)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
