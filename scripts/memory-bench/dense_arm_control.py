#!/usr/bin/env python3
"""Dense-arm control: prove the recall gate can still SEE the vector arm.

    python dense_arm_control.py --expect alive   # dense arm up   -> gold must be found
    python dense_arm_control.py --expect dead    # dense arm down -> gold must be MISSED

Why this exists
---------------
The scored bench cannot detect the loss of the dense arm, and that is not a
tuning problem — it is structural.

Every question in `data/lme_s_24.json` asks about a fact stated in the
conversation in roughly the words the question uses. BM25 alone answers them.
Measured on run 30316983402: a completely dead vector arm — every fixture
written after the chunking deploy had `memories.embedding IS NULL`, so
`VectorSearch`'s `embedding IS NOT NULL` filter returned **zero rows for the
whole haystack** — scored `single-session-user 1.000`, `overall 0.9583`, the best
numbers ever recorded, while reporting `search_mode: hybrid, degraded: false`.

So a gate built only on those questions certifies a system whose dense arm has
been amputated. This file is the missing question: one query that BM25 *cannot*
answer, whose gold session is reachable only through vector similarity.

The construction, and why each half is load-bearing
---------------------------------------------------
`QUERY` and `GOLD_SESSION` share **no content word**. The query says "dog",
"breed", "get"; the gold session says "Weimaraner", "kennel", "picked up". After
stemming and stopword removal the intersection is empty, so the BM25 arm has
nothing to match on and cannot rank gold at all.

The distractors are deliberately BM25 *bait*: they carry the query's own words
("breed standards at dog shows", "the breed she likes is Kohaku", "the dog
park") while being the wrong answer. A keyword-only search ranks them first and
gold nowhere — which is precisely the failure this control turns into a red.

Measured against the CI embedder (`BAAI/bge-small-en-v1.5`, 2026-07-28):

    cos   bm25-overlap  text
    0.7679          0   GOLD (Weimaraner / kennel)          <- dense rank 1
    0.6851          2   documentary about breed standards at dog shows
    0.6629          1   colleague breeds koi carp ... Kohaku
    0.5832          1   the dog park near the office closed
    0.5508          0   quarterly revenue forecast, Frankfurt
    ...
    margin over the best distractor: +0.0828

Gold is rank 1 by cosine with a clear margin, and rank *nowhere* by keyword. The
control therefore has power in both directions, which is the property that
matters: an assertion that can only pass is not a control.

`--expect dead` is the positive control ON THE CONTROL
------------------------------------------------------
Asserting "gold is found" proves nothing on its own — it passes if the corpus is
tiny enough that everything is in top-k, if `top_k` is generous, or if BM25 got
lucky. So the workflow runs this file a second time against the SAME binary and
the SAME corpus with the embedder stopped, and requires the assertion to FAIL.

That is the step that has been missing every previous time this harness went
blind: a green whose ability to go red was never demonstrated. Here it is
demonstrated on every run, against the same code the green came from.

`--expect dead` additionally requires the server to have reported `bm25-only`.
Without that, a "dead" run that *did* have a live dense arm and simply missed
gold for an unrelated reason would be read as proof of power.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from run_ci import (  # noqa: E402
    EXIT_INCONCLUSIVE,
    EXIT_OK,
    EXIT_REGRESSION,
    MODE_BM25_ONLY,
    MODE_HYBRID,
    REASON_PREFIX,
    format_session_text,
    retrieved_session_indices,
)

# The control question. See the module docstring for the measurement that fixed
# this exact wording — do not paraphrase either side without re-running
# `--selftest`, which re-checks the lexical-darkness half.
QUERY = "Which dog breed did the user recently get?"

# Index 0 is gold. Everything after it is a distractor; the first three carry the
# query's vocabulary on purpose.
GOLD_INDEX = 0

SESSIONS: list[list[dict]] = [
    [
        {"role": "user", "content": (
            "I finally picked up the Weimaraner I had been waiting for. She came "
            "from a small kennel two hours north and settled in overnight."
        )},
        {"role": "assistant", "content": "Congratulations on the new arrival."},
    ],
    [
        {"role": "user", "content": (
            "I watched a documentary about breed standards at dog shows in the 1960s."
        )},
        {"role": "assistant", "content": "Those standards changed a lot over the decades."},
    ],
    [
        {"role": "user", "content": (
            "My colleague breeds koi carp; the breed she likes best is Kohaku."
        )},
        {"role": "assistant", "content": "Kohaku are the classic red-and-white variety."},
    ],
    [
        {"role": "user", "content": "The dog park near the office closed for renovation until spring."},
        {"role": "assistant", "content": "That is inconvenient for the morning walk."},
    ],
    [
        {"role": "user", "content": "Did you get the quarterly revenue forecast for the Frankfurt office?"},
        {"role": "assistant", "content": "It landed in your inbox this morning."},
    ],
    [
        {"role": "user", "content": "I am switching my coffee order to a lighter Ethiopian roast."},
        {"role": "assistant", "content": "Those tend to be more floral."},
    ],
    [
        {"role": "user", "content": "The recently renovated library finally reopened on Tuesday."},
        {"role": "assistant", "content": "Good timing for the reading list."},
    ],
]

DATES = ["2026/01/0%d (Mon) 09:00" % (i + 1) for i in range(len(SESSIONS))]

# Deliberately tight. A generous window lets a 7-session corpus return everything
# and the assertion passes without the dense arm having contributed anything —
# the same "true for the wrong reason" shape this file exists to rule out.
DEFAULT_TOP_K = 3

# Stopwords for the lexical-darkness self-check only. Postgres' `english`
# dictionary is the real judge; this list is deliberately SMALLER, so a word it
# fails to strip counts as overlap and the self-check errs toward complaining.
_STOP = set(
    "a an the i my me we our you your he she it they them his her its is are was "
    "were be been being do does did doing have has had having will would shall "
    "should can could may might must of in on at to for with from by about and "
    "or but if then than that this these those what which who whom when where "
    "how why so as up out over under again further once no not only own same "
    "just now user recently".split()
)


def _stems(text: str) -> set[str]:
    """Crude suffix-stripper, only ever used by --selftest.

    Not a Porter implementation and not trying to be: its job is to catch a
    shared root that Postgres' stemmer would also collapse. It over-strips, which
    makes it MORE likely to report an overlap, which is the safe direction for a
    check whose failure means "this control is not lexically dark".
    """
    import re

    out: set[str] = set()
    for w in re.findall(r"[a-z]+", text.lower()):
        if w in _STOP or len(w) < 3:
            continue
        for suf in ("ing", "ies", "ied", "es", "ed", "s"):
            if w.endswith(suf) and len(w) - len(suf) >= 3:
                w = w[: -len(suf)]
                break
        out.add(w)
    return out


def lexical_overlap() -> set[str]:
    """Stems shared by the query and the gold session. Must be empty."""
    gold_text = " ".join(t["content"] for t in SESSIONS[GOLD_INDEX])
    return _stems(QUERY) & _stems(gold_text)


def selftest() -> int:
    """Offline checks — no Mesh, no embedder, no network.

    Runs on every PR next to the other gate self-checks, because the property it
    pins is a property of THIS FILE: an edit that gives the query and the gold
    session a word in common silently converts the control into something BM25
    can answer, and then a dead dense arm passes it.
    """
    failures: list[str] = []

    overlap = lexical_overlap()
    if overlap:
        failures.append(
            f"query and gold share stem(s) {sorted(overlap)} — BM25 can reach gold, "
            "so this control no longer proves the dense arm ran"
        )

    if not (0 <= GOLD_INDEX < len(SESSIONS)):
        failures.append(f"GOLD_INDEX {GOLD_INDEX} is not a session index")

    if len(DATES) != len(SESSIONS):
        failures.append(f"{len(DATES)} dates for {len(SESSIONS)} sessions")

    # At least one distractor must carry the query's words, or "BM25 ranks the
    # wrong thing first" is an assumption rather than a fact about the fixture.
    q = _stems(QUERY)
    bait = [
        i
        for i, s in enumerate(SESSIONS)
        if i != GOLD_INDEX and (q & _stems(" ".join(t["content"] for t in s)))
    ]
    if not bait:
        failures.append(
            "no distractor shares a stem with the query — nothing outranks gold "
            "on keywords, so a bm25-only run could still find it"
        )

    if DEFAULT_TOP_K >= len(SESSIONS):
        failures.append(
            f"top_k {DEFAULT_TOP_K} >= corpus {len(SESSIONS)} — everything is "
            "returned and the assertion cannot fail"
        )

    for f in failures:
        print(f"FAIL: {f}")
    if failures:
        return 1
    print(
        f"dense_arm_control selftest OK — lexical overlap none, "
        f"{len(bait)} bm25-bait distractor(s), top_k {DEFAULT_TOP_K} of {len(SESSIONS)}"
    )
    return 0


def run(expect: str, top_k: int) -> int:
    from mesh_client_stdio import MeshMemoryClient, flatten_exc

    client = MeshMemoryClient(question_id="dense-arm-control")
    try:
        # `search_settle_ok` only on the "alive" path: `remember()` embeds
        # asynchronously (task a2e00afd), so a store-then-immediately-search
        # question like this one can race the embedding write and see gold
        # missing for reasons that have nothing to do with this PR. Retrying
        # the search a few times (never re-storing) closes that timing gap
        # without weakening the check: a dense arm that never contributes
        # still exhausts the budget and fails exactly as before. NOT applied
        # on the "dead" path — that path WANTS gold missing, and settling
        # would only slow down a check that has nothing to wait for.
        settle_ok = (
            (lambda results: GOLD_INDEX in retrieved_session_indices(results))
            if expect == "alive"
            else None
        )
        results = client.ingest_and_search(
            sessions=SESSIONS,
            dates=DATES,
            format_session_text=format_session_text,
            query=QUERY,
            top_k=top_k,
            search_settle_ok=settle_ok,
        )
    except BaseException as exc:  # noqa: BLE001 — reported, not swallowed
        # Same rule as the rest of the harness: could not measure => 2, never 1.
        # Exit 1 here would read as "this PR broke the dense arm" at an author
        # whose only problem is that the bench could not reach Mesh.
        print(f"ERROR: dense-arm control could not run: {flatten_exc(exc)}", file=sys.stderr)
        print(f"{REASON_PREFIX} infra-unreachable — dense-arm control never completed")
        return EXIT_INCONCLUSIVE

    found = GOLD_INDEX in retrieved_session_indices(results)
    mode = client.search_mode
    print(
        f"dense-arm control: expect={expect} served={mode} "
        f"gold_found={found} rows_returned={client.rows_returned} top_k={top_k}"
    )

    if expect == "alive":
        if mode != MODE_HYBRID:
            # The run cannot answer the question it was asked. Not a regression:
            # the embedder being down says nothing about this PR's code.
            print(
                f"\n⚠ INCONCLUSIVE — the dense-arm control expected a hybrid run "
                f"and was served '{mode}'. The dense arm never ran, so its "
                "contribution could not be measured."
            )
            print(f"{REASON_PREFIX} dense-arm-unserved — expected hybrid, served '{mode}'")
            return EXIT_INCONCLUSIVE
        if not found:
            print(
                "\n✗ REGRESSION — the control question is unreachable by keyword "
                "and was NOT retrieved on a run the server reported as hybrid. "
                "The dense arm ran but contributed nothing usable: either its "
                "rows are empty (write path not storing embeddings), its read "
                "path returns nothing, or the merge drops it."
            )
            return EXIT_REGRESSION
        print("✓ dense arm is contributing — keyword-dark gold was retrieved")
        return EXIT_OK

    # expect == "dead" — the positive control on the control.
    if mode != MODE_BM25_ONLY:
        print(
            f"\n⚠ INCONCLUSIVE — the power check needs the dense arm OFF and the "
            f"server reported '{mode}'. The embedder was still reachable, so a "
            "miss here would not have proved anything."
        )
        print(f"{REASON_PREFIX} power-check-unarmed — expected bm25-only, served '{mode}'")
        return EXIT_INCONCLUSIVE
    if found:
        print(
            "\n✗ CONTROL HAS NO POWER — gold was retrieved with the dense arm "
            "switched off, so BM25 can reach it after all. Every green this "
            "control has ever produced was uninformative: it would pass with "
            "vector search entirely removed. Retune the fixture (see the module "
            "docstring) until a bm25-only run misses gold."
        )
        return EXIT_REGRESSION
    print("✓ control has power — with the dense arm off, gold is missed as required")
    return EXIT_OK


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument(
        "--expect",
        choices=["alive", "dead"],
        help="'alive': gold must be retrieved. 'dead': gold must be MISSED (power check).",
    )
    ap.add_argument("--top-k", type=int, default=DEFAULT_TOP_K)
    ap.add_argument(
        "--selftest",
        action="store_true",
        help="Offline: check the fixture is still lexically dark. No Mesh needed.",
    )
    args = ap.parse_args()

    if args.selftest:
        return selftest()
    if not args.expect:
        ap.error("one of --expect or --selftest is required")
    return run(args.expect, args.top_k)


if __name__ == "__main__":
    sys.exit(main())
