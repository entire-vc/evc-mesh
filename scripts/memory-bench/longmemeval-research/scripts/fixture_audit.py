#!/usr/bin/env python3
"""Per-question fixture audit for a LongMemEval full-500 run.

Joins the dataset (what SHOULD have been written), the store-id log that
mesh_client.py appends to on every successful `remember` (what WAS written) and
the run log (content-policy skips, stranded cleanups) into one row per question:

    expected / stored regular sessions, expected / stored temporal index,
    content-policy skips, failed (hypothesis is an "Error:" record), whether
    every gold answer session was stored, stranded fixtures.

Regular sessions and the temporal index are counted separately. A question with
zero stored regular sessions, or with a gold session that was neither stored nor
skipped, makes the run INVALID (exit 1): a score over an empty store is not a
recall score. `--selftest` feeds a deliberately broken run and requires exit 1
(the red control the full-500 receipt quotes).

    fixture_audit.py --dataset D.json --ids-log ids.jsonl --run-log run.log \
        --results out.jsonl --out audit.json
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import defaultdict
from pathlib import Path

SKIP_RE = re.compile(r"skipping bench-\S+?-s(?P<idx>\d+) \(qid=(?P<qid>[^)\s]+)\)")
STRANDED_RE = re.compile(r"fixtures STRANDED")
# Written by mesh_client when the temporal-index write itself is rejected by the content policy.
INDEX_POLICY_RE = re.compile(r"temporal index rejected by content policy \(qid=(?P<qid>[^)\s]+)\)")


def audit(dataset, ids_rows, run_log_text, results_rows):
    stored = defaultdict(set)
    index_stored = defaultdict(int)
    for row in ids_rows:
        qid, key = row["qid"], row.get("key", "")
        if key.endswith("-temporal-index"):
            index_stored[qid] += 1
        else:
            m = re.search(r"-s(\d+)$", key)
            if m:
                stored[qid].add(int(m.group(1)))
    skipped = defaultdict(set)
    for m in SKIP_RE.finditer(run_log_text):
        skipped[m.group("qid")].add(int(m.group("idx")))
    index_policy = {m.group("qid") for m in INDEX_POLICY_RE.finditer(run_log_text)}
    stranded_lines = len(STRANDED_RE.findall(run_log_text))
    hyp = {r["question_id"]: r.get("hypothesis", "") for r in results_rows}

    rows, invalid = [], []
    for entry in dataset:
        qid = entry["question_id"]
        sids = entry["haystack_session_ids"]
        gold_idx = [i for i, s in enumerate(sids) if s in set(entry["answer_session_ids"])]
        gold_ok = all(i in stored[qid] for i in gold_idx) and bool(gold_idx)
        gold_skipped = [i for i in gold_idx if i in skipped[qid]]
        failed = qid not in hyp or hyp[qid].startswith("Error:")
        r = {
            "question_id": qid,
            "question_type": entry["question_type"],
            "expected_regular": len(entry["haystack_sessions"]),
            # sessions with an observed outcome (stored or content-policy skipped);
            # not the expected count, so an incomplete run shows attempted < expected
            "attempted_regular": len(stored[qid]) + len(skipped[qid]),
            "stored_regular": len(stored[qid]),
            "expected_index": 1,
            "stored_index": index_stored[qid],
            "content_policy_skips": len(skipped[qid]),
            "failed": failed,
            "gold_sessions": len(gold_idx),
            "gold_stored": gold_ok,
            "gold_content_policy_skipped": len(gold_skipped),
        }
        reasons = []
        if r["stored_regular"] == 0:
            reasons.append("zero-ingest")
        if r["stored_regular"] + r["content_policy_skips"] != r["expected_regular"]:
            reasons.append("stored+skipped!=expected")
        if not gold_ok:
            reasons.append("gold-not-stored")
        if failed:
            reasons.append("failed")
        # A missing temporal index is explained only by a log line saying the INDEX write was
        # rejected by the content policy. A policy skip of a regular session proves nothing about
        # the index (it holds only 200/250-char user-turn snippets), so it never excuses a gap.
        r["index_missing_policy"] = r["stored_index"] < r["expected_index"] and qid in index_policy
        if r["stored_index"] < r["expected_index"] and not r["index_missing_policy"]:
            reasons.append("index-missing")
        r["invalid_reasons"] = reasons
        if reasons:
            invalid.append(qid)
        rows.append(r)
    totals = {
        "questions": len(rows),
        "distinct_ids": len({r["question_id"] for r in rows}),
        "stored_regular": sum(r["stored_regular"] for r in rows),
        "stored_index": sum(r["stored_index"] for r in rows),
        "index_missing_policy_skip": sum(r["index_missing_policy"] for r in rows),
        "content_policy_skips": sum(r["content_policy_skips"] for r in rows),
        "failed": sum(r["failed"] for r in rows),
        "gold_not_stored": sum(not r["gold_stored"] for r in rows),
        "invalid_questions": len(invalid),
        "stranded_cleanup_log_lines": stranded_lines,
    }
    return {"totals": totals, "invalid": invalid, "rows": rows}


def _selftest() -> int:
    ds = [
        {"question_id": "q1", "question_type": "t", "answer_session_ids": ["g"],
         "haystack_session_ids": ["a", "g"], "haystack_sessions": [[], []]},
        {"question_id": "q2", "question_type": "t", "answer_session_ids": ["g"],
         "haystack_session_ids": ["g", "b"], "haystack_sessions": [[], []]},
    ]
    good = [{"qid": "q1", "key": "bench-q1-s0"}, {"qid": "q1", "key": "bench-q1-s1"},
            {"qid": "q1", "key": "bench-q1-temporal-index"},
            {"qid": "q2", "key": "bench-q2-s0"}, {"qid": "q2", "key": "bench-q2-s1"},
            {"qid": "q2", "key": "bench-q2-temporal-index"}]
    res = [{"question_id": "q1", "hypothesis": "x"}, {"question_id": "q2", "hypothesis": "y"}]
    ok = audit(ds, good, "", res)
    assert ok["totals"]["invalid_questions"] == 0, ok["totals"]
    # broken: q2 wrote nothing (zero-ingest) and its answer is an error record
    bad = audit(ds, good[:3], "", [res[0], {"question_id": "q2", "hypothesis": "Error: boom"}])
    assert bad["invalid"] == ["q2"], bad["invalid"]
    assert "zero-ingest" in bad["rows"][1]["invalid_reasons"]
    # broken: q2's temporal index is absent and nothing explains it
    noidx = audit(ds, good[:5], "", res)
    assert noidx["invalid"] == ["q2"] and "index-missing" in noidx["rows"][1]["invalid_reasons"], noidx["invalid"]
    # a policy skip of a REGULAR session must not excuse the missing index
    skip_only = audit(ds, good[:5], "skipping bench-q2-s1 (qid=q2)", res)
    assert "index-missing" in skip_only["rows"][1]["invalid_reasons"], skip_only["rows"][1]
    # only the index-specific rejection line does
    direct = audit(ds, good[:5], "temporal index rejected by content policy (qid=q2): guard=x", res)
    assert direct["totals"]["invalid_questions"] == 0 and direct["totals"]["index_missing_policy_skip"] == 1, direct["totals"]
    print("selftest OK: clean run valid; zero-ingest, unexplained and skip-only missing-index runs invalid; index-specific policy rejection explained")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--dataset")
    ap.add_argument("--ids-log")
    ap.add_argument("--run-log")
    ap.add_argument("--results")
    ap.add_argument("--out")
    ap.add_argument("--limit", type=int, help="audit only the first N dataset questions (smoke runs)")
    a = ap.parse_args()
    if a.selftest:
        return _selftest()
    dataset = json.loads(Path(a.dataset).read_text())
    if a.limit:
        dataset = dataset[: a.limit]
    ids = [json.loads(x) for x in Path(a.ids_log).read_text().splitlines() if x.strip()]
    log = Path(a.run_log).read_text(errors="replace")
    res = [json.loads(x) for x in Path(a.results).read_text().splitlines() if x.strip()]
    out = audit(dataset, ids, log, res)
    Path(a.out).write_text(json.dumps(out, indent=1))
    print(json.dumps(out["totals"], indent=1))
    return 1 if out["invalid"] else 0


if __name__ == "__main__":
    sys.exit(main())
