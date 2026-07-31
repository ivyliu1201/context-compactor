from __future__ import annotations

import unittest

from context_compactor.privacy import (
    MAX_RETAINED_CHARACTERS,
    REDACTION_MARKER,
    PrivacyFilterError,
    contains_known_secret,
    sanitize_prompt,
)


class PrivacyFilterTests(unittest.TestCase):
    def test_defined_secret_patterns_are_redacted(self) -> None:
        api_key = "sk-" + "synthetic-" + "value-001"
        bearer = "bearer-" + "synthetic-" + "value-002"
        basic = "c3ludGhldGlj" + "OnZhbHVlLTAwMw=="
        password = "pass-" + "synthetic-" + "value-004"
        private_body = "SYNTHETIC" + "PRIVATE" + "BODY"
        github_token = "ghp_" + ("A" * 24)
        aws_access_key = "AKIA" + ("B" * 16)
        cases = (
            f"api_key={api_key}",
            f"Authorization: Bearer {bearer}",
            f"Authorization: Basic {basic}",
            f"use Bearer {bearer}",
            f'password="{password}"',
            "\n".join(
                (
                    "-----BEGIN PRIVATE KEY-----",
                    private_body,
                    "-----END PRIVATE KEY-----",
                )
            ),
            github_token,
            aws_access_key,
        )

        for original in cases:
            with self.subTest(original=original.splitlines()[0]):
                result = sanitize_prompt(original)
                self.assertIn(REDACTION_MARKER, result.text)
                self.assertNotIn(
                    original
                    if "\n" not in original
                    else private_body,
                    result.text,
                )
                self.assertGreaterEqual(result.redaction_count, 1)
                self.assertTrue(contains_known_secret(original))

    def test_default_and_custom_high_risk_environment_names_are_redacted(self) -> None:
        default_value = "openai-" + "synthetic-" + "value"
        custom_value = "custom-" + "synthetic-" + "value"

        default_result = sanitize_prompt(f"OPENAI_API_KEY={default_value}")
        custom_result = sanitize_prompt(
            f"MY_DEPLOY_CREDENTIAL={custom_value}",
            high_risk_environment_names={"MY_DEPLOY_CREDENTIAL"},
        )

        self.assertEqual(
            default_result.text,
            f"OPENAI_API_KEY={REDACTION_MARKER}",
        )
        self.assertEqual(
            custom_result.text,
            f"MY_DEPLOY_CREDENTIAL={REDACTION_MARKER}",
        )

    def test_unicode_character_boundary_is_exact(self) -> None:
        exact = ("a" * (MAX_RETAINED_CHARACTERS - 1)) + "界"
        oversized = exact + "🙂"

        exact_result = sanitize_prompt(exact)
        oversized_result = sanitize_prompt(oversized)

        self.assertEqual(len(exact_result.text), MAX_RETAINED_CHARACTERS)
        self.assertFalse(exact_result.truncated)
        self.assertEqual(len(oversized_result.text), MAX_RETAINED_CHARACTERS)
        self.assertTrue(oversized_result.truncated)
        self.assertTrue(oversized_result.text.endswith("界"))

    def test_bound_never_splits_redaction_marker(self) -> None:
        secret = "sk-" + ("C" * 20)
        prefix = ("x" * (MAX_RETAINED_CHARACTERS - 6)) + " "

        result = sanitize_prompt(prefix + secret)

        self.assertTrue(result.truncated)
        self.assertEqual(result.redaction_count, 1)
        self.assertEqual(len(result.text), MAX_RETAINED_CHARACTERS - 5)
        self.assertNotIn(secret, result.text)

    def test_ordinary_project_instruction_is_unchanged(self) -> None:
        instruction = "Keep state.yaml readable and finish Task 2."

        result = sanitize_prompt(instruction)

        self.assertEqual(result.text, instruction)
        self.assertEqual(result.redaction_count, 0)
        self.assertFalse(result.truncated)
        self.assertFalse(contains_known_secret(instruction))

    def test_redacted_output_is_safe_to_check_again(self) -> None:
        synthetic = "secret-" + "synthetic-" + "value"
        original = (
            f"Authorization: Bearer {synthetic}; "
            f"password={synthetic}"
        )

        first = sanitize_prompt(original)
        second = sanitize_prompt(first.text)

        self.assertEqual(second.text, first.text)
        self.assertFalse(contains_known_secret(first.text))
        self.assertTrue(
            contains_known_secret(
                first.text + " api_key=another-synthetic-secret"
            )
        )

    def test_unsafe_or_policy_weakening_input_is_rejected(self) -> None:
        cases = (
            {"text": "before\x00after"},
            {"text": "before\ud800after"},
            {
                "text": "ordinary",
                "max_characters": MAX_RETAINED_CHARACTERS + 1,
            },
            {
                "text": "ordinary",
                "high_risk_environment_names": {"INVALID-NAME"},
            },
        )

        for arguments in cases:
            with self.subTest(arguments=repr(arguments)):
                with self.assertRaises(PrivacyFilterError):
                    sanitize_prompt(**arguments)


if __name__ == "__main__":
    unittest.main()
