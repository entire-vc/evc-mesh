#!/usr/bin/env python3
"""Self-check for the recall gate's rank/row observability fields.

    python scripts/memory-bench/test_gold_rank.py     # or: python -m unittest

Same convention as the sibling self-checks: stdlib `unittest`, no Mesh, no
network, no dependencies.

Background — why these fields exist at all. The gate recorded hit/miss and
nothing else, so `hit@10 = 0` was the same artifact whether the gold session
ranked 12th or was never retrieved. Those are different faults with different
fixes (ranking vs indexing/candidate-pool), and telling them apart cost a live
probe against prod instead of a five-minute read (#c6b1ecee, #1aff2b25).

The properties pinned here:

  * `hit` and `gold_rank` can never disagree — a hit is exactly a gold row at
    a rank <= top_k. This is what makes `gold_rank` trustworthy rather than a
    second, parallel measurement that can drift from the score.
  * "not retrieved" is `None`, never `0` and never `top_k`. Both of those
    would be indistinguishable from a real rank.
  * `rows_returned` counts the list BEFORE the top_k slice, because its whole
    job is to show the candidate pool truncating.
  * The fields are additive: nothing that read the old result dict breaks, and
    nothing here can change a verdict or an exit code.
"""

from __future__ import annotations

import contextlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mesh_client_stdio  # noqa: E402
import run_ci  # noqa: E402
from run_ci import gold_rank  # noqa: E402


def _row(session_idx: int, qid: str = "q") -> dict:
    """A retrieved record shaped the way `_to_record` emits them."""
    return {
        "record": {"content": f"session {session_idx}"},
        "score": 0.5,
        "key": f"bench-{qid}-s{session_idx}",
        "tags": [f"session-{session_idx}", f"bench-{qid}"],
    }


def _entry(qid: str = "q", *, haystack: int = 45, gold_session: int = 7) -> dict:
    """A dataset entry whose gold answer lives in one haystack session."""
    return {
        "question_id": qid,
        "question_type": "single-session-user",
        "question": "How long is my daily commute to work?",
        "answer": "45 minutes",
        "haystack_sessions": [[] for _ in range(haystack)],
        "haystack_dates": ["2023-01-01"] * haystack,
        "haystack_session_ids": [f"s{i}" for i in range(haystack)],
        "answer_session_ids": [f"s{gold_session}"],
    }


class TestGoldRankIsAPosition(unittest.TestCase):
    def test_first_row_is_rank_one_not_zero(self):
        # 1-based on purpose: it keeps 0 impossible, so `is None` is the only
        # "absent" and no consumer has to guess whether 0 means first or missing.
        self.assertEqual(gold_rank([_row(7), _row(3)], {7}), 1)

    def test_finds_gold_outside_the_scored_window(self):
        # The case the field was added for: top_k is 10, gold sits at 12, and
        # the gate previously recorded only "miss".
        ranked = [_row(i) for i in range(20, 31)] + [_row(7)]
        self.assertEqual(gold_rank(ranked, {7}), 12)

    def test_reports_the_first_gold_when_several_sessions_are_gold(self):
        ranked = [_row(1), _row(9), _row(4)]
        self.assertEqual(gold_rank(ranked, {4, 9}), 2)

    def test_absent_gold_is_none(self):
        self.assertIsNone(gold_rank([_row(1), _row(2)], {7}))

    def test_absent_gold_is_not_zero_and_not_the_cut(self):
        # Restated as the property the task asks for, because "returns None" is
        # a weaker claim than "cannot be confused with a rank". A sentinel of 0
        # collides with 0-based counting; a sentinel of k is exactly what a row
        # ranked last inside the window looks like.
        got = gold_rank([_row(i) for i in range(20, 30)], {7})
        self.assertIsNone(got)
        self.assertNotEqual(got, 0)
        self.assertNotEqual(got, 10)

    def test_empty_ranked_list_is_none(self):
        self.assertIsNone(gold_rank([], {7}))

    def test_unresolvable_gold_labels_are_none(self):
        # No gold indices resolved against the haystack: there is no position to
        # report, and inventing one would be worse than admitting it.
        self.assertIsNone(gold_rank([_row(7)], set()))

    def test_a_row_that_identifies_no_session_is_skipped_not_counted(self):
        # A malformed row must not consume a rank slot — that would shift every
        # real rank below it by one and quietly corrupt the number.
        junk = {"record": {"content": "x"}, "score": 0.1, "key": "", "tags": []}
        self.assertEqual(gold_rank([junk, _row(7)], {7}), 2)


class TestTheClientKeepsTheListBeforeTruncating(unittest.TestCase):
    """`rows_returned` and `ranked_records` have to be captured in `_search`,
    before the top_k slice, or the rank they describe is capped at k — a number
    that looks measured but is bounded by the very cut it exists to see past."""

    class _Session:
        def __init__(self, items):
            self.items = items

        async def call_tool(self, _name, _args):
            return {"items": self.items, "search_mode": "hybrid"}

    def _search(self, *, n_mine: int, top_k: int, tag: str = "bench-q"):
        import asyncio

        client = mesh_client_stdio.MeshMemoryClient(question_id="q")
        items = [
            {"key": f"bench-q-s{i}", "tags": [f"session-{i}", tag], "content": "c"}
            for i in range(n_mine)
        ]
        # Rows belonging to another question's fixtures, which the tag filter
        # drops. They must not inflate rows_returned.
        items += [{"key": "bench-other-s0", "tags": ["bench-other"], "content": "c"}]
        returned = asyncio.run(client._search(self._Session(items), "query", top_k))
        return client, returned

    def test_rows_returned_counts_the_full_list_not_the_window(self):
        client, returned = self._search(n_mine=32, top_k=10)
        self.assertEqual(client.rows_returned, 32)
        self.assertEqual(len(returned), 10)
        self.assertEqual(len(client.ranked_records), 32)

    def test_the_window_is_a_prefix_of_the_retained_list(self):
        # The invariant that lets `hit` and `gold_rank` be derived from two
        # different lists without them ever disagreeing.
        client, returned = self._search(n_mine=32, top_k=10)
        self.assertEqual(returned, client.ranked_records[:10])

    def test_other_questions_rows_are_not_counted(self):
        client, _ = self._search(n_mine=5, top_k=10)
        self.assertEqual(client.rows_returned, 5)

    def test_no_search_yet_is_none_not_zero(self):
        # "The search never ran" and "the search returned nothing" are different
        # facts; collapsing them would make a dead harness look like empty recall.
        client = mesh_client_stdio.MeshMemoryClient(question_id="q")
        self.assertIsNone(client.rows_returned)
        self.assertEqual(client.ranked_records, [])

    def test_an_empty_result_is_zero_not_none(self):
        client, returned = self._search(n_mine=0, top_k=10)
        self.assertEqual(client.rows_returned, 0)
        self.assertEqual(returned, [])


class TestRunSingleEmitsTheFields(unittest.TestCase):
    """Drives the real `run_single` over a fake client, so the fields are proven
    where they are actually assembled rather than in isolation."""

    def _run(self, ranked, *, top_k=10, entry=None, retrieval_only=True):
        entry = entry or _entry()

        class _Client:
            def __init__(self, question_id, **_kw):
                self.qid = question_id
                self.search_mode = "hybrid"
                self.ranked_records = ranked
                self.rows_returned = len(ranked)
                self.attempts_made = 1
                self.attempts_allowed = 4

            def ingest_and_search(self, **kw):
                return self.ranked_records[: kw["top_k"]]

        with mock.patch.object(mesh_client_stdio, "MeshMemoryClient", _Client):
            return run_ci.run_single(
                entry,
                chat_client=None,
                chat_model="",
                judge_client=mock.MagicMock(),
                judge_model="",
                top_k=top_k,
                retrieval_only=retrieval_only,
            )

    def test_a_hit_carries_its_rank(self):
        r = self._run([_row(7)] + [_row(i) for i in range(20, 30)])
        self.assertTrue(r["correct"])
        self.assertEqual(r["gold_rank"], 1)
        self.assertEqual(r["rows_returned"], 11)
        self.assertEqual(r["haystack_size"], 45)

    def test_a_miss_whose_gold_ranked_12th_says_so(self):
        # This is #c6b1ecee's 118b2229 in miniature: retrieved, ranked just past
        # the cut. Previously indistinguishable from "never indexed".
        ranked = [_row(i) for i in range(20, 31)] + [_row(7)] + [_row(40)]
        r = self._run(ranked)
        self.assertFalse(r["correct"])
        self.assertEqual(r["gold_rank"], 12)
        self.assertEqual(r["rows_returned"], 13)

    def test_a_miss_with_no_gold_anywhere_is_null(self):
        r = self._run([_row(i) for i in range(20, 45)])
        self.assertFalse(r["correct"])
        self.assertIsNone(r["gold_rank"])
        self.assertIsNotNone(r["rows_returned"])

    def test_hit_and_rank_can_never_disagree(self):
        # The load-bearing invariant. If these two could diverge, the artifact
        # would carry a rank that contradicts the score it ships next to, and
        # the field would be worse than not having it.
        for position in range(1, 21):
            with self.subTest(position=position):
                ranked = [_row(i) for i in range(50, 50 + position - 1)] + [_row(7)]
                ranked += [_row(i) for i in range(80, 90)]
                r = self._run(ranked, top_k=10)
                rank = r["gold_rank"]
                self.assertEqual(rank, position)
                self.assertEqual(r["correct"], rank is not None and rank <= 10)

    def test_rows_returned_exposes_pool_truncation(self):
        # 32 of 45 fixtures survived: the post-filter signature from #2c087b2a.
        # Read together, `rows_returned < haystack_size` is what stops `None`
        # from being misread as "not indexed".
        r = self._run([_row(i) for i in range(32)], entry=_entry(haystack=45))
        self.assertEqual(r["rows_returned"], 32)
        self.assertEqual(r["haystack_size"], 45)
        self.assertLess(r["rows_returned"], r["haystack_size"])

    def test_the_advisory_arm_carries_them_too(self):
        with mock.patch.object(run_ci, "chat_complete", return_value="answer"), \
             mock.patch.object(run_ci, "judge_answer", return_value=True):
            r = self._run([_row(7)], retrieval_only=False)
        self.assertEqual(r["gold_rank"], 1)
        self.assertEqual(r["rows_returned"], 1)

    def test_existing_keys_are_untouched(self):
        # AC: nothing renamed, nothing removed — comparison against historical
        # artifacts must not break. Additive only.
        r = self._run([_row(7)])
        for key in ("question_id", "question_type", "correct", "search_mode"):
            self.assertIn(key, r)
        self.assertEqual(r["question_id"], "q")
        self.assertEqual(r["search_mode"], "hybrid")

    def test_an_errored_question_reports_no_rank(self):
        # It never ran a recall. A rank here would be fabricated, and a
        # fabricated 'not retrieved' is exactly the wrong diagnosis to hand
        # someone chasing an indexing bug.
        class _Client:
            def __init__(self, question_id, **_kw):
                self.qid = question_id
                self.attempts_made = 1
                self.attempts_allowed = 4

            def ingest_and_search(self, **_kw):
                raise RuntimeError("Connection closed")

        with mock.patch.object(mesh_client_stdio, "MeshMemoryClient", _Client), \
             contextlib.redirect_stderr(io.StringIO()):
            r = run_ci.run_single(
                _entry(),
                chat_client=None,
                chat_model="",
                judge_client=None,
                judge_model="",
                top_k=10,
                retrieval_only=True,
            )
        self.assertIn("error", r)
        self.assertNotIn("gold_rank", r)
        self.assertNotIn("rows_returned", r)
        self.assertEqual(run_ci.format_rank_suffix(r), "")


class TestTheResultsArtifact(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.path = self.tmp / "results" / "recall_gate.json"

    def _write(self, results, **kw):
        opts = dict(
            retrieval_only=True,
            run_mode="hybrid",
            top_k=10,
            repeat=1,
            scores={"overall": 0.909},
            sizes={"overall": (22, 24)},
        )
        opts.update(kw)
        return run_ci.write_results_artifact(self.path, results, **opts)

    def test_every_question_carries_the_new_fields(self):
        results = [
            {
                "question_id": "118b2229",
                "question_type": "single-session-user",
                "correct": False,
                "search_mode": "hybrid",
                "gold_rank": 12,
                "rows_returned": 32,
                "haystack_size": 45,
            }
        ]
        out = self._write(results)
        self.assertIsNotNone(out)
        payload = json.loads(self.path.read_text())
        q = payload["questions"][0]
        self.assertEqual(q["gold_rank"], 12)
        self.assertEqual(q["rows_returned"], 32)
        # And the envelope carries what makes those numbers readable later.
        self.assertEqual(payload["search_mode"], "hybrid")
        self.assertEqual(payload["top_k"], 10)
        self.assertEqual(payload["schema_version"], run_ci.RESULTS_SCHEMA_VERSION)

    def test_a_null_rank_survives_the_json_round_trip_as_null(self):
        # json.dumps turns None into `null`, not 0 and not "None" — pinned
        # because the whole machine-readable distinction rests on it.
        out = self._write([{"question_id": "x", "gold_rank": None, "correct": False}])
        self.assertIsNotNone(out)
        raw = self.path.read_text()
        self.assertIn('"gold_rank": null', raw)
        self.assertIsNone(json.loads(raw)["questions"][0]["gold_rank"])

    def test_an_unwritable_path_does_not_raise(self):
        # Observability must not be able to fail a required check. The gate's
        # verdict is already decided when this runs.
        self.path = Path("/dev/null/cannot-exist/out.json")
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertIsNone(self._write([{"question_id": "x"}]))


class TestTheLogLine(unittest.TestCase):
    """gate.log is what a human opens after a red run, so the rank has to be
    legible there too — a JSON artifact nobody downloads is a smaller win."""

    def test_a_miss_that_ranked_shows_the_position(self):
        suffix = run_ci.format_rank_suffix(
            {"correct": False, "gold_rank": 17, "rows_returned": 27, "haystack_size": 50}
        )
        self.assertIn("rank=17/27", suffix)
        self.assertIn("of 50", suffix)

    def test_a_miss_that_never_retrieved_says_none(self):
        suffix = run_ci.format_rank_suffix(
            {"correct": False, "gold_rank": None, "rows_returned": 30, "haystack_size": 50}
        )
        self.assertIn("rank=none/30", suffix)
        # Not "rank=0" — the log has to make the same distinction the JSON does.
        self.assertNotIn("rank=0", suffix)

    def test_an_older_result_without_the_fields_is_silent(self):
        self.assertEqual(
            run_ci.format_rank_suffix({"question_id": "x", "correct": True}), ""
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
