#!/usr/bin/env python3
"""OpenAI Responses API wrapper for context-compactor foreground benchmarks."""

from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.request


REQUEST_PROTOCOL = "context-compactor/foreground-model-check/v1"
DEFAULT_BASE_URL = "https://api.openai.com/v1"
DEFAULT_MODEL = "gpt-5"
DEFAULT_TIMEOUT_SECONDS = 120
DEFAULT_MAX_OUTPUT_TOKENS = 700


def main(argv: list[str]) -> int:
    if argv == ["--self-test"]:
        run_self_test()
        return 0
    if argv:
        fail("usage: foreground_model_openai.py [--self-test]")

    request = read_benchmark_request(sys.stdin.buffer.read())
    api_key = os.environ.get("OPENAI_API_KEY", "").strip()
    if not api_key:
        fail("OPENAI_API_KEY is required")

    model = os.environ.get("OPENAI_FOREGROUND_MODEL", DEFAULT_MODEL).strip()
    if not model:
        fail("OPENAI_FOREGROUND_MODEL must not be empty")

    response = create_response(
        api_key=api_key,
        model=model,
        request=request,
        base_url=os.environ.get("OPENAI_BASE_URL", DEFAULT_BASE_URL),
        timeout_seconds=parse_positive_int(
            "OPENAI_TIMEOUT_SECONDS",
            DEFAULT_TIMEOUT_SECONDS,
        ),
        max_output_tokens=parse_positive_int(
            "OPENAI_MAX_OUTPUT_TOKENS",
            DEFAULT_MAX_OUTPUT_TOKENS,
        ),
    )
    output = {
        "content": extract_output_text(response),
        "input_tokens": int(response.get("usage", {}).get("input_tokens", 0) or 0),
        "output_tokens": int(response.get("usage", {}).get("output_tokens", 0) or 0),
        "token_basis": "observed" if response.get("usage") else "not_evaluated",
        "model": str(response.get("model") or model),
    }
    json.dump(output, sys.stdout, ensure_ascii=False, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


def read_benchmark_request(raw: bytes) -> dict:
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


def create_response(
    *,
    api_key: str,
    model: str,
    request: dict,
    base_url: str,
    timeout_seconds: int,
    max_output_tokens: int,
) -> dict:
    payload = {
        "model": model,
        "input": build_prompt(request),
        "max_output_tokens": max_output_tokens,
        "store": False,
    }
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    http_request = urllib.request.Request(
        url=base_url.rstrip("/") + "/responses",
        data=body,
        headers={
            "Authorization": "Bearer " + api_key,
            "Content-Type": "application/json",
        },
        method="POST",
    )
    organization = os.environ.get("OPENAI_ORG_ID", "").strip()
    if organization:
        http_request.add_header("OpenAI-Organization", organization)
    project = os.environ.get("OPENAI_PROJECT", "").strip()
    if project:
        http_request.add_header("OpenAI-Project", project)

    try:
        with urllib.request.urlopen(http_request, timeout=timeout_seconds) as response:
            response_body = response.read()
    except urllib.error.HTTPError as exc:
        detail = exc.read(2000).decode("utf-8", "replace")
        fail(f"OpenAI API HTTP {exc.code}: {detail}")
    except urllib.error.URLError as exc:
        fail(f"OpenAI API request failed: {exc.reason}")
    except TimeoutError:
        fail("OpenAI API request timed out")

    try:
        decoded = json.loads(response_body)
    except json.JSONDecodeError as exc:
        fail(f"decode OpenAI API response: {exc}")
    if not isinstance(decoded, dict):
        fail("OpenAI API response must be a JSON object")
    if decoded.get("error"):
        fail("OpenAI API response contained an error")
    return decoded


def build_prompt(request: dict) -> str:
    metadata = {
        "matrix": request.get("matrix"),
        "scenario": request.get("scenario"),
        "seed": request.get("seed"),
        "mode": request.get("mode"),
        "turn_number": request.get("turn_number"),
        "fixed": request.get("fixed"),
        "event_reasons": request.get("event_reasons", []),
    }
    return "\n".join(
        [
            "You are evaluating one context-compactor benchmark checkpoint.",
            "Use only rendered_input as evidence for the answer.",
            "Metadata identifies the benchmark cell but is not evidence for the answer.",
            "Return concise plain text with exactly these labels:",
            "active requirement: <value or unknown>",
            "current focus: <value or unknown>",
            "next action: <value or unknown>",
            "superseded requirement: <value marked superseded/inactive, or unknown>",
            "unknown fields: <comma-separated labels or none>",
            "",
            "metadata:",
            json.dumps(metadata, ensure_ascii=False, separators=(",", ":")),
            "",
            "rendered_input:",
            str(request["rendered_input"]),
        ]
    )


def extract_output_text(response: dict) -> str:
    output_text = response.get("output_text")
    if isinstance(output_text, str) and output_text.strip():
        return output_text.strip()

    parts: list[str] = []
    for item in response.get("output", []) or []:
        if not isinstance(item, dict):
            continue
        for content in item.get("content", []) or []:
            if not isinstance(content, dict):
                continue
            text = content.get("text")
            if isinstance(text, str):
                parts.append(text)
    text = "\n".join(part.strip() for part in parts if part.strip()).strip()
    if not text:
        fail("OpenAI API response did not contain output text")
    return text


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
        "rendered_input": "active requirement: resumable-work",
    }
    read_benchmark_request(json.dumps(request).encode("utf-8"))
    prompt = build_prompt(request)
    if "OPENAI_API_KEY" in prompt:
        fail("self-test failed: prompt contains a secret environment name")
    sample = {
        "model": "test-model",
        "output": [
            {
                "content": [
                    {
                        "type": "output_text",
                        "text": "active requirement: resumable-work",
                    }
                ]
            }
        ],
        "usage": {"input_tokens": 1, "output_tokens": 2},
    }
    if extract_output_text(sample) != "active requirement: resumable-work":
        fail("self-test failed: output extraction mismatch")
    print("self-test ok")


def fail(message: str) -> None:
    print("foreground_model_openai:", message, file=sys.stderr)
    raise SystemExit(1)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
