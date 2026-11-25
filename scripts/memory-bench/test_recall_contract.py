"""Pure contract/collector checks; contract_proof.py tests the real pair in CI."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import mesh_client_stdio as client
from checkout_mcp import archive_url, NoRedirect


class RecallContract(unittest.TestCase):
    def test_server_rank_diagnostics_do_not_replace_score_or_identity(self):
        record = client._to_record({
            "id": "gold", "key": "s0", "content": "text", "score": 0.001,
            "dense_score": 0.9, "recency_score": 0.015625, "created_at": "2023-01-01",
        })
        self.assertEqual(record["score"], 0.001)
        self.assertEqual(record["dense_score"], 0.9)
        self.assertEqual(record["recency_score"], 0.015625)
        self.assertEqual(record["created_at"], "2023-01-01")
        self.assertEqual(record["id"], "gold")

    def test_real_sdk_wire_models_preserve_schema_and_error_flags(self):
        # The regular offline selfchecks need no SDK. CI repeats this file after
        # installing the pinned SDK, and local proof uses that same environment.
        try:
            from mcp.types import Tool, CallToolResult
        except ImportError:
            self.skipTest("run again with the pinned MCP SDK installed")
        schema = {"type": "object", "properties": {"limit": {"type": "integer"}}}
        tool = Tool.model_validate({"name": "recall", "inputSchema": schema})
        self.assertEqual(client._mcp_input_schema(tool), schema)
        for text in ('{"items": []}', "InvalidParams: limit", ""):
            with self.subTest(text=text):
                failure = CallToolResult.model_validate({
                    "isError": True, "content": [{"type": "text", "text": text}],
                })
                self.assertTrue(client._parse_tool_payload(failure)["error"])
        success = CallToolResult.model_validate({
            "isError": False, "content": [{"type": "text", "text": '{"items": []}'}],
        })
        self.assertEqual(client._parse_tool_payload(success), {"items": []})

    def test_archive_token_is_restricted_to_ci_https_origin_without_redirects(self):
        api, server = "https://git.test/api/v4", "https://git.test"
        url = archive_url(api, server, "entire-vc/evc-mesh-mcp", "a" * 40)
        self.assertEqual(url, f"{api}/projects/entire-vc%2Fevc-mesh-mcp/repository/archive.tar.gz?sha={'a' * 40}")
        for invalid in ("file:///api/v4", "http://git.test/api/v4", "https://other.test/api/v4",
                        "https://user:password@git.test/api/v4", f"{api}?query=1", f"{api}#fragment"):
            with self.subTest(invalid=invalid), self.assertRaisesRegex(ValueError, "HTTPS API origin"):
                archive_url(invalid, server, "project", "a" * 40)
        with self.assertRaisesRegex(ValueError, "redirect refused"):
            NoRedirect().redirect_request(None, None, 302, "redirect", {}, "https://other.test")

    def test_each_page_is_legal_and_overlap_covers_graph_reserve(self):
        self.assertLessEqual(client.RECALL_CANDIDATE_LIMIT, 50)
        self.assertGreater(client.RECALL_CANDIDATE_LIMIT, 0)
        self.assertEqual(client.RECALL_PAGE_OFFSETS[0], 0)
        for offset in client.RECALL_PAGE_OFFSETS:
            args = client.recall_arguments("query", "tag", False, offset)
            self.assertEqual(args["offset"], offset)
            self.assertIs(args["apply_recency_decay"], False)
            self.assertIs(args["full"], True)
            self.assertLessEqual(offset, 150)
        for previous, following in zip(client.RECALL_PAGE_OFFSETS, client.RECALL_PAGE_OFFSETS[1:]):
            self.assertLessEqual(following - previous, 50 - 50 // 4)
        self.assertGreaterEqual(client.RECALL_EFFECTIVE_WINDOW, 54 + 4)
        self.assertNotIn("apply_recency_decay", client.recall_arguments("query", "tag"))
        self.assertTrue(client.recall_arguments("query", "tag", True)["apply_recency_decay"])

    def test_dedup_keeps_first_rank_and_does_not_promote_later_gold(self):
        def row(i):
            return {"id": str(i), "key": f"s{i}", "tags": ["tag"], "score": 1}

        ranked, seen = [], set()
        first = [row(i) for i in range(50)]
        client.append_unique(ranked, seen, first, "tag")
        original_top10 = ranked[:10]
        client.append_unique(ranked, seen, [row(i) for i in range(38, 54)], "tag")
        client.append_unique(ranked, seen, [row(i) for i in range(54)], "tag")
        self.assertEqual(len(ranked), 54)
        self.assertEqual(ranked[:10], original_top10)
        self.assertEqual([r["id"] for r in ranked], [str(i) for i in range(54)])
        self.assertEqual(next(i for i, r in enumerate(ranked, 1) if r["id"] == "53"), 54)
        client.append_unique(ranked, seen, [{"id": "foreign", "tags": ["other"]}], "tag")
        self.assertEqual(len(ranked), 54)
        with self.assertRaisesRegex(ValueError, "no memory ID"):
            client.append_unique(ranked, seen, [{"tags": ["tag"]}], "tag")

    def test_unsupported_option_is_rejected_before_a_request(self):
        old = os.environ.get("BENCH_RECENCY_WEIGHT")
        try:
            os.environ["BENCH_RECENCY_WEIGHT"] = "0.1"
            with self.assertRaisesRegex(ValueError, "recency_weight"):
                client.recall_arguments("query", "tag")
            os.environ["BENCH_RECENCY_WEIGHT"] = "0"
            self.assertNotIn("recency_weight", client.recall_arguments("query", "tag"))
        finally:
            if old is None:
                os.environ.pop("BENCH_RECENCY_WEIGHT", None)
            else:
                os.environ["BENCH_RECENCY_WEIGHT"] = old

    def test_ci_pins_canon_and_runs_real_proof_before_controls_and_corpus(self):
        directory = Path(__file__).resolve().parent
        pin = json.loads(directory.joinpath("mcp-source.json").read_text())
        self.assertRegex(pin["ref"], r"^[0-9a-f]{40}$")
        self.assertEqual(pin["project_path"], "entire-vc/evc-mesh-mcp")
        ci = directory.parents[1].joinpath(".gitlab/ci/memory.yml").read_text().split("recall-gate-branch:\n", 1)[1].split("\nlongmemeval-audit-replay:", 1)[0]
        self.assertNotIn("git clone --depth 1 https://github.com/entire-vc/evc-mesh-mcp", ci)
        self.assertIn("checkout_mcp.py /tmp/evc-mesh-mcp", ci)
        self.assertIn('export MCP_COMMIT="$MCP_SOURCE_REF"', ci)
        self.assertLess(ci.index("python3 recall_preflight.py"), ci.index("python3 contract_proof.py"))
        self.assertLess(ci.index("python3 contract_proof.py"), ci.index("python3 dense_arm_control.py --expect alive"))


class BranchVerdicts(unittest.TestCase):
    """Execute the production CI shell; substitute only child CLI processes."""

    def test_redproof_requires_a_valid_result_and_writable_log(self):
        directory = Path(__file__).resolve().parent
        ci = directory.parents[1].joinpath(".gitlab/ci/memory.yml").read_text()
        start = ci.index('      echo "## The gate must still be able to go RED')
        end = ci.index('      cd "$CI_PROJECT_DIR"', start)
        production = "\n".join(line[6:] for line in ci[start:end].splitlines())
        for status, broken_log, expected in ((0, False, 0), (1, False, 1),
                                              (2, False, 2), (9, False, 9),
                                              (0, True, 74)):
            with self.subTest(status=status, broken_log=broken_log), tempfile.TemporaryDirectory() as tmp:
                if broken_log:
                    Path(tmp, "redproof.log").mkdir()
                # External child CLI boundary: execute the production wrapper.
                prefix = ('set -euo pipefail\n'
                          'python3() { echo "redproof child"; return "$TEST_REDPROOF_RC"; }\n')
                result = subprocess.run(
                    ["bash", "-c", prefix + production], cwd=tmp,
                    env={**os.environ, "TEST_REDPROOF_RC": str(status)},
                    text=True, capture_output=True, timeout=10,
                )
                self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
                if expected:
                    self.assertNotIn("redproof PASS", result.stdout)

    def run_branch(self, overrides=None, broken_log=False):
        directory = Path(__file__).resolve().parent
        ci = directory.parents[1].joinpath(".gitlab/ci/memory.yml").read_text()
        start = ci.index('      echo "## Recall harness contract proof')
        before_p95 = ci.index('      # `git -C`', start)
        power = ci.index('      echo "## Power check', before_p95)
        logs = ci.index('      echo "## Server logs', power)
        production = "\n".join(line[6:] for line in
                               (ci[start:before_p95] + ci[power:logs]).splitlines())
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bench = root / "scripts/memory-bench"
            bench.mkdir(parents=True)
            shutil.copyfile(directory / "collect_verdicts.sh", bench / "collect_verdicts.sh")
            if broken_log:
                # tee cannot replace a directory with its requested log file.
                (bench / (broken_log if isinstance(broken_log, str)
                          else "control_recency_visible.log")).mkdir()
            binary = root / "bin"
            binary.mkdir()
            python = binary / "python3"
            python.write_text('''#!/usr/bin/env bash
echo "RAN: $* nonce=${BENCH_RUN_NONCE-unset} backdate=${BENCH_BACKDATE_DSN+set} age=${BENCH_FIXTURE_AGE_MODE-unset}"
case "$*" in
  recall_preflight.py) exit "${TEST_PREFLIGHT_RC:-0}" ;;
  contract_proof.py) exit "${TEST_PROOF_RC:-0}" ;;
  *"--expect visible") exit "${TEST_VISIBLE_RC:-0}" ;;
  *"--expect blind") exit "${TEST_BLIND_RC:-0}" ;;
  *"--expect alive") exit "${TEST_ALIVE_RC:-0}" ;;
  *"--expect dead") exit "${TEST_DEAD_RC:-0}" ;;
  run_ci.py*) echo "GATE_REASON: measured"; exit "${TEST_GATE_RC:-0}" ;;
esac
exit 99
''')
            python.chmod(0o755)
            # External process/HTTP boundaries only; no real server is killed.
            prefix = '''set -euo pipefail
kill() { return 0; }
curl() { return 1; }
export CI_PROJECT_DIR="$PWD" CI_PIPELINE_ID=test BENCH_BACKDATE_DSN=test
echo unused > scripts/memory-bench/embedder.pid
'''
            return subprocess.run(
                ["bash", "-c", prefix + production + "\nfinish_bench_checks\n"],
                cwd=root, env={**os.environ, "PATH": f"{binary}:{os.environ['PATH']}",
                               **(overrides or {})},
                text=True, capture_output=True, timeout=10,
            )

    def test_visible_regression_runs_blind_gate_and_dead_once_and_stays_blocking(self):
        result = self.run_branch({"TEST_VISIBLE_RC": "1", "TEST_GATE_RC": "2"})
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("BENCH CHECK recency-visible: REGRESSION rc=1", result.stdout)
        self.assertIn("BENCH SUMMARY: REGRESSION job_rc=1", result.stdout)
        for command in ("recall_preflight.py", "contract_proof.py",
                        "dense_arm_control.py --expect alive", "recency_control.py --expect visible",
                        "recency_control.py --expect blind", "run_ci.py --retrieval-only",
                        "dense_arm_control.py --expect dead"):
            self.assertEqual(result.stdout.count(f"RAN: {command}"), 1, result.stdout)
        self.assertIn("--expect blind nonce=test-age-neg backdate= age=question-anchored\n", result.stdout)
        self.assertIn("--tolerance 0.25 nonce=test-gate backdate=set", result.stdout)

    def test_dense_controls_do_not_inherit_question_anchored_age(self):
        result = self.run_branch({"BENCH_FIXTURE_AGE_MODE": "question-anchored"})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for expectation, nonce in (("alive", "ctl"), ("dead", "pwr")):
            self.assertIn(
                f"--expect {expectation} nonce=test-{nonce} backdate=set age=ingest-now\n",
                result.stdout,
            )
        self.assertIn("--tolerance 0.25 nonce=test-gate backdate=set age=question-anchored", result.stdout)
        self.assertIn("--expect visible nonce=test-age backdate=set age=question-anchored", result.stdout)
        self.assertIn("--expect blind nonce=test-age-neg backdate= age=question-anchored", result.stdout)

    def test_all_controls_pass(self):
        result = self.run_branch()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("BENCH SUMMARY: PASS job_rc=0", result.stdout)

    def test_each_inconclusive_measurement_blocks_after_remaining_checks(self):
        for variable in ("TEST_ALIVE_RC", "TEST_VISIBLE_RC", "TEST_BLIND_RC",
                         "TEST_GATE_RC", "TEST_DEAD_RC"):
            with self.subTest(variable=variable):
                result = self.run_branch({variable: "2"})
                self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                self.assertIn("BENCH SUMMARY: INCONCLUSIVE job_rc=2", result.stdout)
                self.assertIn("RAN: run_ci.py --retrieval-only", result.stdout)
                self.assertIn("RAN: dense_arm_control.py --expect dead", result.stdout)
                self.assertNotIn("BENCH SUMMARY: PASS", result.stdout)

    def test_regression_remains_distinct_from_inconclusive_in_either_order(self):
        for overrides in ({"TEST_ALIVE_RC": "2", "TEST_DEAD_RC": "1"},
                          {"TEST_ALIVE_RC": "1", "TEST_DEAD_RC": "2"}):
            with self.subTest(overrides=overrides):
                result = self.run_branch(overrides)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("BENCH SUMMARY: REGRESSION job_rc=1", result.stdout)

    def test_unknown_exit_and_failed_log_write_block(self):
        for overrides, broken_log, status in (({"TEST_BLIND_RC": "9"}, False, 9),
                                              ({"TEST_ALIVE_RC": "2", "TEST_BLIND_RC": "9"}, False, 9),
                                              ({"TEST_ALIVE_RC": "9", "TEST_BLIND_RC": "2"}, False, 9),
                                              ({}, True, 74)):
            with self.subTest(overrides=overrides, broken_log=broken_log):
                result = self.run_branch(overrides, broken_log)
                self.assertEqual(result.returncode, status, result.stdout + result.stderr)
                self.assertIn("EXECUTION_ERROR", result.stdout)
                self.assertIn("RAN: dense_arm_control.py --expect dead", result.stdout)
                self.assertNotIn("BENCH SUMMARY: PASS", result.stdout)

    def test_prerequisites_stop_before_any_fixture_controls(self):
        for failing in ("TEST_PREFLIGHT_RC", "TEST_PROOF_RC"):
            with self.subTest(failing=failing):
                result = self.run_branch({failing: "2"})
                self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                self.assertNotIn("RAN: dense_arm_control", result.stdout)
                self.assertNotIn("RAN: run_ci.py", result.stdout)
                if failing == "TEST_PREFLIGHT_RC":
                    self.assertNotIn("RAN: contract_proof.py", result.stdout)
        for log in ("preflight.log", "contract-proof.log"):
            with self.subTest(log=log):
                result = self.run_branch(broken_log=log)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertNotIn("RAN: dense_arm_control", result.stdout)
                self.assertNotIn("RAN: run_ci.py", result.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)
