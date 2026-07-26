#!/usr/bin/env python3
"""LongMemEval-S CI regression gate for evc-mesh memory.

Two gates, deliberately separated by cost and determinism:

  --retrieval-only  RECALL gate. Ingests the haystack, runs `recall`, and scores
                    a question as correct iff a gold (`answer_session_ids`)
                    session is retrieved in the top-k. NO LLM — free, no paid
                    third-party dependency, and it measures OUR memory rather
                    than the answering model. This is the arm intended to become
                    a required check.

  (default)         Full end-to-end LongMemEval: recall → answer with a chat
                    model → grade with an LLM judge. Measures the whole stack,
                    but depends on a paid API and a nondeterministic judge, so it
                    stays advisory (run on dispatch/nightly), never a merge gate.

Exit codes (the load-bearing distinction):
  0  all categories within tolerance of baseline
  1  REGRESSION — the eval ran, and memory quality dropped below tolerance
  2  INCONCLUSIVE — the eval could NOT run, or could not run COMPARABLY:
       * Mesh unreachable / judge/chat API down or out of credit, …
       * no baseline to compare against (a pass against nothing is a vacuous
         green — a gate that enforces nothing must never look healthy)
       * the run's search_mode differs from the baseline's (see below)
     An eval that did not run has NOT measured a regression. Scoring an un-asked
     question as "wrong" turns any infra outage into a fake quality collapse, so
     errored questions are excluded from the scores and reported separately.

Mode-scoped baseline (why exit 2 also covers a cross-mode comparison):
  Mesh recall is hybrid — a BM25 arm plus a dense/vector arm — and it FAILS OPEN
  when the embedder dies: it quietly serves BM25-only results with a 200. Recall
  now reports the mode it was actually served in (`search_mode`: "hybrid" |
  "bm25-only"). Hit@k in bm25-only mode is systematically lower than in hybrid
  mode, so comparing across modes measures the EMBEDDER'S HEALTH, not the PR.
  A required check that goes red because prod's embedder ran out of credit blocks
  every PR in the repo and blames each author for a fault they did not cause — a
  permanent merge-wedge (cf. evc-mesh#320). So: mismatched modes → exit 2, NEVER
  exit 1. Re-snap the baseline when the embedder's health state changes.

Usage (Mac Mini baseline generation):
  python run_ci.py --update-baseline                  # LLM baseline
  python run_ci.py --retrieval-only --update-baseline # recall baseline

Usage (CI regression gate):
  python run_ci.py --retrieval-only [--tolerance 0.25]

Required environment variables:
  MESH_API_URL        — Mesh API base URL
  MESH_AGENT_KEY      — Mesh agent key (ci-bench)

Required for the full (non --retrieval-only) eval:
  LME_JUDGE_API_KEY   — OpenRouter or OpenAI key for LLM judge
  LME_JUDGE_BASE_URL  — Judge API base URL (default: https://openrouter.ai/api/v1)
  LME_JUDGE_MODEL     — Judge model (default: openai/gpt-4o-mini)

Optional:
  MESH_MCP_BIN        — Path to mesh-mcp binary (default: ~/bin/mesh-mcp)
  LME_TOP_K           — Recall top-k (default: 10)
  RECALL_GRAPH_ENABLED — Pass to mesh-mcp (default: false)
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import time
import traceback
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

SCRIPT_DIR = Path(__file__).resolve().parent
DATA_FILE = SCRIPT_DIR / "data" / "lme_s_24.json"
BASELINE_FILE = SCRIPT_DIR / "baseline.json"
RETRIEVAL_BASELINE_FILE = SCRIPT_DIR / "baseline_retrieval.json"

# Exit codes — 1 (regression) and 2 (could-not-run) must never be conflated.
EXIT_OK = 0
EXIT_REGRESSION = 1
EXIT_INCONCLUSIVE = 2

# Retrieval modes Mesh can serve a recall in. UNKNOWN = the server did not say
# (older Mesh, or the recall never happened) — treated as not-comparable, never
# as healthy.
MODE_HYBRID = "hybrid"
MODE_BM25_ONLY = "bm25-only"
MODE_UNKNOWN = "unknown"

# Machine-readable reason kinds, grepped out of the log by the CI workflow so it
# can raise ONE out-of-band alert per reason instead of one per PR.
REASON_NO_BASELINE = "no-baseline"
REASON_MODE_MISMATCH = "mode-mismatch"
REASON_MODE_UNKNOWN = "mode-unknown"
REASON_HARNESS_ERRORS = "harness-errors"
REASON_CATEGORY_UNMEASURED = "category-unmeasured"
# Distinct KIND on purpose. The alert dedups on reason kind, so folding this into
# `category-unmeasured` would let a live "we lost questions to harness errors"
# alert suppress the arrival of "your baseline's denominator no longer matches" —
# a different cause with a different owner and a different fix (re-snap, not
# stop-losing-questions).
REASON_BASELINE_SAMPLE_MISMATCH = "baseline-sample-mismatch"
# Also its own kind, and it OUTRANKS the two above when several apply: it is the
# cause under them. A question lost every run is what shrinks a category below
# `category_comparable`, and re-snapping a baseline while it is lost just records
# the broken denominator as the new truth. Alerting on the symptom while the
# cause renews itself every run is how this survived 6 days.
REASON_PERSISTENT_ERRORS = "persistent-errors"
REASON_PREFIX = "GATE_REASON:"

# How an errored question is classified. `--max-error-rate` was calibrated for
# TRANSIENT infrastructure failures — a mesh-api restart mid-run, a 502 — where
# forgiving up to 10% of a run is right, because the next run measures those
# questions again. It is the wrong instrument for a failure that recurs
# identically for ever: a budget that forgives 10% per run forgives the SAME 8%
# permanently, the questions under it are never measured again, and the report
# line that says so appears in 100% of runs, which is how it comes to read as
# furniture. (2 of 24 sat there for 6 days; both were `temporal-reasoning`, so
# one seventh of the safety net quietly did not exist.)
ERROR_TRANSIENT = "transient"
ERROR_PERSISTENT = "persistent"

ANSWER_SYSTEM = (
    "You are a helpful chat assistant. You have access to memories retrieved "
    "from your past conversations with the user. Use these memories to answer "
    "the user's question. If the memories do not contain enough information, "
    "say so honestly."
)

ANSWER_PROMPT = """\
Below are memories retrieved from our past conversations, followed by the \
user's question.

Important guidelines:
- Each conversation is tagged with its date in a [Conversation date: ...] \
header. Use these dates to reason about when events happened, their \
chronological order, and time spans between them.
- When information was updated across conversations (e.g., a number changed, \
a preference shifted, a status was revised), ALWAYS use the value from the \
MOST RECENT conversation. Later conversations supersede earlier ones.
- Answer based only on what is explicitly stated. Do not add to or modify \
stated values.
- For counting questions ("how many X"), carefully enumerate every distinct \
item mentioned across ALL conversations.

## Retrieved Memories
{memory_context}

## Current Date
{current_date}

## Question
{question}

Answer directly and specifically based on the memories above."""

JUDGE_PROMPT_TEMPLATE = """\
I will give you a question, a correct answer, and a response from a model.
Please answer yes if the response contains the correct answer. Otherwise, answer no.
If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes.
If the response only contains a subset of the information required by the answer, answer no.

Question: {question}

Correct Answer: {answer}

Model Response: {response}

Is the model response correct? Answer yes or no only."""

JUDGE_PROMPT_TEMPORAL = """\
I will give you a question, a correct answer, and a response from a model.
Please answer yes if the response contains the correct answer. Otherwise, answer no.
If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes.
Do not penalize off-by-one errors for the number of days.

Question: {question}

Correct Answer: {answer}

Model Response: {response}

Is the model response correct? Answer yes or no only."""


def _load_env() -> None:
    env_file = SCRIPT_DIR / ".env.ci"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, _, v = line.partition("=")
            os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))


def _require_env(key: str) -> str:
    val = os.environ.get(key, "")
    if not val:
        # EXIT_INCONCLUSIVE, never EXIT_REGRESSION. A missing credential means the
        # eval could not RUN — it says nothing about memory quality. Exiting 1 here
        # would collide with EXIT_REGRESSION and make the (required) gate report
        # "this PR makes memory worse" at an author who merely has no access to the
        # secrets: a PR from a fork gets none, and that merge could never be cleared.
        # Same rule as everywhere else in this harness: cannot measure => exit 2.
        print(f"ERROR: {key} is not set", file=sys.stderr)
        print(
            f"GATE_REASON: infra-unreachable — {key} is not set "
            "(no access to Mesh credentials; nothing was measured)"
        )
        sys.exit(EXIT_INCONCLUSIVE)
    return val


def format_session_text(session: list[dict], date: str = "") -> str:
    lines: list[str] = []
    if date:
        lines.append(f"[Conversation date: {date}]")
    for turn in session:
        role = turn["role"].capitalize()
        lines.append(f"{role}: {turn['content']}")
    return "\n".join(lines)


def build_memory_context(results: list[dict]) -> str:
    if not results:
        return "(no memories retrieved)"
    blocks = []
    for idx, item in enumerate(results, start=1):
        content = (item.get("record") or {}).get("content", "")
        score = item.get("score")
        header = f"[Memory {idx}]"
        if score is not None:
            header += f" (score={score:.3f})"
        blocks.append(f"{header}\n{content}")
    return "\n\n".join(blocks)


def chat_complete(client: Any, *, model: str, system: str, user: str) -> str:
    resp = client.chat.completions.create(
        model=model,
        messages=[{"role": "system", "content": system}, {"role": "user", "content": user}],
        temperature=0.0,
        max_tokens=1024,
    )
    return resp.choices[0].message.content.strip()


def judge_answer(client: Any, *, model: str, question_type: str, question: str, answer: str, response: str) -> bool:
    if question_type == "temporal-reasoning":
        prompt = JUDGE_PROMPT_TEMPORAL.format(question=question, answer=answer, response=response)
    else:
        prompt = JUDGE_PROMPT_TEMPLATE.format(question=question, answer=answer, response=response)
    verdict = client.chat.completions.create(
        model=model,
        messages=[{"role": "user", "content": prompt}],
        temperature=0.0,
        max_tokens=8,
    ).choices[0].message.content.strip().lower()
    return verdict.startswith("yes")


def gold_session_indices(entry: dict) -> set[int]:
    """Indices into haystack_sessions that hold the answer's evidence."""
    gold = set(entry.get("answer_session_ids") or [])
    return {
        idx
        for idx, sid in enumerate(entry.get("haystack_session_ids") or [])
        if sid in gold
    }


def retrieved_session_indices(results: list[dict]) -> set[int]:
    """Recover the haystack index of each retrieved memory.

    Stores are keyed `bench-<qid>-<run nonce>-s<idx>` and tagged `session-<idx>`
    by mesh_client_stdio; either is enough to identify the session. The key is
    read from its TRAILING `-s<idx>`, so whatever the nonce contains cannot move
    the index.
    """
    found: set[int] = set()
    for item in results:
        for tag in item.get("tags") or []:
            if isinstance(tag, str) and tag.startswith("session-"):
                try:
                    found.add(int(tag.removeprefix("session-")))
                except ValueError:
                    pass
        key = item.get("key") or ""
        _, sep, suffix = key.rpartition("-s")
        if sep and suffix.isdigit():
            found.add(int(suffix))
    return found


def run_single(
    entry: dict,
    *,
    chat_client: Any,
    chat_model: str,
    judge_client: Any,
    judge_model: str,
    top_k: int,
    retrieval_only: bool = False,
) -> dict:
    from mesh_client_stdio import MeshMemoryClient, flatten_exc

    qid = entry["question_id"]
    qtype = entry["question_type"]

    def errored(stage: str, exc: BaseException) -> dict:
        # NOTE: no "correct" key. An errored question was never asked, so it is
        # excluded from the scores rather than counted as a wrong answer — that
        # conflation is what turned an API 402 into a fake 7-category
        # "REGRESSION" on every PR.
        #
        # Report the exception's LEAVES, not the TaskGroup wrapper. When the gate
        # goes blind, this string is the whole explanation anyone gets; "unhandled
        # errors in a TaskGroup (1 sub-exception)" names the plumbing and hides
        # the fault, so the reason we stopped enforcing stays unknown.
        detail = flatten_exc(exc)
        logger.error("%s failed for %s: %s", stage, qid, detail)
        return {
            "question_id": qid,
            "question_type": qtype,
            "error": f"{stage}: {detail}",
            "error_stage": stage,
            # Classified HERE, where the retry budget this question actually
            # spent is still known. `client` is bound below and this closure is
            # never called before that; getattr keeps a stage that fails before
            # the client exists from turning into an AttributeError instead of
            # an error report.
            "error_kind": classify_error(
                detail,
                getattr(client, "attempts_made", 0),
                getattr(client, "attempts_allowed", 0),
            ),
        }

    client = MeshMemoryClient(question_id=qid)
    try:
        results = client.ingest_and_search(
            sessions=entry["haystack_sessions"],
            dates=entry["haystack_dates"],
            format_session_text=format_session_text,
            query=entry["question"],
            top_k=top_k,
        )
    except Exception as exc:
        traceback.print_exc()
        return errored("ingest_and_search", exc)

    # Which arms actually served this question's recall. Carried on every result
    # so the run-level mode can be resolved before any score is compared.
    search_mode = getattr(client, "search_mode", None) or MODE_UNKNOWN

    if retrieval_only:
        gold = gold_session_indices(entry)
        if not gold:
            return errored(
                "gold_labels",
                ValueError("no answer_session_ids resolved against haystack"),
            )
        hit = bool(gold & retrieved_session_indices(results))
        return {
            "question_id": qid,
            "question_type": qtype,
            "correct": hit,
            "search_mode": search_mode,
        }

    memory_context = build_memory_context(results)
    user_message = ANSWER_PROMPT.format(
        memory_context=memory_context,
        current_date=entry.get("question_date", ""),
        question=entry["question"],
    )

    try:
        response = chat_complete(chat_client, model=chat_model, system=ANSWER_SYSTEM, user=user_message)
    except Exception as exc:
        return errored("chat_complete", exc)

    try:
        correct = judge_answer(
            judge_client,
            model=judge_model,
            question_type=qtype,
            question=entry["question"],
            answer=entry["answer"],
            response=response,
        )
    except Exception as exc:
        return errored("judge_answer", exc)

    return {
        "question_id": qid,
        "question_type": qtype,
        "correct": correct,
        "search_mode": search_mode,
    }


def resolve_run_search_mode(results: list[dict]) -> str:
    """Collapse the per-question search modes into one mode for the whole run.

    Degradation dominates, and unknown dominates degradation:

      * any question served UNKNOWN  → the run is UNKNOWN (we cannot claim to
        know what we measured);
      * any question served bm25-only → the run is bm25-only (the run as a whole
        is not comparable to a hybrid baseline: even one degraded recall moves
        the aggregate score);
      * only then → hybrid.

    A run in which nothing recalled at all (no modes observed) is UNKNOWN.
    """
    modes = {r["search_mode"] for r in results if r.get("search_mode")}
    if not modes:
        return MODE_UNKNOWN
    if MODE_UNKNOWN in modes or not modes <= {MODE_HYBRID, MODE_BM25_ONLY}:
        return MODE_UNKNOWN
    if MODE_BM25_ONLY in modes:
        return MODE_BM25_ONLY
    return MODE_HYBRID


def load_baseline(path: Path) -> tuple[dict[str, float], str]:
    """Read a baseline file, returning (scores, search_mode).

    Two shapes are accepted:

      new  {"search_mode": "...", "captured_at": "...", "top_k": 10,
            "scores": {"<category>": 0.75, ...}}
      old  {"<category>": 0.75, ...}            — flat, pre-search_mode

    The old shape carries no mode, so it resolves to UNKNOWN, which makes every
    comparison against it INCONCLUSIVE rather than a coin-flip between a fake
    regression and a fake pass. Re-snap with --update-baseline to fix.
    """
    raw = json.loads(path.read_text())
    if isinstance(raw.get("scores"), dict):
        mode = raw.get("search_mode") or MODE_UNKNOWN
        return {k: float(v) for k, v in raw["scores"].items()}, str(mode)
    return {k: float(v) for k, v in raw.items()}, MODE_UNKNOWN


def load_baseline_samples(path: Path) -> dict[str, int]:
    """Per-category DENOMINATORS recorded with the baseline, or `{}` if it has none.

    A score without its sample size is not a comparable operand. `temporal-reasoning:
    1.0` in `baseline_retrieval.json` was captured on 2 questions, because the other
    2 could not be stored at all; a later run that measures all 4 and scores 0.5
    would be reported as a -0.5 quality regression when nothing about quality
    changed. #361 added the guard for a RUN whose sample shrank — this is the same
    rule applied to the operand nothing was checking: the baseline's own.

    Absence is deliberately NOT read as "not comparable". No baseline in existence
    carries this field yet, so treating its absence as a mismatch would take the
    required gate blind on every category at once — a reader tightened past
    anything its writer has ever emitted is a permanent no-op wearing a guard's
    uniform. It starts enforcing from the first re-snapped baseline onward.
    """
    try:
        raw = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        return {}
    samples = raw.get("samples") if isinstance(raw, dict) else None
    if not isinstance(samples, dict):
        return {}
    out: dict[str, int] = {}
    for cat, n in samples.items():
        # bool is an int subclass; `True` is not a denominator.
        if isinstance(n, bool) or not isinstance(n, (int, float)):
            continue
        out[str(cat)] = int(n)
    return out


def retrieval_baseline_payload(
    scores: dict[str, float],
    sizes: dict[str, tuple[int, int]],
    run_mode: str,
    top_k: int,
    captured_at: str,
) -> dict[str, Any]:
    """The mode-scoped baseline file, as a value.

    A pure function so the writer and `load_baseline_samples` can be tested
    against each other for real. Built inline before, which meant the only way to
    check that what is written is what the reader enforces was to re-type the
    dict in the test — and a test that re-implements the writer agrees with it by
    construction, including when both are wrong.
    """
    return {
        "search_mode": run_mode,
        "captured_at": captured_at,
        "top_k": top_k,
        # The DENOMINATOR every score above was computed on. A baseline that
        # records only its scores cannot be told apart from one captured while
        # some questions could not run at all — which is exactly how
        # `temporal-reasoning: 1.0` came to mean "2 of 4" and then sat there for
        # six days looking like a healthy row.
        "samples": {cat: sizes.get(cat, (0, 0))[0] for cat in sorted(scores)},
        "scores": scores,
    }


def modes_comparable(baseline_mode: str, run_mode: str) -> bool:
    """Scores may only be compared within one, known retrieval mode."""
    if baseline_mode == MODE_UNKNOWN or run_mode == MODE_UNKNOWN:
        return False
    return baseline_mode == run_mode


def compute_scores(results: list[dict]) -> dict[str, float]:
    """Score only the questions that actually ran.

    Errored questions carry no "correct" key and are skipped entirely: a
    question the harness could not ask is not a question memory got wrong.
    """
    scored = [r for r in results if "correct" in r]
    by_type: dict[str, list[bool]] = {}
    for r in scored:
        by_type.setdefault(r["question_type"], []).append(bool(r["correct"]))
    scores: dict[str, float] = {}
    for qtype, corrects in sorted(by_type.items()):
        scores[qtype] = sum(corrects) / len(corrects) if corrects else 0.0
    if scored:
        all_correct = [bool(r["correct"]) for r in scored]
        scores["overall"] = sum(all_correct) / len(all_correct)
    return scores


def category_sample_sizes(results: list[dict]) -> dict[str, tuple[int, int]]:
    """Per category: (questions that produced a score, questions attempted).

    The error budget is checked over the WHOLE run, but every verdict below is
    per category. Without this the two never meet: a run can stay inside a 10%
    global budget while both dropped questions land in the same 4-question
    category, which is then scored on 2 answers and compared against a baseline
    captured on 4 — silently.
    """
    sizes: dict[str, list[int]] = {}
    for r in results:
        qtype = r.get("question_type")
        if qtype is None:
            continue
        row = sizes.setdefault(qtype, [0, 0])
        row[1] += 1
        if "correct" in r:
            row[0] += 1
    return {qtype: (ran, attempted) for qtype, (ran, attempted) in sizes.items()}


def category_comparable(ran: int, tolerance: float) -> bool:
    """Is a category's surviving sample still comparable to its baseline?

    The tolerance is calibrated as "one question's worth of variance on the
    4-questions-per-category CI subset" (= 0.25). That calibration is a
    statement about the DENOMINATOR: at n=4 a single flipped answer moves the
    score by exactly the tolerance, so a swing that large is absorbed rather
    than called a regression. Lose one question to a harness error and the
    quantum becomes 1/3 > 0.25 — now a single unlucky answer clears the
    tolerance on its own.

    That cuts both ways, and both ways are this gate's stated failure modes:
      * false RED — a harness BrokenResourceError, not the PR's code, produces
        EXIT_REGRESSION and blocks a merge the author cannot unblock;
      * false GREEN — a genuine 25% drop hides inside the widened noise while
        the table prints an unqualified score and "all categories within
        tolerance".

    So a category whose one-question quantum no longer fits inside the
    tolerance is not compared at all. Same rule as the mode gate, one level
    down: compare like with like, or do not compare.
    """
    if ran <= 0:
        return False
    return (1.0 / ran) <= tolerance + 1e-9


def baseline_sample_mismatches(
    scores: dict[str, float],
    sizes: dict[str, tuple[int, int]],
    baseline_samples: dict[str, int] | None,
) -> list[str]:
    """Categories this run measured on a different denominator than the baseline.

    `1.000` over 2 questions and `0.500` over 4 are not a 50% drop in recall
    quality; they are two different measurements. Same rule as the mode gate and
    the sample gate — compare like with like, or do not compare.
    """
    if not baseline_samples:
        return []
    return sorted(
        cat
        for cat, base_n in baseline_samples.items()
        if cat in scores and sizes.get(cat, (0, 0))[0] != base_n
    )


def unmeasured_detail(
    cat: str,
    sizes: dict[str, tuple[int, int]],
    baseline_samples: dict[str, int] | None,
) -> str:
    """One category's reason for not being compared, in its own terms.

    Two different causes land in the same `unmeasured` list and they are not
    interchangeable: "we lost questions to a harness error" is the PR author's
    infrastructure problem, "the baseline was captured on a different denominator"
    is the maintainer's re-snap. Reporting both as `n/m questions` sends whoever
    reads the log looking for the wrong fault.
    """
    ran, attempted = sizes.get(cat, (0, 0))
    base_n = (baseline_samples or {}).get(cat)
    if base_n is not None and ran != base_n:
        return f"{cat} ({ran} questions this run vs {base_n} in the baseline)"
    return f"{cat} ({ran}/{attempted} questions)"


def decide_verdict(
    scores: dict[str, float],
    sizes: dict[str, tuple[int, int]],
    baseline: dict[str, float],
    tolerance: float,
    baseline_samples: dict[str, int] | None = None,
) -> tuple[int, list[str], list[str]]:
    """The whole quality verdict, as a pure function: (exit code, regressions,
    unmeasured).

    Kept pure and separate from cmd_run on purpose. A test that re-implements
    this decision instead of calling it passes just as happily with the logic
    reverted, which makes it a decoration rather than a guard — the exact trap
    the cleanup test in test_gate_blindness.py fell into first time round.

    Precedence: REGRESSION > INCONCLUSIVE > OK. A measured, comparable category
    that got worse is a positive finding; partial blindness in another category
    does not erase it. The reverse order would let one flaky question suppress
    every merge block in the repo.
    """
    unmeasured = sorted(
        cat for cat in scores
        if not category_comparable(sizes.get(cat, (0, 0))[0], tolerance)
    )
    # Categories the baseline knows about that produced no score at all never
    # appear in `scores`, so iterating `scores` alone cannot see them — their
    # absence has to be caught from the baseline side.
    unmeasured += sorted(
        cat for cat in baseline
        if cat not in scores
        and not category_comparable(sizes.get(cat, (0, 0))[0], tolerance)
    )
    # A category can be perfectly well measured THIS run and still not be
    # comparable, because the operand on the other side of the `<` was measured on
    # a different number of questions. Caught here rather than left to produce a
    # confident verdict out of two incomparable numbers.
    unmeasured += [
        cat
        for cat in baseline_sample_mismatches(scores, sizes, baseline_samples)
        if cat not in unmeasured
    ]

    regressions = sorted(
        cat for cat, score in scores.items()
        if cat not in unmeasured and score < baseline.get(cat, 0.0) - tolerance
    )
    if regressions:
        return EXIT_REGRESSION, regressions, unmeasured
    if unmeasured:
        return EXIT_INCONCLUSIVE, regressions, unmeasured
    return EXIT_OK, regressions, unmeasured


def report_errors(results: list[dict]) -> list[dict]:
    return [r for r in results if r.get("error")]


def classify_error(detail: str, attempts_made: int, attempts_allowed: int) -> str:
    """TRANSIENT or PERSISTENT, decided from ONE run — no cross-run state.

    Two independent ways to earn PERSISTENT, and both are load-bearing:

    * the message does not match the transient predicate at all. The client does
      not retry these by construction, so the same input fails the same way next
      run — "deterministic" here is not a guess, it is the retry policy's own
      premise, read back;
    * the message DOES look transient, but the question spent every attempt it
      was allowed and each one failed. Four fresh mesh-mcp processes over ~50s
      of backoff all dying is not a blip whatever the string says. This is the
      arm that catches a permanent failure wearing a transient message — the one
      a "same ids as last run" comparison needs a previous run to see, and that
      this sees on the first.

    Exhausting the allowance only counts when there WAS an allowance: once the
    circuit breaker trips, `attempts_allowed` drops to 1 and using it up proves
    nothing about whether a retry would have helped.

    KNOWN LIMIT, stated rather than glossed: an outage lasting longer than the
    client's ~50s retry window (a slow rolling deploy, not a systemd bounce) can
    put a genuinely transient question through the exhaustion arm and label it
    persistent. It is bounded on both sides and cannot manufacture a verdict:
      * `--max-error-rate` is checked FIRST, so anything above 10% of the run
        exits `harness-errors` before reaching here;
      * the breaker withdraws retries after BREAKER_TRIP_AFTER questions, so at
        most that many can reach this arm during one outage;
      * with today's 4-question categories, a run that lost even ONE question is
        already INCONCLUSIVE — `category_comparable(3, 0.25)` is False. What such
        a run gets from this classifier is a different REASON KIND, not a
        different verdict. (Pinned by
        `test_a_single_lost_question_is_already_inconclusive_today`; if a future
        dataset gives categories more questions, that test fails and this
        paragraph stops being true.)
    """
    # Imported here, not at module scope: SCRIPT_DIR only joins sys.path inside
    # main(), and every other mesh_client_stdio import in this file is local for
    # the same reason.
    from mesh_client_stdio import is_transient_text

    if not is_transient_text(detail):
        return ERROR_PERSISTENT
    if attempts_allowed > 1 and attempts_made >= attempts_allowed:
        return ERROR_PERSISTENT
    return ERROR_TRANSIENT


def persistent_errors(errors: list[dict]) -> list[dict]:
    """The errored questions that will fail identically on the next run.

    An error with no `error_kind` (a result file written by an older run_ci) is
    NOT counted: this gate exists to name a specific, provable property, and
    inferring it from absence would manufacture alerts out of old artifacts.
    """
    return [r for r in errors if r.get("error_kind") == ERROR_PERSISTENT]


def persistent_verdict(rc: int, stuck: int, allowed: int) -> int:
    """Fold "questions are permanently unmeasured" into an existing verdict.

    A persistent error must never DOWNGRADE a regression. The whole repo ranks
    `REGRESSION > INCONCLUSIVE > OK` for one reason: a run that measured a real
    drop has to keep saying so. If a harness defect could demote that to
    INCONCLUSIVE, then — since these two questions failed on every run for six
    days — this gate would have stopped blocking bad memory PRs entirely, which
    is a worse hole than the one it is closing.

    Upward, though, it is decisive: a green verdict that quietly excluded the
    same questions for ever is the exact false green this card was filed about.
    """
    if stuck <= allowed:
        return rc
    if rc == EXIT_REGRESSION:
        return rc
    return EXIT_INCONCLUSIVE


def print_error_report(errors: list[dict], total: int) -> None:
    by_stage: dict[str, list[dict]] = {}
    for r in errors:
        by_stage.setdefault(r.get("error_stage", "unknown"), []).append(r)
    print(f"\n{len(errors)}/{total} questions did not run:")
    for stage, rows in sorted(by_stage.items()):
        # One representative message per stage — 24 copies of the same 402 is noise.
        sample = rows[0]["error"]
        qids = ", ".join(r["question_id"] for r in rows[:5])
        more = f" (+{len(rows) - 5} more)" if len(rows) > 5 else ""
        print(f"  [{stage}] {len(rows)}x — {qids}{more}")
        print(f"    {sample}")

    # Its own block, not a flag on a line inside the list above. The old report
    # printed the same two ids in every single run, and nothing on the line
    # distinguished them from a one-off 502 — which is precisely why six days of
    # readers scrolled past them.
    stuck = persistent_errors(errors)
    if stuck:
        print(
            f"\n  ⚠ {len(stuck)} of those are PERSISTENT — not infrastructure "
            "noise. They will fail identically on the next run:"
        )
        for r in stuck:
            print(f"      {r['question_id']} — {r['error']}")
        print(
            "    They are excluded from the scores like any other error, which "
            "means those questions are NOT BEING MEASURED AT ALL, run after run. "
            "The error budget is calibrated for transient failures and will "
            "never clear this: fix the harness or the dataset."
        )


def print_table(
    scores: dict[str, float],
    baseline: dict[str, float] | None,
    tolerance: float,
    sizes: dict[str, tuple[int, int]] | None = None,
    baseline_samples: dict[str, int] | None = None,
) -> None:
    # `Sample` is not decoration. A bare "1.000" reads as "4 of 4 questions
    # passed" when it can equally mean "the 2 questions that survived passed";
    # the number that decides whether the row means anything was the one thing
    # the table never printed.
    sizes = sizes or {}
    header = f"{'Category':<35} {'Score':>7} {'Sample':>8}"
    if baseline:
        header += f" {'Baseline':>9} {'Delta':>8} {'Status':>10}"
    print(header)
    print("-" * (len(header) + 4))
    for cat, score in sorted(scores.items()):
        ran, attempted = sizes.get(cat, (0, 0))
        sample = f"{ran}/{attempted}" if attempted else "-"
        line = f"{cat:<35} {score:>7.3f} {sample:>8}"
        if baseline:
            base = baseline.get(cat, 0.0)
            delta = score - base
            base_n = (baseline_samples or {}).get(cat)
            if attempted and not category_comparable(ran, tolerance):
                status = "⚠ UNMEASURED"
            elif base_n is not None and ran != base_n:
                # The row's own delta is meaningless: the two operands were
                # measured on different denominators. Never print it as a ✓ or an ✗.
                status = f"⚠ vs n={base_n}"
            elif score >= base - tolerance:
                status = "✓"
            else:
                status = "✗ REGRESS"
            line += f" {base:>9.3f} {delta:>+8.3f} {status:>10}"
        print(line)


def cmd_run(args: argparse.Namespace) -> int:
    _load_env()

    retrieval_only = args.retrieval_only
    baseline_file = RETRIEVAL_BASELINE_FILE if retrieval_only else BASELINE_FILE

    # Validate required env
    mesh_url = _require_env("MESH_API_URL")
    _require_env("MESH_AGENT_KEY")
    top_k = int(os.environ.get("LME_TOP_K", "10"))
    tolerance = args.tolerance

    chat_client = judge_client = None
    chat_model = judge_model = ""

    if retrieval_only:
        # No LLM is constructed at all — the recall gate must not acquire a paid
        # dependency by accident.
        dataset = json.loads(DATA_FILE.read_text())
        print(f"Loaded {len(dataset)} questions from {DATA_FILE.name}")
        print(f"Mesh: {mesh_url}")
        print(f"Mode: retrieval-only (recall@{top_k} vs answer_session_ids, no LLM)")
    else:
        judge_key = _require_env("LME_JUDGE_API_KEY")
        judge_base_url = os.environ.get("LME_JUDGE_BASE_URL", "https://openrouter.ai/api/v1")
        judge_model = os.environ.get("LME_JUDGE_MODEL", "openai/gpt-4o-mini")
        chat_key = os.environ.get("LME_CHAT_API_KEY", judge_key)
        chat_base_url = os.environ.get("LME_CHAT_BASE_URL", judge_base_url)
        chat_model = os.environ.get("LME_CHAT_MODEL", judge_model)

        from openai import OpenAI  # type: ignore
        chat_client = OpenAI(api_key=chat_key, base_url=chat_base_url)
        judge_client = OpenAI(api_key=judge_key, base_url=judge_base_url)

        dataset = json.loads(DATA_FILE.read_text())
        print(f"Loaded {len(dataset)} questions from {DATA_FILE.name}")
        print(f"Mesh: {mesh_url}")
        print(f"Judge: {judge_model} @ {judge_base_url}")
        print(f"Chat: {chat_model}")
    print()

    results: list[dict] = []
    for i, entry in enumerate(dataset, start=1):
        qid = entry["question_id"]
        qtype = entry["question_type"]
        print(f"[{i:02d}/{len(dataset)}] {qid} ({qtype})", end=" ", flush=True)
        t0 = time.monotonic()
        r = run_single(
            entry,
            chat_client=chat_client,
            chat_model=chat_model,
            judge_client=judge_client,
            judge_model=judge_model,
            top_k=top_k,
            retrieval_only=retrieval_only,
        )
        elapsed = time.monotonic() - t0
        status = "!" if r.get("error") else ("✓" if r.get("correct") else "✗")
        print(f"{status} ({elapsed:.1f}s)")
        results.append(r)

    errors = report_errors(results)
    ran = len(results) - len(errors)
    error_rate = len(errors) / len(results) if results else 1.0

    scores = compute_scores(results)
    sizes = category_sample_sizes(results)
    # "overall" is a real row in `scores`, so it needs a denominator too — but it
    # is pooled across every category, so its quantum is 1/22, not 1/2. It stays
    # comparable exactly when the global error budget above says it does.
    sizes["overall"] = (ran, len(results))
    run_mode = resolve_run_search_mode(results)

    baseline: dict[str, float] | None = None
    baseline_mode = MODE_UNKNOWN
    baseline_samples: dict[str, int] = {}
    if baseline_file.exists():
        baseline, baseline_mode = load_baseline(baseline_file)
        baseline_samples = load_baseline_samples(baseline_file)

    title = "Recall Gate Results" if retrieval_only else "LongMemEval-S Results"
    print()
    print("=" * 70)
    print(f"{title}  ({ran}/{len(results)} questions ran)")
    print(f"Search mode served: {run_mode}    Baseline mode: {baseline_mode}")
    print("=" * 70)
    print_table(scores, baseline, tolerance, sizes, baseline_samples)
    print("=" * 70)

    if errors:
        print_error_report(errors, len(results))

    # INCONCLUSIVE takes precedence over every quality verdict below. A run that
    # could not execute has measured nothing — reporting it as a regression (or,
    # worse, as a pass) is a lie either way.
    if error_rate > args.max_error_rate:
        print(
            f"\n⚠ EVAL INCONCLUSIVE — {len(errors)}/{len(results)} questions could not run "
            f"({error_rate:.0%} > max {args.max_error_rate:.0%}). "
            "This is an infrastructure failure, NOT a memory-quality regression. "
            "No verdict was produced; fix the harness dependency above and re-run."
        )
        print(
            f"{REASON_PREFIX} {REASON_HARNESS_ERRORS} — "
            f"{len(errors)}/{len(results)} questions could not run"
        )
        return EXIT_INCONCLUSIVE

    if args.update_baseline:
        if retrieval_only:
            # The recall baseline is MODE-SCOPED: it records the retrieval mode it
            # was captured in, because hit@k in bm25-only mode is not comparable to
            # hit@k in hybrid mode. A baseline without a mode can only ever produce
            # INCONCLUSIVE.
            payload = retrieval_baseline_payload(
                scores,
                sizes,
                run_mode,
                top_k,
                time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            )
            baseline_file.write_text(json.dumps(payload, indent=2) + "\n")
            print(f"\nBaseline updated: {baseline_file}  (search_mode={run_mode})")
            if run_mode != MODE_HYBRID:
                print(
                    f"⚠ Captured in mode '{run_mode}', not '{MODE_HYBRID}'. "
                    "If the embedder is merely down, this baseline pins a DEGRADED "
                    "quality level — re-snap it once the embedder is healthy."
                )
        else:
            baseline_file.write_text(json.dumps(scores, indent=2) + "\n")
            print(f"\nBaseline updated: {baseline_file}")
        return EXIT_OK

    if baseline is None:
        # A pass against no baseline compares against NOTHING. Calling that green
        # would make an unenforcing gate look healthy — exactly the blindness this
        # gate exists to remove. It is inconclusive, and it says so out loud.
        print(
            f"\n⚠ EVAL INCONCLUSIVE — no {baseline_file.name} found: this run was "
            "compared against nothing and enforced nothing. Snap a baseline with "
            "--update-baseline (against a healthy embedder) before relying on this gate."
        )
        print(f"{REASON_PREFIX} {REASON_NO_BASELINE} — {baseline_file.name} does not exist")
        return EXIT_INCONCLUSIVE if retrieval_only else EXIT_OK

    # ── Mode gate: compare like with like, or do not compare at all ───────────
    # This check sits BEFORE any score comparison and can only ever produce
    # EXIT_INCONCLUSIVE. A cross-mode score drop is a statement about the
    # embedder's health, not about this PR's code, and must never return
    # EXIT_REGRESSION: a required check that goes red on an infra outage the
    # author cannot fix blocks every PR in the repo and gets bypassed.
    if retrieval_only and not modes_comparable(baseline_mode, run_mode):
        if MODE_UNKNOWN in (baseline_mode, run_mode):
            reason = REASON_MODE_UNKNOWN
            detail = (
                f"retrieval mode is unknown (baseline='{baseline_mode}', run='{run_mode}'). "
                "Either the baseline predates mode-scoping or the server does not report "
                "`search_mode` — nothing here is safely comparable."
            )
        else:
            reason = REASON_MODE_MISMATCH
            detail = (
                f"baseline captured in mode '{baseline_mode}' but this run served "
                f"'{run_mode}'"
                + (
                    " (prod embedder degraded)"
                    if run_mode == MODE_BM25_ONLY
                    else " (embedder recovered)"
                )
                + ". Scores across modes are not comparable — this is NOT a code "
                "regression. Re-snap the baseline once the embedder is healthy: "
                "`python run_ci.py --retrieval-only --update-baseline`."
            )
        print(f"\n⚠ INCONCLUSIVE: {detail}")
        print(
            "The recall safety net is BLIND for this run: no comparison was made, "
            "so a real regression could pass through unseen."
        )
        print(f"{REASON_PREFIX} {reason} — baseline='{baseline_mode}' run='{run_mode}'")
        return EXIT_INCONCLUSIVE

    if errors:
        stuck_note = persistent_errors(errors)
        budget = (
            f" ({len(errors) - len(stuck_note)} within the allowed error budget, "
            f"{len(stuck_note)} PERSISTENT — see below)"
            if stuck_note
            else " (within the allowed error budget)"
        )
        print(
            f"\nNote: {len(errors)} question(s) errored and were EXCLUDED from the "
            f"scores above{budget}."
        )

    # ── Sample gate: a category is only judged on a sample it can be judged on ─
    # Categories that lost questions to harness errors are set aside BEFORE the
    # comparison, so a dropped question can neither manufacture a regression nor
    # be laundered into a pass. See category_comparable() for why 1/n vs the
    # tolerance is the right line, and decide_verdict() for the precedence.
    rc, regressions, unmeasured = decide_verdict(
        scores, sizes, baseline, tolerance, baseline_samples
    )
    mismatched = [
        cat
        for cat in baseline_sample_mismatches(scores, sizes, baseline_samples)
        if cat in unmeasured
    ]
    # Folded in AFTER decide_verdict, deliberately: a question that fails
    # identically every run falls outside the error budget's premise, but it must
    # only ever raise OK to INCONCLUSIVE — never lower a REGRESSION. See
    # persistent_verdict().
    stuck = persistent_errors(errors)
    persistent_blind = len(stuck) > args.max_persistent_errors
    rc = persistent_verdict(rc, len(stuck), args.max_persistent_errors)

    # `rc` is returned verbatim below: the exit code comes from decide_verdict and
    # persistent_verdict and nowhere else, so the reporting here cannot drift out
    # of step with the precedence the tests pin.
    if unmeasured:
        detail = ", ".join(
            unmeasured_detail(cat, sizes, baseline_samples) for cat in unmeasured
        )
        # What SURVIVED matters as much as what did not. `category-unmeasured` is
        # partial by construction — decide_verdict ranks REGRESSION above
        # INCONCLUSIVE, so every comparable category is still enforced and still
        # blocks. Saying "nothing was measured" when 6 of 7 categories were is how
        # a banner earns the discount it then gets applied at the moment it is
        # telling the truth.
        enforced = sorted(c for c in scores if c not in unmeasured)
        total = len(set(scores) | set(unmeasured))
        print(
            f"\n⚠ {len(unmeasured)} of {total} category(ies) could not be compared "
            f"against the baseline: {detail}."
        )
        if enforced:
            print(
                f"The other {len(enforced)} WERE compared and enforced normally "
                f"({', '.join(enforced)}) — a regression in any of them still fails "
                "this check. The blind spot is limited to the categories named above."
            )
        else:
            print(
                "NO category could be compared: this run enforced nothing at all, so "
                "a memory regression anywhere would pass through it unseen."
            )
        if mismatched:
            print(
                "Cause for "
                f"{', '.join(mismatched)}: the baseline was captured on a different "
                "number of questions than this run measured. That is a sample "
                "change, NOT a quality change, and it is the maintainer's to clear: "
                "re-snap with `python run_ci.py --retrieval-only --update-baseline` "
                "against a healthy embedder."
            )
        if [c for c in unmeasured if c not in mismatched]:
            print(
                "Cause for the rest: questions were lost to a harness error above, "
                "not to this PR's code — the fix is to stop losing questions, not to "
                "lower the bar."
            )

    if regressions:
        print(f"\n✗ REGRESSION detected in: {', '.join(regressions)}")
        if persistent_blind:
            # Said out loud rather than swallowed: the verdict is correct and
            # still blocks, but the reader deserves to know it was reached with
            # questions missing that will be missing again tomorrow.
            print(
                f"({len(stuck)} question(s) also failed PERSISTENTLY and were "
                "excluded — the regression verdict stands, and those questions "
                "remain unmeasured.)"
            )
    elif persistent_blind:
        qids = ", ".join(r["question_id"] for r in stuck)
        print(
            f"\n⚠ EVAL INCONCLUSIVE — {len(stuck)}/{len(results)} question(s) fail "
            f"PERSISTENTLY: {qids}. These are not transient infrastructure errors; "
            "they will fail identically on the next run, so those questions are "
            "permanently unmeasured. The error budget deliberately does NOT absorb "
            "them: a per-run percentage cannot express 'the same 8% for ever'."
        )
        print(
            f"{REASON_PREFIX} {REASON_PERSISTENT_ERRORS} — "
            f"{len(stuck)}/{len(results)} question(s) fail every run ({qids})"
        )
    elif unmeasured:
        print(
            "\n⚠ EVAL INCONCLUSIVE — the recall safety net did not cover every "
            "category."
        )
        # Kind selected by CAUSE, not by which branch printed it: the workflow
        # dedups its alert on the kind, and the two causes need two different
        # alerts (see REASON_BASELINE_SAMPLE_MISMATCH). A run with both reports the
        # mismatch, because that one is actionable by the person reading the alert.
        kind = (
            REASON_BASELINE_SAMPLE_MISMATCH if mismatched
            else REASON_CATEGORY_UNMEASURED
        )
        print(f"{REASON_PREFIX} {kind} — {detail}")
    else:
        print("\n✓ All categories within tolerance.")

    return rc


def main() -> int:
    logging.basicConfig(level=logging.WARNING, format="%(levelname)s: %(message)s")
    parser = argparse.ArgumentParser(description="LongMemEval-S CI regression gate")
    parser.add_argument(
        "--update-baseline",
        action="store_true",
        help="Run benchmark and write results to baseline.json (Mac Mini only)",
    )
    parser.add_argument(
        "--tolerance",
        type=float,
        default=0.25,
        help="Allowed per-category regression below baseline (default: 0.25 — 1-question variance on 4q/category CI subset)",
    )
    parser.add_argument(
        "--retrieval-only",
        action="store_true",
        help="Score recall@k against answer_session_ids. No LLM, no paid API — the gateable arm.",
    )
    parser.add_argument(
        "--max-error-rate",
        type=float,
        default=0.10,
        help=(
            "Fraction of questions allowed to error before the run is declared "
            "INCONCLUSIVE (exit 2) instead of scored (default: 0.10). Errored "
            "questions are always excluded from the scores, never counted wrong. "
            "Governs TRANSIENT errors only — see --max-persistent-errors."
        ),
    )
    parser.add_argument(
        "--max-persistent-errors",
        type=int,
        default=0,
        help=(
            "How many questions may fail PERSISTENTLY (a non-transient error, or "
            "a transient-looking one that exhausted every retry) before the run "
            "is declared INCONCLUSIVE (exit 2). Default 0: a question that fails "
            "identically every run is permanently unmeasured, and no per-run "
            "percentage can express that. Never downgrades a REGRESSION."
        ),
    )
    args = parser.parse_args()

    # Ensure script dir is on path for mesh_client_stdio import
    sys.path.insert(0, str(SCRIPT_DIR))

    return cmd_run(args)


if __name__ == "__main__":
    raise SystemExit(main())
