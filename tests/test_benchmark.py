from __future__ import annotations

import json
import os
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

from context_compactor.benchmark import (
    CHECKPOINT_QUESTION,
    ENDURANCE_TURNS,
    SEEDS,
    STAGE1_CHECKPOINTS,
    BenchmarkError,
    ForegroundInvocation,
    build_scenario,
    estimate_input_tokens,
    expected_facts,
    parse_codex_jsonl,
    render_markdown_report,
    run_stage1_benchmark,
    serialize_arm_input,
    state_for_turn,
    validate_report,
)


class FakeForegroundClient:
    codex_version = "codex-cli test"
    model = "synthetic-test-model"
    reasoning_effort = "deterministic"

    def probe(self) -> ForegroundInvocation:
        return ForegroundInvocation(
            value={"probe": True},
            usage={
                "input_tokens": 10,
                "cached_input_tokens": 0,
                "output_tokens": 1,
                "reasoning_output_tokens": 0,
                "total_tokens": 11,
            },
            elapsed_ms=1.0,
        )

    def invoke(self, prompt: str) -> ForegroundInvocation:
        seed, checkpoint = _identity_from_prompt(prompt)
        fixture = build_scenario(seed, checkpoint)
        value = expected_facts(fixture, checkpoint).as_mapping()
        input_tokens = estimate_input_tokens(prompt)
        return ForegroundInvocation(
            value=value,
            usage={
                "input_tokens": input_tokens,
                "cached_input_tokens": 0,
                "output_tokens": 20,
                "reasoning_output_tokens": 0,
                "total_tokens": input_tokens + 20,
            },
            elapsed_ms=2.0,
            stdout=json.dumps(value, separators=(",", ":")),
        )


def _identity_from_prompt(prompt: str) -> tuple[int, int]:
    for seed in SEEDS:
        marker = f"goal-private-bounded-memory-{seed:04x}"
        if marker in prompt:
            for checkpoint in reversed(STAGE1_CHECKPOINTS):
                if f"TURN {checkpoint:02d} " in prompt:
                    return seed, checkpoint
    raise AssertionError("fake foreground input identity is missing")


class BenchmarkTests(unittest.TestCase):
    def test_v3_fixture_is_deterministic_and_one_combined_scenario(self) -> None:
        first = build_scenario(SEEDS[0])
        second = build_scenario(SEEDS[0])
        other = build_scenario(SEEDS[1])

        self.assertEqual(first, second)
        self.assertEqual(len(first.turns), ENDURANCE_TURNS)
        self.assertNotEqual(first.project_code, other.project_code)
        self.assertEqual(
            [turn.outcome for turn in first.turns],
            [turn.outcome for turn in other.turns],
        )
        self.assertEqual(first.turns[20].session_id, first.turns[21].session_id)
        self.assertNotEqual(first.turns[19].session_id, first.turns[20].session_id)

    def test_a_b_inputs_share_question_and_meet_estimated_reductions(self) -> None:
        for seed in SEEDS:
            fixture = build_scenario(seed, 30)
            for checkpoint in STAGE1_CHECKPOINTS:
                state = state_for_turn(fixture, checkpoint)
                a_input = serialize_arm_input(
                    fixture,
                    checkpoint,
                    state,
                    "A",
                )
                b_input = serialize_arm_input(
                    fixture,
                    checkpoint,
                    state,
                    "B",
                )
                a_tokens = estimate_input_tokens(a_input)
                b_tokens = estimate_input_tokens(b_input)
                reduction = (a_tokens - b_tokens) / a_tokens * 100

                self.assertIn(CHECKPOINT_QUESTION.rstrip(), a_input)
                self.assertIn(CHECKPOINT_QUESTION.rstrip(), b_input)
                self.assertIn("TURN 01 ", a_input)
                self.assertNotIn("TURN 01 ", b_input)
                self.assertGreaterEqual(
                    reduction,
                    {10: 30.0, 20: 50.0, 30: 60.0}[checkpoint],
                )

    def test_state_facts_appear_only_after_their_scenario_turn(self) -> None:
        fixture = build_scenario(SEEDS[0], 11)

        first = state_for_turn(fixture, 1)
        third = state_for_turn(fixture, 3)
        reversed_state = state_for_turn(fixture, 11)

        self.assertEqual(first.constraints, ())
        self.assertEqual(first.decisions, ())
        self.assertEqual(first.recent_verification, ())
        self.assertEqual(first.next_actions, ())
        self.assertIn("require-local-yaml", third.decisions[-1].statement)
        self.assertTrue(
            any(
                "superseded-requirement:require-local-yaml"
                in entry.statement
                for entry in reversed_state.decisions
            )
        )
        self.assertTrue(
            any(
                "active-requirement:require-metadata-only-status"
                in entry.statement
                for entry in reversed_state.decisions
            )
        )

    @unittest.skipUnless(
        os.environ.get("CONTEXT_COMPACTOR_RUN_BENCHMARK_TESTS") == "1",
        "formal 90-turn benchmark matrix is opt-in",
    )
    def test_stage1_fake_run_passes_schema_privacy_and_all_gates(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess_repository(root)
            report = run_stage1_benchmark(
                root,
                FakeForegroundClient(),
                started_at=datetime(2026, 7, 31, tzinfo=timezone.utc),
            )

        validate_report(report)
        markdown = render_markdown_report(report)
        self.assertEqual(report["status"], "pass")
        self.assertEqual(len(report["calls"]), 18)
        self.assertEqual(report["privacy"]["defined_secret_matches"], 0)
        self.assertTrue(
            report["aggregate"]["primary_tokens"][
                "all_reduction_gates_passed"
            ]
        )
        self.assertTrue(all(report["aggregate"]["quality_gates"].values()))
        self.assertNotIn("BENCHMARK_PASSWORD", json.dumps(report))
        self.assertNotIn("BENCHMARK_PASSWORD", markdown)
        invalid = json.loads(json.dumps(report))
        invalid["calls"][0]["prompt"] = "forbidden"
        with self.assertRaisesRegex(BenchmarkError, "forbidden field"):
            validate_report(invalid)

    def test_codex_jsonl_usage_is_observed_only_when_input_exists(self) -> None:
        content = {
            name: value
            for name, value in expected_facts(build_scenario(17, 10), 10)
            .as_mapping()
            .items()
        }
        stream = "\n".join(
            (
                json.dumps({"type": "thread.started", "thread_id": "synthetic"}),
                json.dumps(
                    {
                        "type": "item.completed",
                        "item": {
                            "type": "agent_message",
                            "text": json.dumps(content),
                        },
                    }
                ),
                json.dumps(
                    {
                        "type": "turn.completed",
                        "usage": {
                            "input_tokens": 100,
                            "cached_input_tokens": 40,
                            "output_tokens": 10,
                            "reasoning_output_tokens": 2,
                        },
                    }
                ),
            )
        )

        decoded, usage = parse_codex_jsonl(stream)

        self.assertEqual(decoded, content)
        self.assertIsNotNone(usage)
        self.assertEqual(usage["input_tokens"], 100)
        self.assertEqual(usage["total_tokens"], 110)
        with self.assertRaises(BenchmarkError):
            parse_codex_jsonl('{"type":"turn.completed"}')

def subprocess_repository(root: Path) -> None:
    import subprocess

    subprocess.run(
        ("git", "init", "-q"),
        cwd=str(root),
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    (root / "tracked.txt").write_text("fixture\n", encoding="utf-8")
    subprocess.run(
        ("git", "add", "tracked.txt"),
        cwd=str(root),
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    subprocess.run(
        (
            "git",
            "-c",
            "user.name=Benchmark Test",
            "-c",
            "user.email=benchmark@example.invalid",
            "commit",
            "-qm",
            "fixture",
        ),
        cwd=str(root),
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


if __name__ == "__main__":
    unittest.main()
