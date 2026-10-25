#!/usr/bin/env python3
"""Offline check of validate_judge_env. Run: python3 -I scripts/test_env_config.py"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from env_config import BenchConfig, validate_judge_env  # noqa: E402


def config(judge_api_key: str) -> BenchConfig:
    return BenchConfig(
        metronix_mcp_api_key="", metronix_mcp_url="", metronix_api_url="", workspace_id="",
        chat_api_key="", chat_base_url="https://api.openai.com/v1", chat_model="gpt-4o",
        judge_api_key=judge_api_key, judge_base_url="https://api.openai.com/v1", judge_model="gpt-4o",
        retrieve_top_k=5,
    )


class ValidateJudgeEnvTest(unittest.TestCase):
    def test_bad_key_is_reported_without_its_value(self):
        value = "not-a-key-but-maybe-a-secret-1234"
        errors = validate_judge_env(config(value))
        self.assertEqual(len(errors), 1)
        self.assertIn("does not look like an API key", errors[0])
        self.assertIn(f"{len(value)}-char", errors[0])
        self.assertNotIn(value, errors[0])
        self.assertNotIn("1234", errors[0])

    def test_well_formed_key_passes(self):
        self.assertEqual(validate_judge_env(config("sk-test")), [])  # pragma: allowlist secret


if __name__ == "__main__":
    unittest.main()
