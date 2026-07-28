#!/usr/bin/env python3
"""Self-checks for the version-attribution guard.

    python scripts/memory-bench/test_serving_version.py

`check_serving_version.py` is the thing standing between "we measured prod" and
"we measured whichever of two binaries happened to be up". It has the same
property as the gate it protects: its dangerous failure is silence. A guard that
returns "acceptable" for everything is indistinguishable from a working one until
somebody reads a score that straddled a deploy.

So each test below pins one way it could go quietly permissive:

  1. STRICT WHEN BLIND — if `deploy-backend.yml`'s `paths:` cannot be parsed the
     guard must fall back to requiring an EXACT commit, not to accepting any
     ancestor. `None` and `[]` must not collapse: an empty deployable-path list
     makes every commit look equivalent.
  2. PARSE PINNED AGAINST THE REAL FILE — the fallback above is safe but blind,
     so drift in the real workflow must break a test rather than silently select
     it.
  3. NEWER PROD IS A REFUSAL — a prod ahead of (or diverged from) the commit
     under test is not "close enough"; it is a different program.
  4. DEPLOYABLE DIFF BLOCKS, HARNESS DIFF DOES NOT — the whole reason the test
     is not `served == expected`: a push touching only `scripts/memory-bench/**`
     triggers this gate and no deploy, and must not wedge at INCONCLUSIVE.
  5. TIMEOUT REFUSES AND NAMES THE KIND — the workflow's alert dedups on reason
     kind, so an unnamed or generic failure is an alert that cannot be routed.
  6. A BLIP IS NOT A CHANGE — the post-run stability check must not manufacture
     a mid-run deploy out of an endpoint that failed to answer.
  7. build_time IS NOT A COMMIT — a payload with no usable `commit` must raise,
     not fall back to something that merely looks fresh.
"""

from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_serving_version as csv  # noqa: E402

REAL_DEPLOY_WORKFLOW = csv.DEPLOY_WORKFLOW
REAL_BENCH_WORKFLOW = (
    Path(__file__).resolve().parents[2] / ".github" / "workflows" / "memory-bench.yml"
)


def workflow_step(text: str, name: str) -> str:
    """Return the body of the `- name: <name>` step, or "" if it is not there.

    Stdlib only — these self-checks run before `pip install`, so no PyYAML. Slices
    on the `      - name:` indent the file already uses uniformly.
    """
    lines = text.splitlines()
    marker = f"      - name: {name}"
    try:
        start = lines.index(marker)
    except ValueError:
        return ""
    body: list[str] = []
    for line in lines[start + 1 :]:
        if line.startswith("      - name: "):
            break
        body.append(line)
    return "\n".join(body)


class CaptureStepCallsTheGuard(unittest.TestCase):
    """The guard exists; these pin that the CAPTURE path actually CALLS it.

    `test_a_mid_run_deploy_is_inconclusive_and_named` proves `--was` refuses when
    invoked. For a year it could have proved that while nothing invoked it from
    the capture arm — which is exactly what happened: the post-flight check lived
    inside the scoring step only, so a `--repeat 3` capture measured across two
    binaries (run 30336693957, prod 5d2cda8 -> ba7deae 42 min in), exited SUCCESS
    and wrote the baseline. A test for the non-call without a test for the call
    is half a guard.

    Deliberately asserted against the REAL workflow: a copy of the YAML in the
    test would keep passing after somebody edited the file.
    """

    def setUp(self):
        self.assertTrue(REAL_BENCH_WORKFLOW.exists(), REAL_BENCH_WORKFLOW)
        self.text = REAL_BENCH_WORKFLOW.read_text()

    def test_the_slicer_finds_a_real_step(self):
        """Positive control for the helper — an empty body would pass every test below."""
        body = workflow_step(self.text, "Re-snap the recall baseline")
        self.assertTrue(body.strip(), "the capture step was not found — the tests below are vacuous")
        self.assertEqual("", workflow_step(self.text, "a step that does not exist"))

    def test_the_capture_step_runs_the_post_flight_stability_check(self):
        body = workflow_step(self.text, "Re-snap the recall baseline")
        self.assertIn("SERVED_AT_START", body)
        self.assertIn("check_serving_version.py --was", body)

    def test_the_capture_step_removes_the_baseline_when_the_binary_swapped(self):
        """Refusing loudly is not enough — the file must not survive to be committed.

        `capture_blockers` refuses BEFORE writing for this reason: a guard that
        reports after the file is on disk is advice, the artifact still uploads
        and the figure still gets committed. This check can only run after the
        measurement, so deletion is how it inherits that property.
        """
        body = workflow_step(self.text, "Re-snap the recall baseline")
        self.assertIn("rm -f baseline_retrieval.json", body)

    def test_the_capture_step_records_which_binary_it_measured(self):
        body = workflow_step(self.text, "Re-snap the recall baseline")
        self.assertIn("--served-commit", body)

    def test_the_scoring_step_still_has_its_own_check(self):
        """The two arms must not drift — fixing one by moving it is not a fix."""
        body = workflow_step(self.text, "Run recall gate (no LLM)")
        self.assertTrue(body.strip(), "the scoring step was not found")
        self.assertIn("check_serving_version.py \\", body)
        self.assertIn("--was", body)


class ParseDeployPaths(unittest.TestCase):
    def test_reads_the_real_deploy_workflow(self):
        """Pinned against the real file: drift must break this, not select the fallback."""
        self.assertTrue(REAL_DEPLOY_WORKFLOW.exists(), REAL_DEPLOY_WORKFLOW)
        paths = csv.parse_deploy_paths(REAL_DEPLOY_WORKFLOW.read_text())
        self.assertIsNotNone(paths, "deploy-backend.yml `on.push.paths` no longer parses")
        # The Go server and its migrations are what "a deploy" means. If this list
        # ever stops containing them, `classify` would call a server-code change
        # equivalent and the guard would pass a run measuring the wrong binary.
        self.assertIn("internal/**", paths)
        self.assertIn("cmd/**", paths)
        self.assertIn("migrations/**", paths)
        self.assertNotIn("scripts/memory-bench/**", paths)

    def test_the_real_files_exclusions_survive_into_a_git_pathspec(self):
        """The coupling that broke once: `!` is GitHub's exclude, `:!` is git's.

        A bare `!pattern` in a `git diff -- …` pathspec is not an exclusion, it is
        an ordinary pattern matching nothing — so every exclusion in the real
        workflow evaporated and its files kept counting as deployable. That fails
        in the direction that never clears: evc-mesh#408 stopped `*_test.go`
        merges deploying, so prod legitimately stays behind after one, and a pin
        that still thinks the file deploys waits for a deploy nobody will ever
        make. Pinned against the REAL file so the next exclusion added there
        cannot desynchronise this quietly.
        """
        paths = csv.parse_deploy_paths(REAL_DEPLOY_WORKFLOW.read_text())
        spec = csv.to_git_pathspec(paths)
        self.assertEqual(len(paths), len(spec), "translation must not drop patterns")
        for raw, translated in zip(paths, spec):
            if raw.startswith("!"):
                self.assertEqual(":!" + raw[1:], translated)
            else:
                self.assertEqual(raw, translated)
        self.assertFalse(
            [s for s in spec if s.startswith("!")],
            "a bare `!` reached the pathspec — git reads it as a literal, not an exclusion",
        )

    def test_a_list_of_only_exclusions_reads_as_cannot_tell(self):
        """`[:!x]` would diff EVERYTHING-except and read as 'the whole tree deploys'."""
        self.assertIsNone(
            csv.parse_deploy_paths(
                "on:\n  push:\n    paths:\n      - '!**/*_test.go'\n"
            )
        )

    def test_unparseable_yields_none_not_empty(self):
        """None (cannot tell) and [] (nothing deploys) must never collapse."""
        self.assertIsNone(csv.parse_deploy_paths("name: x\non:\n  workflow_dispatch:\n"))
        self.assertIsNone(csv.parse_deploy_paths(""))
        self.assertIsNone(csv.parse_deploy_paths("on:\n  push:\n    branches: [main]\n"))

    def test_ignores_a_paths_block_belonging_to_another_trigger(self):
        text = (
            "on:\n"
            "  pull_request:\n"
            "    paths:\n"
            "      - 'docs/**'\n"
            "  push:\n"
            "    branches: [main]\n"
            "    paths:\n"
            "      - 'cmd/**'\n"
            "      - 'internal/**'\n"
        )
        self.assertEqual(["cmd/**", "internal/**"], csv.parse_deploy_paths(text))


class GitRepo:
    """A throwaway repo, so ancestry and diffs are exercised for real."""

    def __init__(self, tmp: Path):
        self.path = tmp
        self._run("init", "-q", "-b", "main")
        self._run("config", "user.email", "t@t")
        self._run("config", "user.name", "t")

    def _run(self, *args):
        return subprocess.run(
            ["git", *args], cwd=self.path, capture_output=True, text=True, check=True
        )

    def commit(self, rel: str, body: str) -> str:
        target = self.path / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(body)
        self._run("add", "-A")
        self._run("commit", "-q", "-m", f"touch {rel}")
        return self._run("rev-parse", "HEAD").stdout.strip()


class Classify(unittest.TestCase):
    PATHS = ["cmd/**", "internal/**", "go.mod", "go.sum", "migrations/**"]

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.repo = GitRepo(Path(self._tmp.name))
        self._orig_root = csv.REPO_ROOT
        csv.REPO_ROOT = self.repo.path
        self.base = self.repo.commit("internal/service/memory_service.go", "v1\n")

    def tearDown(self):
        csv.REPO_ROOT = self._orig_root
        self._tmp.cleanup()

    def test_exact_match_is_acceptable(self):
        ok, why = csv.classify(self.base, self.base, self.PATHS)
        self.assertTrue(ok)
        self.assertIn("exact", why)

    def test_short_sha_still_matches(self):
        ok, _ = csv.classify(self.base, self.base[:7], self.PATHS)
        self.assertTrue(ok)

    def test_harness_only_change_is_equivalent(self):
        """A push touching only scripts/memory-bench/** deploys nothing."""
        head = self.repo.commit("scripts/memory-bench/run_ci.py", "print()\n")
        ok, why = csv.classify(self.base, head, self.PATHS)
        self.assertTrue(ok, why)
        self.assertIn("equivalent", why)

    def test_deployable_change_is_refused_until_it_lands(self):
        head = self.repo.commit("internal/service/memory_service.go", "v2\n")
        ok, why = csv.classify(self.base, head, self.PATHS)
        self.assertFalse(ok)
        self.assertIn("deployable", why)

    def test_a_test_only_change_is_not_a_deployable_diff(self):
        """`_test.go` never enters a non-test build, so prod is entitled to lag.

        The negative control is the same file without the `_test` suffix: if the
        exclusion were over-broad it would swallow that too, and the guard would
        wave through a run measuring the previous binary — the failure this whole
        tool exists to prevent. Both directions, or neither proves anything.
        """
        paths = [*self.PATHS, "!**/*_test.go"]
        excluded = self.repo.commit("internal/service/memory_service_test.go", "t\n")
        ok, why = csv.classify(self.base, excluded, paths)
        self.assertTrue(ok, why)
        self.assertIn("equivalent", why)

        included = self.repo.commit("internal/service/memory_service.go", "v2\n")
        ok, why = csv.classify(self.base, included, paths)
        self.assertFalse(ok, why)
        self.assertIn("deployable", why)

    def test_prod_ahead_of_the_commit_under_test_is_refused(self):
        """Newer is not 'close enough' — it is a different program."""
        head = self.repo.commit("scripts/memory-bench/run_ci.py", "print()\n")
        ok, why = csv.classify(head, self.base, self.PATHS)
        self.assertFalse(ok)
        self.assertIn("NOT an ancestor", why)

    def test_unknown_paths_falls_back_to_strict_equality(self):
        """The blind fallback must be STRICT, never permissive."""
        head = self.repo.commit("scripts/memory-bench/run_ci.py", "print()\n")
        ok, why = csv.classify(self.base, head, None)
        self.assertFalse(ok)
        self.assertIn("exact commit match", why)
        # ...and still accepts a true exact match, so the fallback does not wedge
        # every run the moment the parse breaks.
        self.assertTrue(csv.classify(head, head, None)[0])

    def test_empty_path_list_blocks_everything_so_it_must_be_unreachable(self):
        """Documents WHY parse returns None rather than [].

        `git diff A B --` with no pathspec diffs the whole tree, so [] is not
        permissive — it is the opposite, and it would wedge the harness-only
        push that `classify` exists to let through. Either way [] is the wrong
        answer, so the parser never produces it.
        """
        head = self.repo.commit("scripts/memory-bench/run_ci.py", "print()\n")
        ok, _ = csv.classify(self.base, head, [])
        self.assertFalse(ok, "[] diffs the whole tree — it would wedge a harness-only push")
        self.assertTrue(csv.classify(self.base, head, self.PATHS)[0])
        self.assertIsNone(csv.parse_deploy_paths("on:\n  push:\n    branches: [main]\n"))

    def test_commit_absent_from_the_checkout_is_refused(self):
        ok, why = csv.classify("0" * 40, self.base, self.PATHS)
        self.assertFalse(ok)
        self.assertIn("not in this checkout", why)


class WaitForVersion(unittest.TestCase):
    def _wait(self, commits, **kw):
        seq = list(commits)
        seen = []

        def fetch(_url):
            value = seq.pop(0) if len(seq) > 1 else seq[0]
            if isinstance(value, Exception):
                raise value
            seen.append(value)
            return value

        clock = {"t": 0.0}
        kw.setdefault("wait_seconds", 60)
        kw.setdefault("poll_seconds", 15)
        rc, reason, served = csv.wait_for_version(
            "http://mesh.test",
            kw.pop("expect"),
            fetch=fetch,
            sleep=lambda s: clock.__setitem__("t", clock["t"] + s),
            now=lambda: clock["t"],
            log=lambda *_a, **_k: None,
            **kw,
        )
        return rc, reason, served, seen

    def test_returns_immediately_on_a_match(self):
        rc, reason, served, seen = self._wait(["a" * 40], expect="a" * 40, paths=["cmd/**"])
        self.assertEqual(csv.EXIT_OK, rc)
        self.assertEqual("", reason)
        self.assertEqual("a" * 40, served)
        self.assertEqual(1, len(seen), "matched on the first poll but kept polling")

    def test_waits_then_accepts_the_new_version(self):
        """AC3: a run that starts during a deploy waits for the new binary."""
        rc, reason, served, seen = self._wait(
            ["b" * 40, "b" * 40, "a" * 40], expect="a" * 40, paths=None
        )
        self.assertEqual(csv.EXIT_OK, rc)
        self.assertEqual("", reason)
        self.assertEqual("a" * 40, served)
        self.assertEqual(3, len(seen), "did not keep polling across the deploy")

    def test_timeout_refuses_and_names_the_kind(self):
        rc, reason, served, _ = self._wait(["b" * 40], expect="a" * 40, paths=None)
        self.assertEqual(csv.EXIT_INCONCLUSIVE, rc)
        self.assertTrue(reason.startswith(csv.REASON_VERSION_MISMATCH), reason)
        self.assertIn("aaaaaaa", reason)
        self.assertIn("bbbbbbb", reason)
        self.assertEqual("b" * 40, served)

    def test_endpoint_never_answers_is_unreadable_not_mismatch(self):
        rc, reason, served, _ = self._wait(
            [csv.VersionUnreadable("boom")], expect="a" * 40, paths=None
        )
        self.assertEqual(csv.EXIT_INCONCLUSIVE, rc)
        self.assertTrue(reason.startswith(csv.REASON_VERSION_UNREADABLE), reason)
        self.assertEqual("", served)

    def test_it_actually_bounds_the_wait(self):
        """A guard that can wait forever inside a 30-minute job is a timeout, not a guard."""
        rc, _, _, seen = self._wait(
            ["b" * 40], expect="a" * 40, paths=None, wait_seconds=60, poll_seconds=15
        )
        self.assertEqual(csv.EXIT_INCONCLUSIVE, rc)
        self.assertLessEqual(len(seen), 6)


class FetchServingCommit(unittest.TestCase):
    def _payload(self, body: str):
        import io

        class Resp(io.BytesIO):
            def __enter__(self_inner):
                return self_inner

            def __exit__(self_inner, *a):
                return False

        return Resp(body.encode())

    def _with_response(self, body):
        orig = csv.urllib.request.urlopen
        csv.urllib.request.urlopen = lambda *a, **k: self._payload(body)
        self.addCleanup(lambda: setattr(csv.urllib.request, "urlopen", orig))

    def test_reads_the_commit(self):
        self._with_response('{"commit":"DE52F76E1751BC28B6BCEEC7C2019A74A7777D5C","version":"de52f76"}')
        self.assertEqual(
            "de52f76e1751bc28b6bceec7c2019a74a7777d5c",
            csv.fetch_serving_commit("http://mesh.test"),
        )

    def test_build_time_is_not_a_fallback_for_a_missing_commit(self):
        """`build_time` says when the binary was built, not which one is serving."""
        self._with_response('{"build_time":"2026-07-28T03:43:10Z","version":"de52f76"}')
        with self.assertRaises(csv.VersionUnreadable):
            csv.fetch_serving_commit("http://mesh.test")

    def test_non_json_is_unreadable(self):
        self._with_response("<html>502</html>")
        with self.assertRaises(csv.VersionUnreadable):
            csv.fetch_serving_commit("http://mesh.test")

    def test_url_is_built_without_a_double_slash(self):
        self.assertEqual("http://m/api/version", csv.version_url("http://m/"))
        self.assertEqual("http://m/api/version", csv.version_url("http://m"))


class StabilityCheck(unittest.TestCase):
    """`--was`: the post-run half."""

    def _run(self, was: str, response):
        orig = csv.fetch_serving_commit
        if isinstance(response, Exception):
            csv.fetch_serving_commit = lambda *_a, **_k: (_ for _ in ()).throw(response)
        else:
            csv.fetch_serving_commit = lambda *_a, **_k: response
        self.addCleanup(lambda: setattr(csv, "fetch_serving_commit", orig))
        out = []
        orig_print = __builtins__["print"] if isinstance(__builtins__, dict) else print
        try:
            import builtins

            builtins.print = lambda *a, **k: out.append(" ".join(str(x) for x in a))
            rc = csv.main(["--base-url", "http://mesh.test", "--was", was])
        finally:
            import builtins

            builtins.print = orig_print
        return rc, "\n".join(out)

    def test_unchanged_passes(self):
        rc, out = self._run("a" * 40, "a" * 40)
        self.assertEqual(csv.EXIT_OK, rc)
        self.assertIn("unchanged", out)

    def test_a_mid_run_deploy_is_inconclusive_and_named(self):
        rc, out = self._run("a" * 40, "b" * 40)
        self.assertEqual(csv.EXIT_INCONCLUSIVE, rc)
        self.assertIn(csv.REASON_PREFIX, out)
        self.assertIn(csv.REASON_VERSION_CHANGED, out)

    def test_a_blip_is_not_a_change(self):
        """An endpoint that fails to answer is not evidence the binary swapped."""
        rc, out = self._run("a" * 40, csv.VersionUnreadable("boom"))
        self.assertEqual(csv.EXIT_OK, rc)
        self.assertIn("unknown", out)
        self.assertNotIn(csv.REASON_VERSION_CHANGED, out)


class Cli(unittest.TestCase):
    def test_missing_base_url_is_inconclusive_not_a_crash(self):
        self.assertEqual(csv.EXIT_INCONCLUSIVE, csv.main(["--base-url", "", "--record"]))

    def test_record_writes_the_served_sha_to_the_step_output(self):
        orig = csv.fetch_serving_commit
        csv.fetch_serving_commit = lambda *_a, **_k: "c" * 40
        self.addCleanup(lambda: setattr(csv, "fetch_serving_commit", orig))
        with tempfile.NamedTemporaryFile("w+", delete=False) as fh:
            out_path = fh.name
        rc = csv.main(["--base-url", "http://mesh.test", "--record", "--output", out_path])
        self.assertEqual(csv.EXIT_OK, rc)
        self.assertIn(f"served={'c' * 40}", Path(out_path).read_text())


if __name__ == "__main__":
    unittest.main(verbosity=2)
