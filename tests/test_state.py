from __future__ import annotations

import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from unittest import mock

from context_compactor.state import (
    ProjectState,
    StateEntry,
    StateFormatError,
    StateMetadata,
    StateTooLargeError,
    StateValidationError,
    decode_state,
    enforce_token_budget,
    estimate_input_tokens,
    fit_state_to_budget,
    load_state,
    load_state_file,
    publish_state,
    serialize_state,
)


class StateTests(unittest.TestCase):
    def test_empty_state_round_trip(self) -> None:
        state = ProjectState.empty()

        self.assertEqual(decode_state(serialize_state(state)), state)

    def test_valid_state_round_trip_preserves_unicode_and_sources(self) -> None:
        state = ProjectState(
            project_summary="本機優先的 context 工具",
            current_focus="實作 Task 1",
            goals=(StateEntry("完成 readable state", source="SPEC.md"),),
            constraints=(
                StateEntry(
                    "不得保存原始 prompt",
                    source_event_id="event-synthetic-001",
                ),
            ),
            metadata=StateMetadata(
                source_cursor=7,
                updated_at="2026-07-31T12:00:00+08:00",
            ),
        )

        self.assertEqual(decode_state(serialize_state(state)), state)

    def test_unknown_fields_are_rejected_at_every_schema_level(self) -> None:
        valid = serialize_state(ProjectState.empty())
        cases = (
            valid + "unexpected: \"value\"\n",
            valid.replace(
                "goals: []",
                'goals:\n  - statement: "goal"\n    unexpected: "value"',
            ),
            valid.replace(
                '  updated_at: ""',
                '  updated_at: ""\n  unexpected: "value"',
            ),
        )

        for text in cases:
            with self.subTest(text=text):
                with self.assertRaises(StateValidationError):
                    decode_state(text)

    def test_malformed_yaml_is_rejected(self) -> None:
        cases = (
            "",
            "schema_version 1\n",
            " schema_version: 1\n",
            'schema_version: "unterminated\n',
        )

        for text in cases:
            with self.subTest(text=text):
                with self.assertRaises(StateFormatError):
                    decode_state(text)

    def test_oversize_state_is_rejected(self) -> None:
        state = ProjectState(project_summary="x" * 500)

        with self.assertRaises(StateTooLargeError):
            enforce_token_budget(state, token_budget=100)

    def test_budget_fit_preserves_goals_constraints_and_focus(self) -> None:
        mandatory = ProjectState(
            current_focus="Task 1",
            goals=(StateEntry("ship readable state"),),
            constraints=(StateEntry("never persist raw prompts"),),
        )
        budget = estimate_input_tokens(serialize_state(mandatory))
        state = replace(
            mandatory,
            project_summary="summary " * 200,
            open_questions=(StateEntry("question " * 100),),
            recent_verification=(StateEntry("verification " * 100),),
        )

        fitted = fit_state_to_budget(state, token_budget=budget)

        self.assertEqual(fitted.current_focus, mandatory.current_focus)
        self.assertEqual(fitted.goals, mandatory.goals)
        self.assertEqual(fitted.constraints, mandatory.constraints)
        self.assertLessEqual(
            estimate_input_tokens(serialize_state(fitted)),
            budget,
        )

    def test_publish_is_atomic_and_keeps_previous_valid_backup(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = ProjectState(current_focus="first")
            second = ProjectState(current_focus="second")

            state_path = publish_state(root, first)
            publish_state(root, second)

            self.assertEqual(load_state(root), second)
            self.assertEqual(
                load_state_file(state_path.with_name("state.backup.yaml")),
                first,
            )

    def test_invalid_candidate_leaves_last_valid_state_unchanged(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = ProjectState(current_focus="first")
            state_path = publish_state(root, first)
            before = state_path.read_bytes()
            invalid = ProjectState(
                goals=(StateEntry("invalid source", source="../outside"),),
            )

            with self.assertRaises(StateValidationError):
                publish_state(root, invalid)

            self.assertEqual(state_path.read_bytes(), before)
            self.assertEqual(load_state(root), first)

    def test_replace_failure_rolls_back_to_last_valid_state(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = ProjectState(current_focus="first")
            second = ProjectState(current_focus="second")
            state_path = publish_state(root, first)
            before = state_path.read_bytes()
            real_replace = __import__("os").replace

            def fail_state_replace(source: object, destination: object) -> None:
                if Path(destination) == state_path:
                    raise OSError("synthetic replace failure")
                real_replace(source, destination)

            with mock.patch(
                "context_compactor.state.os.replace",
                side_effect=fail_state_replace,
            ):
                with self.assertRaises(OSError):
                    publish_state(root, second)

            self.assertEqual(state_path.read_bytes(), before)
            self.assertEqual(load_state(root), first)


if __name__ == "__main__":
    unittest.main()
