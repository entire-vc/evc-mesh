#!/usr/bin/env python3
"""Recency control: prove the bench can SEE how old a memory is.

    python recency_control.py --expect visible    # aged corpus  -> decay must MOVE gold
    python recency_control.py --expect blind      # un-aged corpus -> decay must NOT move it
    python recency_control.py --selftest          # offline fixture checks

Why this exists
---------------
The scored bench cannot detect a change to any age-dependent ranking mechanism,
and — exactly as with the dense arm — that is structural rather than a tuning
problem. `remember` has no `created_at`, so every fixture is born at ingest and
the whole haystack shares one age. Recall multiplies each score by
`exp(-Δt·ln2/half_life)`; at equal ages that is a common factor, which reorders
nothing. Measured: the decay spread across a 300 s ingest window is 0.0344 %,
against a 0.0534 % smallest gap between adjacent RRF scores. The signal sat
below the resolution of the thing it was supposed to move.

`fixture_ages.py` closes that by backdating fixtures after ingest. This file is
the control on that fix — because "we backdated the corpus" is a claim about a
side effect nobody looks at, and the failure mode of getting it wrong (a
backdate that silently touched nothing, an age mode that fell back to the
default, a decay parameter that never reached the server) produces exactly the
numbers the blindness produced. A fix for a measurement hole that is itself
unmeasured is the same hole one level up.

The construction
----------------
One corpus, five sessions, all on the same topic and near-tied on content.
GOLD is the session that answers the query most directly, and is also the
OLDEST — 180 days back, against distractors 0-3 days old.

    decay OFF : ranking is content only            -> gold ranks near the top
    decay ON  : every score is multiplied by
                exp(-Δt·ln2/30d); gold takes a
                2^(180/30) = 64x penalty and the
                fresher near-ties overtake it      -> gold ranks strictly WORSE

So the two runs must DISAGREE on an aged corpus, and disagree in the predicted
DIRECTION — that is the positive control (AC2). It can only happen if the ages
reached the server. Measured live on run 31274262413: rank 2 with decay off,
rank 5 with it.

The requirement is the direction, not "gold is rank 1 on content". Only one
variable differs between the arms, so gold's absolute content rank is not part of
the argument; a rank-1 fixture merely reads more cleanly. Demanding it cost a
valid measurement once already (see the AC2 block in `run`). What IS blocking:
no movement at all (the bench is still blind), and movement the wrong way (decay
promoting the oldest row is a sign error, not a pass).

`--expect blind` is the negative control (AC3)
----------------------------------------------
The same query, the same five sessions, the same two decay settings — with
backdating switched off, so the corpus is born at ingest as it always was. Now
the two runs must AGREE. That is what makes the positive control mean "age",
rather than "the `apply_recency_decay` parameter perturbs something": a
parameter that reordered results on an un-aged corpus would be moving something
other than time, and every conclusion drawn from the positive arm would be
about that other thing.

Together the two arms bracket the claim:
    aged     + decay toggled -> differs   (the harness can see age)
    un-aged  + decay toggled -> identical (what it sees IS age)

Each measurement ingests its own corpus under its own tag, rather than reusing
one and recalling twice. `BoostRelevance` (memory_repo.go) stamps
`updated_at = NOW()` and bumps `relevance` on every row a recall returns, so a
second recall over the same rows is not a repeat of the first — the second
measurement would carry the first one's footprint, in the very columns this
control is about.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from fixture_ages import (  # noqa: E402
    AGE_MODE_ANCHORED,
    AGE_MODE_NOW,
    ENV_BACKDATE_DSN,
    MCP_TEMPORAL_KEYWORDS,
    BackdateError,
    parse_lme_timestamp,
    trips_temporal_profile,
)
from run_ci import (  # noqa: E402
    EXIT_INCONCLUSIVE,
    EXIT_OK,
    EXIT_REGRESSION,
    REASON_PREFIX,
    format_session_text,
    gold_rank,
)

QUERY = "What was the user's decision about the apartment lease?"

# Index 0 is gold: it answers the query outright. The distractors are the same
# subject matter with the wrong content, so they score close enough that a 64x
# decay penalty can overtake gold — a fixture where gold wins by a mile would
# make the positive control impossible to fire for the right reason, and one
# where it wins by nothing would make it fire for any reason at all.
GOLD_INDEX = 0

SESSIONS: list[list[dict]] = [
    [
        {"role": "user", "content": (
            "My decision about the apartment lease is made: I am renewing it for "
            "another year rather than moving out. The landlord agreed to keep the "
            "rent flat."
        )},
        {"role": "assistant", "content": "Renewing at the same rent sounds like a good outcome."},
    ],
    [
        {"role": "user", "content": (
            "I am still weighing whether to renew the apartment lease or move "
            "somewhere with a shorter commute."
        )},
        {"role": "assistant", "content": "Worth listing the commute time against the rent."},
    ],
    [
        {"role": "user", "content": (
            "The apartment lease paperwork the agency sent has a clause about "
            "subletting that I did not understand."
        )},
        {"role": "assistant", "content": "Subletting clauses usually require written consent."},
    ],
    [
        {"role": "user", "content": (
            "My brother asked what I had decided about his lease on the storage "
            "unit. I told him to renew it."
        )},
        {"role": "assistant", "content": "Storage units are usually month to month."},
    ],
    [
        {"role": "user", "content": (
            "I decided to keep the same phone plan for another year instead of "
            "switching provider."
        )},
        {"role": "assistant", "content": "Staying put avoids the transfer hassle."},
    ],
]

# The question is asked on 2023/06/17. Gold is 180 days older than everything
# else — a 2^6 = 64x decay penalty at the 30-day half-life, which is orders of
# magnitude above the 0.0534 % gap between adjacent RRF scores and therefore
# cannot be swamped by content scoring.
QUESTION_DATE = "2023/06/17 (Sat) 04:02"
DATES = [
    "2022/12/19 (Mon) 04:02",  # gold — 180 days before the question
    "2023/06/16 (Fri) 09:00",
    "2023/06/15 (Thu) 18:30",
    "2023/06/14 (Wed) 11:15",
    "2023/06/17 (Sat) 01:00",
]

GOLD_AGE_DAYS = 180.0
DEFAULT_TOP_K = 5

# The half-life the server uses when the caller does not override it
# (`defaultHalfLifeDays`, internal/service/memory_service.go). Mirrored, not
# imported — Python cannot read a Go constant, and a mirror that says it is a
# mirror is the most this repo can check. Only used by the selftest's arithmetic.
SERVER_HALF_LIFE_DAYS = 30.0

# The decay ratio a 180-day-old fixture suffers relative to a fresh one. The
# selftest requires this to stay far above the ranking's resolution; if a future
# dataset or half-life change shrinks it, the control silently loses power, and
# a control that cannot fire is worse than no control because it is believed.
MIN_DECAY_RATIO = 8.0


def _measure(label: str, *, age_mode: str, decay: bool, top_k: int):
    """One (age regime x decay setting) measurement -> (gold_rank, client).

    Returns `(None, None)` on an infrastructure failure so the caller can exit
    INCONCLUSIVE rather than REGRESSION — the same rule the rest of the harness
    follows. "Could not reach Mesh" must never read as "this PR broke recency".
    """
    from mesh_client_stdio import MeshMemoryClient, flatten_exc

    client = MeshMemoryClient(
        question_id=f"recency-control-{label}",
        age_mode=age_mode,
        apply_recency_decay=decay,
    )
    try:
        client.ingest_and_search(
            sessions=SESSIONS,
            dates=DATES,
            format_session_text=format_session_text,
            query=QUERY,
            top_k=top_k,
            question_date=QUESTION_DATE,
        )
    except BackdateError as exc:
        print(f"ERROR: {label}: fixtures could not be aged: {exc}", file=sys.stderr)
        return None, None
    except BaseException as exc:  # noqa: BLE001 — reported, not swallowed
        print(f"ERROR: {label}: recency control could not run: {flatten_exc(exc)}", file=sys.stderr)
        return None, None

    # The FULL ranked list, not the top_k window: the control is about ORDER, and
    # a gold session pushed out of the window by decay would otherwise report the
    # same "not found" as one that never arrived.
    rank = gold_rank(client.ranked_records, {GOLD_INDEX})
    print(
        f"  {label:<22} age_mode={age_mode:<18} apply_recency_decay={str(decay):<5} "
        f"gold_rank={rank} rows={client.rows_returned} served={client.search_mode}"
    )
    return rank, client


def run(expect: str, top_k: int) -> int:
    aged = expect == "visible"
    age_mode = AGE_MODE_ANCHORED if aged else AGE_MODE_NOW
    print(
        f"recency control: expect={expect} corpus="
        f"{'backdated' if aged else 'born-at-ingest'}"
    )

    rank_on, client_on = _measure(
        "decay-on", age_mode=age_mode, decay=True, top_k=top_k
    )
    rank_off, client_off = _measure(
        "decay-off", age_mode=age_mode, decay=False, top_k=top_k
    )

    if client_on is None or client_off is None:
        print(
            f"{REASON_PREFIX} infra-unreachable — the recency control never completed"
        )
        return EXIT_INCONCLUSIVE

    # Both arms need gold to have been RETRIEVED at all. `None` means it never
    # reached the client, and "two runs both failed to find gold" is an
    # agreement that proves nothing — it would pass the negative control for a
    # reason that has nothing to do with recency.
    if rank_off is None:
        print(
            "\n⚠ INCONCLUSIVE — with decay off, gold was not in the ranked result "
            "at all, so there is no baseline position for decay to move it from. "
            "The corpus or the tag filter is the problem, not recency."
        )
        print(f"{REASON_PREFIX} control-unarmed — gold absent with decay off")
        return EXIT_INCONCLUSIVE

    if aged and rank_off >= len(client_off.ranked_records):
        # The floor. Decay penalises gold, so the only movement this arm can
        # ever observe is DOWNWARD — and gold is already in the last position
        # there is. `rank_on > rank_off` is then unsatisfiable, which makes the
        # pass condition unreachable rather than merely unmet: the control
        # cannot go green for the right reason, only red for a wrong one.
        #
        # Without this branch that red is a MISDIAGNOSIS. The next test below
        # reports "the bench is still blind to memory age — either the backdate
        # did not reach the rows, or the decay parameter did not reach the
        # server", and neither is true when gold is simply pinned to the floor.
        # A reader acting on that message investigates the backdate plumbing,
        # which is working. Same class as the `rank_off is None` branch above,
        # and it gets the same verdict: state that the instrument had no power,
        # do not report a measurement it could not take.
        #
        # Observed live on run 32084076208: gold_rank=5 of rows=5 in all four
        # (age_mode x decay) measurements. The cause is NOT this fixture, which
        # is a strict content winner both by --selftest's stem overlap
        # ([3,2,2,1,0]) and by Postgres's own OR-arm ts_rank_cd
        # (0.4/0.3/0.3/0.2/0.1 = 0.1 x matched lexemes, gold the only row
        # matching all four of 'user'&'decis'&'apart'&'leas'). It is
        # mergeORScoredRows (memory_repo.go): a strict-AND hit keeps its
        # ts_rank_cd against the AND tsquery (cover density — 0.025 for gold)
        # while OR-only rows are scored against the OR tsquery and discounted
        # 0.8x (0.24 here). The two numbers come from different tsqueries and
        # are not comparable, so the row that matched EVERY term sorts last.
        # Tracked separately; this branch only stops the control from blaming
        # the wrong subsystem while that is fixed.
        print(
            f"\n⚠ INCONCLUSIVE — gold is already last (rank {rank_off} of "
            f"{len(client_off.ranked_records)}) with decay OFF, so the positive "
            "control has no headroom: decay can only demote, and there is "
            "nowhere below last. This says nothing about whether the bench can "
            "see age — the instrument is at its floor, not the mechanism. "
            "Fix the ranking that puts the strongest content match last, and "
            "this control re-arms on its own."
        )
        print(f"{REASON_PREFIX} control-unarmed — gold already last with decay off")
        return EXIT_INCONCLUSIVE

    if aged and rank_on is None:
        # Gold was retrieved without decay and is absent WITH it. Two different
        # things produce that, and this arm cannot tell them apart:
        #
        #   * decay demoted gold clean out of the candidate pool — movement in
        #     exactly the predicted direction, and arguably the strongest pass
        #     this control could ever record; or
        #   * gold never arrived for a reason that has nothing to do with age.
        #     `gold_rank`'s own caveat applies — `ranked_records` is what
        #     SURVIVED to the client, and scope/tags are post-filters over a
        #     workspace-wide pool, so a perfectly indexed fixture can be missing.
        #
        # Calling it a pass would certify a measurement whose cause is not
        # established, which is the one thing a control may never do. So:
        # unarmed, with its own reason.
        #
        # Before this branch existed the comparisons below reached
        # `rank_on < rank_off` with rank_on=None and raised
        # `TypeError: '<' not supported between instances of 'NoneType' and
        # 'int'`. A traceback exits non-zero, so the required job went red with
        # no verdict at all — the same queue-blocking non-measurement this file
        # is being fixed for, reached by the *success* case of the very demotion
        # the control exists to observe.
        print(
            f"\n⚠ INCONCLUSIVE — gold ranks {rank_off} with decay off and is "
            "absent from the ranked result entirely with decay on. That is "
            "either a demotion past the end of the pool or a fixture that never "
            "arrived, and this arm cannot separate the two. Re-run; if it "
            "persists, compare rows_returned against the haystack size."
        )
        print(f"{REASON_PREFIX} control-unarmed — gold absent with decay on")
        return EXIT_INCONCLUSIVE

    if aged:
        # ── AC2: the positive control ─────────────────────────────────────────
        #
        # The requirement is DIRECTIONAL, not "gold must be rank 1 on content".
        #
        # The first version demanded rank_off == 1 and refused otherwise. Live on
        # run 31274262413 it measured rank_off=2, rank_on=5 — the ages were
        # plainly reaching the ranking, and the control called itself unarmed and
        # returned INCONCLUSIVE. That precondition was about how cleanly the
        # result reads, not about whether it is valid, and it was costing the
        # measurement it was meant to protect.
        #
        # What actually makes the comparison sound is that both arms differ by
        # ONE variable: same corpus, same query, same code, only the decay flag.
        # The negative control is what licenses attributing the difference to
        # age — it shows the flag alone moves nothing on an un-aged corpus. Gold's
        # absolute content rank is not part of that argument.
        #
        # So: rank must move, and it must move DOWN, because gold is the oldest
        # fixture and decay penalises age. The direction was predicted from the
        # mechanism before any run, which is what keeps this from being a
        # threshold fitted to an observation.
        if rank_off != 1:
            print(
                f"NOTE: gold ranks {rank_off} on content (decay off), not 1. The "
                "fixture is not a clean content winner — the verdict below is "
                "still sound (one variable differs between the arms), but a "
                "sharper fixture would read better."
            )
        if rank_on == rank_off:
            print(
                f"\n✗ REGRESSION — the corpus was aged (gold {GOLD_AGE_DAYS:.0f} days "
                f"older than its distractors) and toggling apply_recency_decay did "
                f"not move gold at all (rank {rank_on} either way). The bench is "
                "still blind to memory age: either the backdate did not reach the "
                "rows the recall ranked, or the decay parameter did not reach the "
                "server. Every age-dependent profile remains unmeasurable here, "
                "which is the defect this control exists to keep closed."
            )
            return EXIT_REGRESSION
        if rank_on < rank_off:
            # Not merely "unexpected" — a sign error. Decay that PROMOTES the
            # oldest row is `exp(+λΔt)`, and the harness would then be certifying
            # a ranking that prefers stale memories. Accepting this as "the ranks
            # differ, so ages are visible" is how a control blesses the bug it was
            # built to catch.
            print(
                f"\n✗ REGRESSION — decay PROMOTED the oldest fixture: gold went "
                f"from rank {rank_off} (decay off) to {rank_on} (decay on), while "
                f"being {GOLD_AGE_DAYS:.0f} days older than every distractor. The "
                "recency term has the wrong sign, or gold is not the oldest row "
                "in the corpus this run actually wrote."
            )
            return EXIT_REGRESSION
        print(
            f"\n✓ the bench can see fixture age — gold is rank {rank_off} without "
            f"decay and rank {rank_on} with it, demoted by one "
            f"{GOLD_AGE_DAYS:.0f}-day age difference on an otherwise identical run"
        )
        return EXIT_OK

    # ── AC3: the negative control ─────────────────────────────────────────────
    if rank_on != rank_off:
        print(
            f"\n✗ CONTROL IS NOT ABOUT AGE — on a corpus whose rows are all the "
            f"same age, toggling apply_recency_decay still moved gold "
            f"({rank_off} -> {rank_on}). Then the positive control's disagreement "
            "is not evidence about time: the parameter is changing something else "
            "as well, and the aged result cannot be attributed to the backdate."
        )
        return EXIT_REGRESSION
    print(
        f"\n✓ control is about age — with every row born at ingest, the decay "
        f"toggle changes nothing (gold rank {rank_on} either way)"
    )
    return EXIT_OK


# ---------------------------------------------------------------------------
# Offline selftest: the fixture's arithmetic, before any network exists.
# ---------------------------------------------------------------------------


def _selftest() -> int:
    import math

    failures: list[str] = []

    if len(DATES) != len(SESSIONS):
        failures.append(f"{len(DATES)} dates for {len(SESSIONS)} sessions")

    q = parse_lme_timestamp(QUESTION_DATE)
    ages = [(q - parse_lme_timestamp(d)).total_seconds() / 86400.0 for d in DATES]

    if any(a < 0 for a in ages):
        failures.append(
            f"a session is dated after the question ({ages}) — it would be clamped "
            "to `now` and the intended age spread would not exist"
        )

    gold_age = ages[GOLD_INDEX]
    if abs(gold_age - GOLD_AGE_DAYS) > 1.0:
        failures.append(
            f"gold is {gold_age:.1f} days old, but GOLD_AGE_DAYS says "
            f"{GOLD_AGE_DAYS} — the docstring's arithmetic no longer matches DATES"
        )

    newest_distractor = min(a for i, a in enumerate(ages) if i != GOLD_INDEX)
    ratio = 2 ** ((gold_age - newest_distractor) / SERVER_HALF_LIFE_DAYS)
    if ratio < MIN_DECAY_RATIO:
        failures.append(
            f"gold's decay penalty relative to the freshest distractor is only "
            f"{ratio:.1f}x (need >= {MIN_DECAY_RATIO}x). Below that the control "
            "can be swamped by content scoring and its greens stop meaning anything."
        )

    # Gold must out-score every distractor on content, or "rank 1 with decay
    # off" is luck rather than a property of the fixture. Checked as a stem
    # overlap — the same crude proxy `dense_arm_control` uses for the
    # mirror-image property, imported rather than re-written so the two controls
    # cannot drift into disagreeing about what a word is.
    from dense_arm_control import _stems as stems

    qs = stems(QUERY)
    overlaps = [
        len(qs & stems(" ".join(t["content"] for t in s))) for s in SESSIONS
    ]
    if overlaps[GOLD_INDEX] <= max(
        o for i, o in enumerate(overlaps) if i != GOLD_INDEX
    ):
        failures.append(
            f"gold does not out-overlap every distractor on the query "
            f"({overlaps}) — it may not be rank 1 with decay off, which the "
            "positive control requires as its starting position"
        )

    if DEFAULT_TOP_K > len(SESSIONS):
        failures.append(
            f"top_k {DEFAULT_TOP_K} exceeds the corpus ({len(SESSIONS)})"
        )

    # THE assertion this control's power rests on. mesh-mcp classifies every
    # recall by query text and a matched `temporal` profile does
    # `applyDecay = true` OVER the caller's explicit false — plus a 7-day
    # half-life. A control query containing "when" / "ago" / "recently" /
    # "before" / "after" would therefore run with decay ON in BOTH arms: both
    # would agree, the negative control would pass, and the pass would say
    # nothing about age. The failure is silent and in the flattering direction,
    # which is why it is checked here rather than trusted.
    if trips_temporal_profile(QUERY):
        failures.append(
            f"QUERY trips mesh-mcp's temporal auto-classifier ({QUERY!r}) — the "
            "server would force apply_recency_decay=true regardless of what this "
            "control passes, so neither arm measures the toggle. Reword it to "
            "avoid " + "/".join(MCP_TEMPORAL_KEYWORDS)
        )
    # And the distractors must not drag the query there via the profile either:
    # classification is on the QUERY only, so this is belt-and-braces, but a
    # future edit that moves text between the two should not quietly re-arm it.
    if any(trips_temporal_profile(t["content"]) for s in SESSIONS for t in s):
        print(
            "NOTE: a session mentions a temporal keyword. Harmless today — "
            "mesh-mcp classifies the QUERY, not the corpus — but if that ever "
            "changes this fixture is the first thing to re-check."
        )

    # The decay direction itself: older must score LOWER. A sign error here
    # would make the positive control fire on a system that ranked stale
    # memories first, and read as success.
    lam = math.log(2) / SERVER_HALF_LIFE_DAYS
    if math.exp(-lam * gold_age) >= math.exp(-lam * newest_distractor):
        failures.append("decay arithmetic does not penalise the older fixture")

    for f in failures:
        print(f"FAIL: {f}")
    if failures:
        return 1
    print(
        f"recency_control selftest OK — gold {gold_age:.0f}d vs freshest "
        f"distractor {newest_distractor:.1f}d, decay ratio {ratio:.0f}x, "
        f"gold leads content overlap {overlaps}"
    )
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "--expect",
        choices=["visible", "blind"],
        help=(
            "'visible': aged corpus, the decay toggle MUST move gold (AC2). "
            "'blind': un-aged corpus, it must NOT (AC3)."
        ),
    )
    ap.add_argument("--top-k", type=int, default=DEFAULT_TOP_K)
    ap.add_argument(
        "--selftest",
        action="store_true",
        help="Offline: check the fixture still has the age spread it claims.",
    )
    args = ap.parse_args()

    if args.selftest:
        return _selftest()
    if not args.expect:
        ap.error("one of --expect or --selftest is required")
    if args.expect == "visible" and not (
        __import__("os").environ.get(ENV_BACKDATE_DSN, "").strip()
    ):
        # Named here rather than left to fail deep inside the client: the whole
        # arm is unrunnable without a writable database, and that is a
        # configuration fact, not a measurement.
        print(
            f"\n⚠ INCONCLUSIVE — {ENV_BACKDATE_DSN} is not set, so the corpus "
            "cannot be aged and the positive control has nothing to detect."
        )
        print(f"{REASON_PREFIX} control-unarmed — {ENV_BACKDATE_DSN} is not set")
        return EXIT_INCONCLUSIVE
    return run(args.expect, args.top_k)


if __name__ == "__main__":
    sys.exit(main())
