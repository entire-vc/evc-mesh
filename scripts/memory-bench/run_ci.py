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

Every rule here applies to BOTH arms. The mode gate and the missing-baseline
check used to be conditioned on `retrieval_only`, so they guarded the required
arm and skipped the advisory one — which then compared a `hybrid` run against a
legacy mode-less baseline and printed `✗ REGRESSION` for it on most nights. An
invalid comparison is invalid regardless of which arm makes it; "advisory" is a
statement about who the verdict blocks, not a licence to publish a verdict that
cannot be true.

A baseline is a claim about a DISTRIBUTION, not a sample:
  This eval answers 4 questions per category with a chat model and grades them
  with an LLM judge, so one flipped question moves a category by 0.25 with no
  code change at all — and `single-session-assistant` was observed at 1.000,
  0.250 and 1.000 on three consecutive nightlies of identical code. A baseline
  snapped from ONE run therefore records a coin toss, and the one it replaced
  happened to land on the MAXIMUM ever observed in 6 of 7 categories.
  So: `--update-baseline --repeat N` captures the MEAN of N passes and records
  the observed per-category `spread` beside it; the verdict threshold is
  `baseline - max(tolerance, spread)`. Where that threshold falls to zero the
  category is reported as "no verdict possible" with its reason, never as a ✓ —
  a category that cannot fail must not be counted as one that passed.

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

Usage (baseline generation — capture it in the SAME environment that will be
compared against it: same runner, same agent key, same chat/judge models):
  python run_ci.py --update-baseline --repeat 3        # LLM baseline (paid × 3)
  python run_ci.py --retrieval-only --update-baseline  # recall baseline (free)

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
  RECALL_GRAPH_ENABLED — Pass to mesh-mcp (default: false; the bench workflow
                        sets it to 'true'). It costs `graphBoostReserve(limit)`
                        = limit/4 of the page, so the retrieval window is
                        limit*3/4 — read `rows_returned` against that ceiling,
                        not against the limit. See `_mesh_env` in
                        mesh_client_stdio.py.
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
from typing import Any, NamedTuple

logger = logging.getLogger(__name__)

SCRIPT_DIR = Path(__file__).resolve().parent
DATA_FILE = SCRIPT_DIR / "data" / "lme_s_24.json"
BASELINE_FILE = SCRIPT_DIR / "baseline.json"
RETRIEVAL_BASELINE_FILE = SCRIPT_DIR / "baseline_retrieval.json"
# The recall gate runs in two ARMS against two different systems (ADR-0003):
#
#   prod    — the deployed server. Answers "is what we shipped still good?"
#   branch  — `cmd/api` built from the PR head, on an ephemeral database with a
#             local CPU embedder. Answers "does THIS CHANGE make recall worse?"
#             This is the arm behind the required check.
#
# Their scores are NOT comparable — different embedder, different corpus, empty
# vs accumulated database — so they get separate baseline files and the arm is
# recorded INSIDE each file. A baseline copied into the wrong arm is the one
# failure here that would otherwise produce a confident, entirely fictional
# verdict, which is the same shape of fault this whole ADR exists to close.
BRANCH_RETRIEVAL_BASELINE_FILE = SCRIPT_DIR / "baseline_retrieval_branch.json"
ARM_PROD = "prod"
ARM_BRANCH = "branch"
# Per-question results. `results/` has existed since the gate was created but
# nothing ever wrote to it: the only artifact was gate.log, whose per-question
# line carries a tick or a cross and nothing else. That is why answering "where
# did gold rank?" needed a live prod probe rather than a download.
RESULTS_DIR = SCRIPT_DIR / "results"
RETRIEVAL_RESULTS_FILE = RESULTS_DIR / "recall_gate.json"
E2E_RESULTS_FILE = RESULTS_DIR / "longmemeval.json"
RESULTS_SCHEMA_VERSION = 1

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

# What the DENSE arm actually returned, which the mode above cannot express.
# MODE_HYBRID is set when the dense arm RAN end-to-end — embedder alive, query
# vectorised, VectorSearch returned no error. It is silent on whether the arm
# matched a single row, and those are different states: with every
# `memories.embedding` NULL, VectorSearch matches nothing corpus-wide and Mesh
# still answers `search_mode: hybrid`, `degraded: false`. That is not
# hypothetical — run 30316983402 scored `overall 0.9583`, the best ever recorded,
# on a haystack its dense arm could not see, because every fixture had been
# written after the chunked-embed deploy.
DENSE_ARM_SERVED = "served"    # at least one hybrid recall returned dense rows
DENSE_ARM_EMPTY = "empty"      # every hybrid recall that reported returned zero
DENSE_ARM_UNKNOWN = "unknown"  # nothing reported it — an older server, not a finding

# Machine-readable reason kinds, grepped out of the log by the CI workflow so it
# can raise ONE out-of-band alert per reason instead of one per PR.
REASON_NO_BASELINE = "no-baseline"
REASON_MODE_MISMATCH = "mode-mismatch"
REASON_MODE_UNKNOWN = "mode-unknown"
REASON_HARNESS_ERRORS = "harness-errors"
REASON_NO_ELIGIBLE_CATEGORY = "no-eligible-category"
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
# The baseline file on disk was captured in a DIFFERENT arm than this run. Its
# own kind because the fix is neither "re-snap" nor "stop losing questions": it
# is "you are holding the wrong file", and a run that continues past it produces
# a numerically valid comparison between two unrelated systems.
REASON_ARM_MISMATCH = "arm-mismatch"
# A capture that could not be trusted is refused BEFORE the write, so the previous
# baseline survives. Its own kind: "we declined to record a floor" is not an
# inconclusive verdict, and it has a different reader (whoever dispatched it).
REASON_CAPTURE_REFUSED = "capture-refused"
# Its own kind, and deliberately NOT folded into `mode-mismatch`. Both say "this
# run is not comparable", but they have different owners and different fixes: a
# mode mismatch is the embedder being down (wait for it, or re-snap), while an
# empty dense arm is the server reporting `hybrid` for an arm that matched
# nothing — a data or write-path defect that no re-snap fixes and that a re-snap
# would in fact PIN as the new floor.
REASON_DENSE_ARM_EMPTY = "dense-arm-empty"
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

    Stores are keyed `bench-<qid>-s<idx>` and tagged `session-<idx>` by
    mesh_client_stdio; either is enough to identify the session.
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


def gold_rank(ranked: list[dict], gold: set[int]) -> int | None:
    """1-based rank of the first gold session in the FULL ranked recall.

    `ranked` is the complete tag-filtered candidate list as it reached the
    client — NOT the top_k window that `hit` is scored on — so a gold session
    that lands just outside the cut still gets a number. That distinction is
    the whole point of the field: `hit@10 = 0` reads identically whether gold
    ranked 12th or was never retrieved, and those have different causes and
    different fixes.

    Returns None when no gold row appears anywhere in `ranked`. None is
    deliberately neither 0 nor top_k: 1-based ranks keep 0 impossible, so a
    consumer can test `gold_rank is None` without it colliding with a real
    position, and a sentinel equal to k would be indistinguishable from
    "ranked last inside the window".

    CAVEAT worth knowing before reading None as "not indexed": `ranked` is
    what SURVIVED to the client. `scope`/`tags_any` are post-filters over a
    workspace-wide candidate pool (#2c087b2a), so a fixture can be indexed
    perfectly and still never arrive. Read `gold_rank` together with
    `rows_returned` vs `haystack_size` — when rows_returned < haystack_size
    the pool truncated, and None means "did not reach the client", not
    "absent from the index".

    Reuses `retrieved_session_indices` rather than reimplementing the
    key/tag -> session-index mapping, so this cannot drift from what the gate
    scores as a hit.
    """
    if not gold:
        return None
    for pos, item in enumerate(ranked, start=1):
        if gold & retrieved_session_indices([item]):
            return pos
    return None


def retrieval_observability(entry: dict, client: Any) -> dict:
    """The retrieval fields both arms carry alongside their verdict.

    Purely additive — nothing here feeds scoring, thresholds or the pass/fail
    decision. It exists so that "recall missed" can be read out of an artifact
    instead of reconstructed by probing prod.

    `haystack_size` is included because `rows_returned` alone is not
    interpretable: 32 is meaningless until you know it is 32 of 45, and the
    ratio is what shows the candidate pool truncating.

    `dense_rows` is the ONE field here that is not purely advisory — it feeds
    `resolve_dense_arm_status`, which can take the run INCONCLUSIVE. It is
    reported per question rather than per run because it is read off a per-recall
    envelope, and `None` (server too old to report it) has to survive as `None`
    all the way to the resolver rather than being flattened into a run-level 0.
    """
    return {
        "gold_rank": gold_rank(
            getattr(client, "ranked_records", []), gold_session_indices(entry)
        ),
        "rows_returned": getattr(client, "rows_returned", None),
        "haystack_size": len(entry.get("haystack_sessions") or []),
        "dense_rows": getattr(client, "dense_rows", None),
        "sparse_rows": getattr(client, "sparse_rows", None),
    }


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
            **retrieval_observability(entry, client),
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
        # Also on the advisory arm: it runs the same recall, so it has the same
        # blind spot, and a wrong answer whose evidence ranked 12th is a
        # different bug from one whose evidence was never retrieved.
        **retrieval_observability(entry, client),
    }


def format_rank_suffix(result: dict) -> str:
    """`  rank=12/32 of 45` for the per-question log line, or "" when unmeasured.

    Empty for errored questions: they never ran a recall, so any rank shown
    would be a fabrication rather than a measurement.
    """
    if result.get("error") or "rows_returned" not in result:
        return ""
    rows = result.get("rows_returned")
    if rows is None:
        return ""
    rank = result.get("gold_rank")
    haystack = result.get("haystack_size")
    rank_s = "none" if rank is None else str(rank)
    tail = f" of {haystack}" if haystack else ""
    return f"  rank={rank_s}/{rows}{tail}"


def write_results_artifact(
    path: Path,
    results: list[dict],
    *,
    retrieval_only: bool,
    run_mode: str,
    top_k: int,
    repeat: int,
    scores: dict[str, float],
    sizes: dict[str, tuple[int, int]],
    dense_arm: str = DENSE_ARM_UNKNOWN,
) -> Path | None:
    """Write the per-question results the run just produced.

    Additive by construction: every key a question dict already carried is
    written through verbatim, so a consumer reading `question_id` / `correct`
    / `search_mode` / `error*` keeps working. Nothing here is read back by the
    gate — scoring, thresholds and the exit code are decided before this is
    called, and a failure to write is reported but never changes the verdict.
    An observability artifact must not be able to fail a required check.
    """
    payload = {
        "schema_version": RESULTS_SCHEMA_VERSION,
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "retrieval_only": retrieval_only,
        "search_mode": run_mode,
        # Recorded next to search_mode because reading one without the other is
        # what made run 30316983402's `hybrid`/0.9583 look like a record instead
        # of a blind run. Anyone downloading this artifact should be able to see
        # both without re-deriving one from the per-question rows.
        "dense_arm": dense_arm,
        "top_k": top_k,
        "repeat": repeat,
        "scores": scores,
        "sample_sizes": {k: list(v) for k, v in sizes.items()},
        "questions": results,
    }
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(payload, indent=2, default=str) + "\n")
    except OSError as exc:
        logger.error("could not write results artifact %s: %s", path, exc)
        return None
    return path


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


def resolve_dense_arm_status(results: list[dict]) -> str:
    """Did the dense arm actually return anything, across the whole run?

    Only questions served in HYBRID mode are evidence. A bm25-only question has
    no dense arm by definition — counting its zero would report "dense arm empty"
    on every deployment that runs without an embedder, which is a legitimate
    configuration and not a defect. The run-level mode gate already owns that
    case, and duplicating it here would just alert twice on one cause.

    BACK-COMPAT IS THE LOAD-BEARING PART. A server too old to report `dense_rows`
    yields None on every question, so nothing is evidence, so the status is
    UNKNOWN and the verdict is untouched. It has to work that way round: if a
    missing field read as 0, this gate would take the prod arm INCONCLUSIVE from
    the moment it merged until the moment the server was deployed — i.e. it would
    break exactly during the window it was written to fix, and the pressure would
    be to revert it rather than to finish the rollout.

    EMPTY requires that EVERY reporting hybrid question returned zero, not that
    one did. The failure this detects is corpus-wide by nature: the vector arm
    draws from a relevance-neutral candidate pool, so it returns rows for any
    query as long as anything in the workspace carries an embedding at all. One
    zero among non-zeros would therefore be a per-query oddity, not a lost arm,
    and taking a required check inconclusive on it would be a false alarm on a
    gate whose credibility is its only enforcement mechanism.
    """
    # `not isinstance(v, bool)`: bool subclasses int in Python, so a stray
    # `dense_rows: True` would otherwise count as the integer 1 and report a
    # healthy arm off a type error.
    reported = [
        r["dense_rows"]
        for r in results
        if r.get("search_mode") == MODE_HYBRID
        and isinstance(r.get("dense_rows"), int)
        and not isinstance(r.get("dense_rows"), bool)
    ]
    if not reported:
        return DENSE_ARM_UNKNOWN
    if any(n > 0 for n in reported):
        return DENSE_ARM_SERVED
    return DENSE_ARM_EMPTY


class Baseline(NamedTuple):
    """A parsed baseline file.

    `n_runs` and `spread` are what make a baseline honest about its own
    precision. A baseline captured from one pass records `n_runs=1` and an empty
    spread, and the gate then has no idea how much of any delta is judge noise —
    so it says so out loud instead of ruling on it.
    """

    scores: dict[str, float]
    search_mode: str
    n_runs: int
    spread: dict[str, float]
    # {category: (questions that scored, questions attempted)} at capture time.
    # Empty for any baseline written before the field existed — which must read
    # as "unknown denominator", never as "the same denominator as this run".
    sample_sizes: dict[str, tuple[int, int]] = {}
    # Which arm captured this file (ADR-0003). Empty for any baseline written
    # before the field existed, which must read as "unstated", never as "the
    # same arm as this run" — the whole point is that an unlabelled file cannot
    # vouch for where it came from.
    arm: str = ""


def load_baseline(path: Path) -> Baseline:
    """Read a baseline file.

    Two shapes are accepted:

      new  {"search_mode": "...", "captured_at": "...", "top_k": 10,
            "n_runs": 3, "scores": {...}, "spread": {...}}
      old  {"<category>": 0.75, ...}            — flat, pre-search_mode

    The old shape carries no mode, so it resolves to UNKNOWN, which makes every
    comparison against it INCONCLUSIVE rather than a coin-flip between a fake
    regression and a fake pass. Re-snap with --update-baseline to fix.

    `n_runs` defaults to 1 and `spread` to empty: a new-shape baseline written
    before those fields existed is a single sample and must be read as one, not
    silently credited with a precision it never had.
    """
    raw = json.loads(path.read_text())
    if isinstance(raw.get("scores"), dict):
        return Baseline(
            scores={k: float(v) for k, v in raw["scores"].items()},
            search_mode=str(raw.get("search_mode") or MODE_UNKNOWN),
            n_runs=int(raw.get("n_runs") or 1),
            spread={k: float(v) for k, v in (raw.get("spread") or {}).items()},
            sample_sizes={
                k: (int(v[0]), int(v[1]))
                for k, v in (raw.get("sample_sizes") or {}).items()
            },
            arm=str(raw.get("arm") or ""),
        )
    return Baseline(
        scores={k: float(v) for k, v in raw.items()},
        search_mode=MODE_UNKNOWN,
        n_runs=1,
        spread={},
        sample_sizes={},
        arm="",
    )


def build_baseline_payload(
    per_pass_scores: list[dict[str, float]],
    run_mode: str,
    top_k: int,
    sizes: dict[str, tuple[int, int]] | None = None,
    arm: str = ARM_PROD,
    served_commit: str = "",
) -> dict[str, Any]:
    """Aggregate N passes' scores into one mode-scoped baseline payload.

    BOTH arms write this shape. The advisory arm used to write a bare flat
    `{category: score}` dict, which `load_baseline` can only read back as
    MODE_UNKNOWN — so `--update-baseline` on that arm could never produce a
    comparable baseline no matter how many times it was run, and the mode gate
    it feeds would have had nothing to compare for ever.

    The baseline is the MEAN over the passes, and the observed max−min per
    category is recorded next to it as `spread`.

    `sample_sizes` records the DENOMINATOR each baseline figure was measured on.
    Without it a baseline/run sample mismatch is undetectable in the only
    direction that matters here: the sample gate catches a RUN whose sample
    shrank, but nothing catches a BASELINE captured on a smaller one. That is
    not hypothetical — `baseline_retrieval.json`'s `temporal-reasoning: 1.0`
    rests on 2 of that category's 4 questions (evc-mesh#362), and every run
    since has been scored against it as though it were 4. A baseline that does
    not state its own denominator cannot be checked for comparability at all.
    """
    sizes = sizes or {}
    categories = sorted({c for s in per_pass_scores for c in s})
    scores: dict[str, float] = {}
    spread: dict[str, float] = {}
    for cat in categories:
        vals = [s[cat] for s in per_pass_scores if cat in s]
        scores[cat] = sum(vals) / len(vals)
        spread[cat] = max(vals) - min(vals)
    payload: dict[str, Any] = {
        "search_mode": run_mode,
        # Which system produced these numbers (ADR-0003). `search_mode` already
        # scopes the baseline to a retrieval mode; this scopes it to a target,
        # which is the axis #b052cdda got wrong.
        "arm": arm,
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "top_k": top_k,
        "n_runs": len(per_pass_scores),
        "scores": scores,
        "spread": spread,
    }
    if served_commit:
        # WHICH BINARY produced these numbers. `arm` says which system, `search_mode`
        # says which retrieval path — neither says which build, and this file becomes
        # the standing floor for a required check. Capture 30336693957 is the case:
        # prod swapped 5d2cda8 -> ba7deae 42 min into a 65-min capture and the written
        # baseline recorded nothing about it, so the only way to learn what it measured
        # was for a human to go and diff two commits by hand afterwards. A baseline
        # that does not state its own denominator cannot be checked for comparability;
        # neither can one that does not state its own binary.
        payload["commit"] = served_commit
    if sizes:
        payload["sample_sizes"] = {
            cat: list(sizes[cat]) for cat in categories if cat in sizes
        }
    return payload


def capture_blockers(
    sizes: dict[str, tuple[int, int]],
    run_mode: str,
    retrieval_only: bool,
    dense_status: str = DENSE_ARM_UNKNOWN,
) -> list[str]:
    """Reasons this run must NOT be written as a baseline. Empty list = sound.

    Only the RETRIEVAL arm is gated this hard, and the asymmetry is deliberate.

    Its baseline feeds a REQUIRED check, so every figure in it becomes a merge
    threshold. A figure measured on a smaller sample than the runs it will judge
    does not merely lose precision — it silently converts a *sample* change into
    a REGRESSION verdict against code that did nothing wrong. That is not a
    hypothetical: `baseline_retrieval.json` pinned `temporal-reasoning: 1.0` on
    2 of that category's 4 questions (evc-mesh#362) and went unnoticed for six
    days, because 2 failures out of 24 is 8.3% — just under the 10%
    `--max-error-rate` that would otherwise have stopped the run. A warning
    printed into a capture log nobody reads twice is not a guard.

    The advisory arm keeps #363's warn-and-write behaviour. Its baseline blocks
    no merge, it is judged by a nondeterministic model, and each pass costs real
    money — refusing a capture there would trade a flawed baseline for no
    baseline at all, which is strictly worse for an arm whose whole job is to
    produce a trend.
    """
    if not retrieval_only:
        return []
    blockers: list[str] = []
    if run_mode != MODE_HYBRID:
        blockers.append(
            f"served in retrieval mode '{run_mode}', not '{MODE_HYBRID}' — this "
            "would pin a required check at a DEGRADED quality level, and every "
            "healthy run afterwards would be compared across modes"
        )
    # The mode check above cannot see this one: the run IS 'hybrid', and that is
    # the problem. Capturing here records a floor measured with no dense arm, and
    # since a later healthy run only ever scores at or above it, the gate would
    # then be permanently green AND permanently blind — the failure mode this
    # whole harness exists to remove, installed as the baseline. Default is
    # UNKNOWN, so a server that does not report `dense_rows` never blocks a
    # capture on a check it cannot perform.
    if dense_status == DENSE_ARM_EMPTY:
        blockers.append(
            f"served in mode '{MODE_HYBRID}' but the dense arm returned ZERO rows on "
            "every question — the embedder answered while the vector search matched "
            "nothing, so this floor was measured by BM25 alone and would be recorded "
            "as the hybrid standard"
        )
    # "overall" is checked alongside the categories, not instead of them: a run
    # can be complete overall while one category lost questions, and it is the
    # per-category rows that carry the tolerance.
    short = sorted(
        (cat, ran, attempted)
        for cat, (ran, attempted) in sizes.items()
        if attempted and ran < attempted
    )
    for cat, ran, attempted in short:
        blockers.append(
            f"{cat} measured {ran}/{attempted} questions — the missing ones are a "
            "harness failure, and pinning the baseline here bakes that failure "
            "into the denominator every future run is judged against"
        )
    return blockers


def modes_comparable(baseline_mode: str, run_mode: str) -> bool:
    """Scores may only be compared within one, known retrieval mode."""
    if baseline_mode == MODE_UNKNOWN or run_mode == MODE_UNKNOWN:
        return False
    return baseline_mode == run_mode


def effective_tolerance(category: str, tolerance: float, baseline: Baseline) -> float:
    """How far below baseline a score must fall before it means anything.

    At least `--tolerance`, and at least the run-to-run spread the baseline
    actually observed in this category. A category whose score swings 0.75
    between two identical runs cannot support a 0.25 verdict — calling that
    swing a regression reports the judge's nondeterminism as a code defect.
    """
    return max(tolerance, baseline.spread.get(category, 0.0))


# Why a category could not be ruled on. The two causes are NOT interchangeable
# and must not be collapsed into one "blind" bucket:
#
#   SAMPLE — this run lost questions to a harness error, so the surviving
#            denominator no longer supports the tolerance (#361). It is an
#            ANOMALY: it should be loud, and it should be rare. Any occurrence
#            makes the run inconclusive.
#   SPREAD — the baseline's own run-to-run spread is as wide as its score, so
#            no result could ever fall below the threshold. It is a STANDING
#            PROPERTY of the baseline, not an event. Making it inconclusive per
#            occurrence would pin the arm at INCONCLUSIVE for ever — the silent
#            no-op this whole card exists to avoid — so it only forces a verdict
#            when it holds for EVERY category and the run therefore enforced
#            nothing at all.
INELIGIBLE_SAMPLE = "sample"
INELIGIBLE_SPREAD = "spread"


class CategoryVerdict(NamedTuple):
    category: str
    # None when the category produced no score at all: every one of its
    # questions errored, so it is absent from `scores` and has to be recovered
    # from the baseline side. Printing 0.000 there would report a wipe as a
    # perfect failure.
    score: float | None
    baseline: float
    tolerance: float
    ran: int
    attempted: int
    eligible: bool
    ineligible_reason: str | None
    regressed: bool


def _denominator_mismatch(cat: str, ran: int, baseline: Baseline) -> bool:
    """Did the BASELINE record a different denominator than this run measured?

    Comparing a 4-question score against a figure derived from 2 is the same
    not-comparable as the run-side sample gate, one operand over — and it reads
    as a regression the moment either restored question misses (evc-mesh#362).

    Only checked when the baseline says what its denominator was: every baseline
    captured before `sample_sizes` existed is silent, and reading that silence as
    a mismatch would take the required gate blind on every category at once — a
    reader tightened past anything its writer has ever emitted. A silent baseline
    is handled by the `n_runs` warning instead.
    """
    base_ran = baseline.sample_sizes.get(cat, (0, 0))[0]
    return bool(base_ran) and bool(ran) and base_ran != ran


def baseline_denominator_mismatch(
    verdict: CategoryVerdict, baseline: Baseline, tolerance: float
) -> bool:
    """Was THIS category set aside because of the baseline, not because of the run?

    Reporting-side counterpart of the rule `classify` ruled with, sharing its
    predicate rather than restating it: the banner names a cause and an owner
    (maintainer re-snap vs harness fix), and a banner that derives the cause from
    its own copy of the rule is exactly how it comes to name the wrong one.
    """
    sample_ok = (
        category_comparable(verdict.ran, tolerance)
        if verdict.attempted
        else verdict.score is not None
    )
    return sample_ok and _denominator_mismatch(verdict.category, verdict.ran, baseline)


def classify(
    scores: dict[str, float],
    baseline: Baseline,
    tolerance: float,
    sizes: dict[str, tuple[int, int]] | None = None,
) -> list[CategoryVerdict]:
    """Per-category verdicts, including which categories cannot have one, and why.

    Two independent conditions have to hold before a category can be ruled on,
    and they fail for different reasons at different levels:

      1. the surviving SAMPLE still fits the tolerance — `category_comparable`,
         i.e. this run measured enough of the category to compare it at all;
      2. the THRESHOLD `baseline - effective_tolerance` is above zero — i.e. a
         score exists that could fall below it.

    A category failing either is reported as unruled, tagged with which one.
    Silently passing a check that cannot fail is how a gate manufactures
    coverage it does not have; silently merging the two causes is how a
    permanent structural gap gets read as tonight's flake.
    """
    sizes = sizes or {}
    verdicts = []
    # Categories the baseline knows about that produced no score at all never
    # appear in `scores`, so iterating `scores` alone cannot see them — a total
    # wipe would vanish rather than register. Recover them from the baseline.
    categories = sorted(set(scores) | set(baseline.scores))
    for cat in categories:
        score = scores.get(cat)
        base = baseline.scores.get(cat, 0.0)
        tol = effective_tolerance(cat, tolerance, baseline)
        threshold = base - tol
        ran, attempted = sizes.get(cat, (0, 0))

        # A category present in `scores` but with no recorded size predates the
        # sample bookkeeping (hand-built score dicts in tests, older artifacts);
        # treat it as sample-OK rather than inventing a wipe.
        sample_ok = category_comparable(ran, tolerance) if attempted else score is not None

        # The mismatch the run-side gate structurally cannot see: this run may
        # have measured the category fully while the BASELINE was captured on
        # fewer questions. See baseline_denominator_mismatch().
        if sample_ok and _denominator_mismatch(cat, ran, baseline):
            sample_ok = False

        spread_ok = threshold > 0.0

        if not sample_ok:
            reason = INELIGIBLE_SAMPLE
        elif not spread_ok:
            reason = INELIGIBLE_SPREAD
        else:
            reason = None
        eligible = reason is None

        verdicts.append(
            CategoryVerdict(
                category=cat,
                score=score,
                baseline=base,
                tolerance=tol,
                ran=ran,
                attempted=attempted,
                eligible=eligible,
                ineligible_reason=reason,
                regressed=eligible and score is not None and score < threshold,
            )
        )
    return verdicts


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

    Counted in DISTINCT QUESTIONS, not answer rows, because `--repeat N` puts N
    rows in `results` for every question. Counting rows would let repetition
    launder a systematically missing question into an adequate sample: with the
    two `gpt4_*` ids that fail deterministically on every pass, `--repeat 3`
    turns temporal-reasoning's honest 2-of-4 into "6 ran", 1/6 < 0.25, gate
    satisfied — while the same two questions are still missing from all three
    passes. Repeating a run buys precision on the questions that run; it buys
    exactly nothing on coverage of the ones that never do.
    """
    ran_ids: dict[str, set[str]] = {}
    attempted_ids: dict[str, set[str]] = {}
    for r in results:
        qtype = r.get("question_type")
        if qtype is None:
            continue
        qid = r.get("question_id")
        # No id to dedupe on (hand-built rows in tests): fall back to counting
        # the row, so a caller that never repeats gets the old behaviour.
        key = qid if qid is not None else object()
        attempted_ids.setdefault(qtype, set()).add(key)
        if "correct" in r:
            ran_ids.setdefault(qtype, set()).add(key)
    return {
        qtype: (len(ran_ids.get(qtype, ())), len(ids))
        for qtype, ids in attempted_ids.items()
    }


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


class Verdict(NamedTuple):
    exit_code: int
    regressions: list[str]
    unmeasured: list[str]      # ineligible because the RUN lost questions
    spread_blind: list[str]    # ineligible because the BASELINE is too noisy
    reason: str | None         # REASON_* kind when exit_code is INCONCLUSIVE


def decide_verdict(verdicts: list[CategoryVerdict]) -> Verdict:
    """The whole quality verdict, as a pure function over the classified rows.

    Kept pure and separate from cmd_run on purpose. A test that re-implements
    this decision instead of calling it passes just as happily with the logic
    reverted, which makes it a decoration rather than a guard — the exact trap
    the cleanup test in test_gate_blindness.py fell into first time round.

    Precedence: REGRESSION > sample-unmeasured > all-spread-blind > OK.

    * REGRESSION first. A measured, comparable category that got worse is a
      positive finding; partial blindness elsewhere does not erase it. The
      reverse order would let one flaky question suppress every merge block in
      the repo.
    * Then ANY sample-unmeasured category, because losing questions is an
      anomaly this run must not paper over.
    * Then spread-blindness, but only when it is TOTAL. Per-category it is a
      permanent property of the baseline (see INELIGIBLE_SPREAD): firing on one
      occurrence would wedge the arm at INCONCLUSIVE for ever, which is the
      silent no-op, not a fix for it. When it holds everywhere, though, the run
      enforced literally nothing and must not exit green.
    """
    regressions = sorted(v.category for v in verdicts if v.regressed)
    unmeasured = sorted(
        v.category for v in verdicts if v.ineligible_reason == INELIGIBLE_SAMPLE
    )
    spread_blind = sorted(
        v.category for v in verdicts if v.ineligible_reason == INELIGIBLE_SPREAD
    )
    if regressions:
        return Verdict(EXIT_REGRESSION, regressions, unmeasured, spread_blind, None)
    if unmeasured:
        return Verdict(
            EXIT_INCONCLUSIVE, regressions, unmeasured, spread_blind,
            REASON_CATEGORY_UNMEASURED,
        )
    if verdicts and not any(v.eligible for v in verdicts):
        return Verdict(
            EXIT_INCONCLUSIVE, regressions, unmeasured, spread_blind,
            REASON_NO_ELIGIBLE_CATEGORY,
        )
    return Verdict(EXIT_OK, regressions, unmeasured, spread_blind, None)


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
    baseline: Baseline | None,
    tolerance: float,
    sizes: dict[str, tuple[int, int]] | None = None,
) -> list[CategoryVerdict]:
    """Print the score table and return the per-category verdicts.

    `Sample` is not decoration. A bare "1.000" reads as "4 of 4 questions
    passed" when it can equally mean "the 2 questions that survived passed";
    the number that decides whether the row means anything was the one thing the
    table never printed.
    """
    sizes = sizes or {}
    if baseline is None:
        header = f"{'Category':<35} {'Score':>7} {'Sample':>8}"
        print(header)
        print("-" * (len(header) + 4))
        for cat, score in sorted(scores.items()):
            ran, attempted = sizes.get(cat, (0, 0))
            sample = f"{ran}/{attempted}" if attempted else "-"
            print(f"{cat:<35} {score:>7.3f} {sample:>8}")
        return []

    verdicts = classify(scores, baseline, tolerance, sizes)
    header = (
        f"{'Category':<35} {'Score':>7} {'Sample':>8} {'Baseline':>9} {'Delta':>8} "
        f"{'Thresh':>7} {'Status':>13}"
    )
    print(header)
    print("-" * (len(header) + 4))
    for v in verdicts:
        # The two unruled statuses stay visually distinct: ⚠ is tonight's
        # anomaly and should provoke someone, ⓘ is a standing limit of the
        # baseline and should not.
        if v.ineligible_reason == INELIGIBLE_SAMPLE:
            status = "⚠ UNMEASURED"
        elif v.ineligible_reason == INELIGIBLE_SPREAD:
            status = "ⓘ no verdict"
        elif v.regressed:
            status = "✗ REGRESS"
        else:
            status = "✓"
        sample = f"{v.ran}/{v.attempted}" if v.attempted else "-"
        score_s = f"{v.score:.3f}" if v.score is not None else "—"
        delta_s = f"{v.score - v.baseline:+.3f}" if v.score is not None else "—"
        # `Thresh` is the score this category must stay at or above: the baseline
        # less whichever is larger, --tolerance or the baseline's own spread.
        print(
            f"{v.category:<35} {score_s:>7} {sample:>8} {v.baseline:>9.3f} "
            f"{delta_s:>8} {v.baseline - v.tolerance:>7.3f} {status:>13}"
        )
    return verdicts


def cmd_run(args: argparse.Namespace) -> int:
    _load_env()

    retrieval_only = args.retrieval_only
    arm = getattr(args, "arm", ARM_PROD)
    if not retrieval_only:
        # The paid end-to-end arm only ever runs against the deployed server —
        # it needs a chat model and a judge, neither of which a PR runner has.
        baseline_file = BASELINE_FILE
        arm = ARM_PROD
    elif arm == ARM_BRANCH:
        baseline_file = BRANCH_RETRIEVAL_BASELINE_FILE
    else:
        baseline_file = RETRIEVAL_BASELINE_FILE

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

    # `--repeat N` (baseline capture only) runs the dataset N times so the
    # baseline can be a mean with a measured spread rather than one sample.
    repeat = max(1, args.repeat)
    results: list[dict] = []
    per_pass_scores: list[dict[str, float]] = []
    for p in range(1, repeat + 1):
        if repeat > 1:
            print(f"── pass {p}/{repeat} " + "─" * 46)
        pass_results: list[dict] = []
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
            # gate.log is what anyone actually opens after a red run, so the rank
            # goes in the line itself, not only in the JSON. `rank=none` on a miss
            # is the reading that matters: it separates "ranked 12th" (fix the
            # ranking) from "never arrived" (fix indexing or the candidate pool).
            print(f"{status} ({elapsed:.1f}s){format_rank_suffix(r)}")
            pass_results.append(r)
        results.extend(pass_results)
        per_pass_scores.append(compute_scores(pass_results))
        if repeat > 1:
            overall = per_pass_scores[-1].get("overall")
            print(f"   pass {p}/{repeat} overall: " + (f"{overall:.3f}" if overall is not None else "n/a"))

    errors = report_errors(results)
    ran = len(results) - len(errors)
    error_rate = len(errors) / len(results) if results else 1.0

    scores = compute_scores(results)
    sizes = category_sample_sizes(results)
    # "overall" is a real row in `scores`, so it needs a denominator too — but it
    # is pooled across every category, so its quantum is 1/22, not 1/2. It stays
    # comparable exactly when the global error budget above says it does.
    # Distinct questions, for the same reason as category_sample_sizes: under
    # `--repeat N` the row count is N× the coverage.
    sizes["overall"] = (
        len({r["question_id"] for r in results if "correct" in r}),
        len({r["question_id"] for r in results}),
    )
    run_mode = resolve_run_search_mode(results)

    # Written BEFORE the verdict branches below, every one of which can return
    # early (INCONCLUSIVE, capture refused, regression). An artifact only
    # produced on the happy path is missing exactly when it is wanted most.
    results_file = write_results_artifact(
        RETRIEVAL_RESULTS_FILE if retrieval_only else E2E_RESULTS_FILE,
        results,
        retrieval_only=retrieval_only,
        run_mode=run_mode,
        top_k=top_k,
        repeat=repeat,
        scores=scores,
        sizes=sizes,
        dense_arm=resolve_dense_arm_status(results),
    )
    if results_file is not None:
        print(f"\nPer-question results: {results_file}")

    baseline: Baseline | None = None
    baseline_mode = MODE_UNKNOWN
    if baseline_file.exists():
        baseline = load_baseline(baseline_file)
        baseline_mode = baseline.search_mode

    title = "Recall Gate Results" if retrieval_only else "LongMemEval-S Results"
    print()
    print("=" * 70)
    print(f"{title}  ({ran}/{len(results)} questions ran)")
    print(f"Search mode served: {run_mode}    Baseline mode: {baseline_mode}")
    print(f"Dense arm: {resolve_dense_arm_status(results)}")
    if baseline is not None:
        print(f"Baseline captured from {baseline.n_runs} pass(es)")
    print("=" * 70)
    verdicts = print_table(scores, baseline, tolerance, sizes)
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
        # Refuse BEFORE writing. A capture guard that reports after the file is on
        # disk is advice, not a guard — the artifact still gets uploaded and the
        # figure still gets committed.
        blockers = capture_blockers(
            sizes, run_mode, retrieval_only, resolve_dense_arm_status(results)
        )
        if blockers and not args.allow_partial_capture:
            print(
                f"\n✗ CAPTURE REFUSED — {baseline_file.name} was NOT written. This run "
                "is not a sound basis for a required check's baseline:"
            )
            for b in blockers:
                print(f"  • {b}")
            print(
                "\nFix the cause and re-capture. If you genuinely need to pin a "
                "required check on this run, pass --allow-partial-capture and say in "
                "the commit message which figures rest on an incomplete sample."
            )
            print(
                f"{REASON_PREFIX} {REASON_CAPTURE_REFUSED} — "
                f"{len(blockers)} blocker(s); {baseline_file.name} not written"
            )
            return EXIT_INCONCLUSIVE
        if blockers:
            print(
                "\n⚠ --allow-partial-capture: writing a baseline over "
                f"{len(blockers)} blocker(s) listed above."
            )

        # BOTH arms write the MODE-SCOPED shape. It records the retrieval mode it
        # was captured in, because hit@k in bm25-only mode is not comparable to
        # hit@k in hybrid mode; a baseline without a mode can only ever produce
        # INCONCLUSIVE. The advisory arm used to write a flat mode-less dict here,
        # which made its own mode gate permanently unsatisfiable.
        payload = build_baseline_payload(
            per_pass_scores, run_mode, top_k, sizes, arm, args.served_commit
        )
        baseline_file.write_text(json.dumps(payload, indent=2) + "\n")
        print(
            f"\nBaseline updated: {baseline_file}  "
            f"(search_mode={run_mode}, n_runs={payload['n_runs']})"
        )
        if run_mode != MODE_HYBRID:
            print(
                f"⚠ Captured in mode '{run_mode}', not '{MODE_HYBRID}'. "
                "If the embedder is merely down, this baseline pins a DEGRADED "
                "quality level — re-snap it once the embedder is healthy."
            )
        if payload["n_runs"] < 2:
            print(
                "⚠ Captured from a SINGLE pass, so it carries no measured spread and "
                "every threshold will fall back to --tolerance. On a judged eval with "
                "4 questions per category, one flipped answer is 0.25 — re-snap with "
                "`--repeat 3` to record a distribution instead of a coin toss."
            )
        else:
            widest = max(payload["spread"].items(), key=lambda kv: kv[1])
            print(
                f"  Widest observed spread across the {payload['n_runs']} passes: "
                f"{widest[0]} {widest[1]:.3f}"
            )
        # A baseline snapped while questions were failing pins a figure derived
        # from fewer questions than the runs it will judge. Say it at capture
        # time, when it is still cheap to fix; by the time a nightly compares
        # against it the number looks as authoritative as any other.
        short = {
            cat: (r, a) for cat, (r, a) in sizes.items()
            if cat != "overall" and a and r < a
        }
        if short:
            detail = ", ".join(f"{c} ({r}/{a})" for c, (r, a) in sorted(short.items()))
            print(
                f"⚠ Captured on an INCOMPLETE sample in: {detail}. Those figures rest "
                "on fewer questions than a healthy run will measure, so every future "
                "run is compared against a different denominator. Recorded in "
                "`sample_sizes` so the gate can refuse the comparison — fix the "
                "harness errors above and re-snap."
            )
        return EXIT_OK

    if baseline is None:
        # A pass against no baseline compares against NOTHING. Calling that green
        # would make an unenforcing gate look healthy — exactly the blindness this
        # gate exists to remove. It is inconclusive, and it says so out loud.
        #
        # This applies to BOTH arms. It used to return EXIT_OK for the advisory
        # arm, so if baseline.json ever went missing that arm reported a plain
        # green pass against nothing at all — a vacuous green is not less of a lie
        # for being advisory.
        print(
            f"\n⚠ EVAL INCONCLUSIVE — no {baseline_file.name} found: this run was "
            "compared against nothing and enforced nothing. Snap a baseline with "
            "--update-baseline (against a healthy embedder) before relying on this gate."
        )
        print(f"{REASON_PREFIX} {REASON_NO_BASELINE} — {baseline_file.name} does not exist")
        return EXIT_INCONCLUSIVE

    # ── Arm gate: is this baseline even about the same system? ────────────────
    # Sits before the mode gate because it is the coarser question: mode asks
    # "was retrieval served the same way", arm asks "was it the same server at
    # all". Only ever INCONCLUSIVE — the author of a PR cannot fix a baseline
    # file that was captured in the wrong arm, and a red they cannot clear is
    # the failure mode the whole two-arm design is trying to avoid.
    #
    # An UNSTATED arm ("") is accepted rather than blocked, because every
    # baseline captured before ADR-0003 has no `arm` field and blocking on that
    # would wedge the prod arm the moment this lands. Unstated is only accepted
    # in the prod arm: the branch arm's baseline is created by this change, so
    # there is no pre-existing unlabelled file it could legitimately be.
    if baseline.arm and baseline.arm != arm:
        print(
            f"\n⚠ EVAL INCONCLUSIVE — {baseline_file.name} was captured in arm "
            f"'{baseline.arm}', but this run is arm '{arm}'. These measure "
            "different systems (different embedder, different corpus), so a "
            "comparison between them would be arithmetically valid and "
            "factually meaningless. Nothing was enforced."
        )
        print(
            f"{REASON_PREFIX} {REASON_ARM_MISMATCH} — baseline arm "
            f"'{baseline.arm}' != run arm '{arm}'"
        )
        return EXIT_INCONCLUSIVE
    if not baseline.arm and arm == ARM_BRANCH:
        print(
            f"\n⚠ EVAL INCONCLUSIVE — {baseline_file.name} states no arm, and the "
            "branch arm has no pre-ADR-0003 baseline it could legitimately be. "
            "This is almost certainly the prod baseline copied into the branch "
            "arm's path. Re-snap with `--arm branch --update-baseline`."
        )
        print(
            f"{REASON_PREFIX} {REASON_ARM_MISMATCH} — baseline states no arm, "
            f"run arm '{arm}' requires one"
        )
        return EXIT_INCONCLUSIVE

    # ── Mode gate: compare like with like, or do not compare at all ───────────
    # This check sits BEFORE any score comparison and can only ever produce
    # EXIT_INCONCLUSIVE. A cross-mode score drop is a statement about the
    # embedder's health, not about this PR's code, and must never return
    # EXIT_REGRESSION: a required check that goes red on an infra outage the
    # author cannot fix blocks every PR in the repo and gets bypassed.
    #
    # Applies to BOTH arms. It used to be `retrieval_only and ...`, which left the
    # advisory arm comparing a `hybrid` run against the legacy mode-less
    # baseline.json — an UNKNOWN baseline it had no basis to compare to — and
    # printing `✗ REGRESSION` for the difference on most nights. "Cannot compare"
    # is a property of the two operands, not of which arm holds them.
    if not modes_comparable(baseline_mode, run_mode):
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

    # ── Dense-arm gate: "hybrid" is a claim about the embedder, not the corpus ─
    # Sits AFTER the mode gate on purpose. Mode-unknown / mode-mismatch are the
    # broader statements ("we do not know what we measured" / "we measured
    # something else"), and reporting the narrower cause underneath one of them
    # would just be a second alert for a condition already owned.
    #
    # INCONCLUSIVE, never REGRESSION, for the same reason the mode gate is: the
    # dense arm being empty is a property of the deployed server and its corpus,
    # not of the diff under review, and a required check that goes red on
    # something the author cannot fix is a check that gets bypassed.
    dense_status = resolve_dense_arm_status(results)
    if dense_status == DENSE_ARM_EMPTY:
        print(
            f"\n⚠ INCONCLUSIVE: every recall was served in mode '{MODE_HYBRID}' and the "
            "dense/vector arm returned ZERO rows in all of them. The embedder answered "
            "— which is all `search_mode` ever checked — but the vector search matched "
            "nothing at all, so this run was served by BM25 alone while reporting itself "
            "healthy."
        )
        print(
            "  Likely cause: the corpus has no usable embeddings (e.g. every "
            "`memories.embedding` is NULL because the write path stopped populating "
            "it). Scores measured here say nothing about the dense arm — including a "
            "GREEN one, which is how this class of failure gets recorded as a personal "
            "best. Re-index/backfill embeddings, then re-run."
        )
        print(
            f"{REASON_PREFIX} {REASON_DENSE_ARM_EMPTY} — "
            f"run='{run_mode}' dense_rows=0 on every reporting question"
        )
        return EXIT_INCONCLUSIVE
    if dense_status == DENSE_ARM_UNKNOWN:
        # Not a verdict, and deliberately not one: a server that does not report
        # `dense_rows` leaves this check inert rather than tripping it. Said out
        # loud so "the gate is watching the dense arm" is never assumed of a
        # deployment where it structurally cannot.
        print(
            "\nNote: this server did not report `dense_rows`, so the dense-arm check "
            "did not run. A recall served by an EMPTY vector arm is indistinguishable "
            "from a healthy hybrid one here — deploy a Mesh that reports it to close "
            "that gap."
        )

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

    if baseline.n_runs < 2:
        print(
            f"\n⚠ {baseline_file.name} was captured from a SINGLE pass (n_runs="
            f"{baseline.n_runs}): it records no spread, so every threshold above fell "
            "back to --tolerance and any verdict below is one sample against another. "
            "Re-snap with `--update-baseline --repeat 3`."
        )

    # The exit code comes from decide_verdict and persistent_verdict and nowhere
    # else, so the reporting below cannot drift out of step with the precedence
    # the tests pin.
    verdict = decide_verdict(verdicts)
    # Folded in AFTER decide_verdict, deliberately: a question that fails
    # identically every run falls outside the error budget's premise, but it must
    # only ever raise OK to INCONCLUSIVE — never lower a REGRESSION. See
    # persistent_verdict().
    stuck = persistent_errors(errors)
    persistent_blind = len(stuck) > args.max_persistent_errors
    rc = persistent_verdict(verdict.exit_code, len(stuck), args.max_persistent_errors)

    # ── Sample gate (#361, #362): a category could not be compared ─────────────
    # Set aside BEFORE the comparison, so a dropped question can neither
    # manufacture a regression nor be laundered into a pass. TWO causes land in
    # this one bucket and they are not interchangeable: questions lost to a
    # harness error are the PR author's to fix, while a baseline captured on a
    # different denominator is the maintainer's to re-snap. Recomputed here for
    # REPORTING only, through the same predicate classify() ruled with — a
    # reporting copy of the rule is how the banner and the verdict drift apart.
    mismatched = sorted(
        v.category
        for v in verdicts
        if v.ineligible_reason == INELIGIBLE_SAMPLE
        and baseline_denominator_mismatch(v, baseline, tolerance)
    )
    sample_detail = ", ".join(
        (
            f"{v.category} ({v.ran} questions this run vs "
            f"{baseline.sample_sizes.get(v.category, (0, 0))[0]} in the baseline)"
        )
        if v.category in mismatched
        else f"{v.category} ({v.ran}/{v.attempted} questions)"
        for v in verdicts
        if v.ineligible_reason == INELIGIBLE_SAMPLE
    )
    if verdict.unmeasured:
        # What SURVIVED matters as much as what did not. `category-unmeasured` is
        # partial by construction — decide_verdict ranks REGRESSION above
        # INCONCLUSIVE, so every comparable category is still enforced and still
        # blocks. Saying "nothing was measured" when 6 of 7 categories were is how
        # a banner earns the discount it then gets applied at the moment it is
        # telling the truth.
        enforced = sorted(v.category for v in verdicts if v.eligible)
        print(
            f"\n⚠ {len(verdict.unmeasured)} of {len(verdicts)} category(ies) could not "
            f"be compared against the baseline: {sample_detail}."
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
        if [c for c in verdict.unmeasured if c not in mismatched]:
            print(
                "Cause for the rest: questions were lost to a harness error above, "
                "not to this PR's code — the fix is to stop losing questions, not to "
                "lower the bar."
            )

    # ── Spread gate: the BASELINE cannot support a verdict here ───────────────
    if verdict.spread_blind:
        blind = [v for v in verdicts if v.ineligible_reason == INELIGIBLE_SPREAD]
        print(
            "\nⓘ No verdict possible in: "
            + ", ".join(
                f"{v.category} (baseline {v.baseline:.3f} ≤ threshold width {v.tolerance:.2f})"
                for v in blind
            )
        )
        print(
            "  These categories cannot produce a regression at this sample size — the "
            "run-to-run spread is as wide as the baseline itself, so no score could "
            "fall below the threshold. They are NOT counted as passing: to get a real "
            "verdict here the category needs more questions, or a chat model whose "
            "answers do not flip between identical runs."
        )

    if verdict.regressions:
        print(f"\n✗ REGRESSION detected in: {', '.join(verdict.regressions)}")
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
        # Outranks the unmeasured branch below when both apply: it is the CAUSE
        # under it. A question lost every run is what shrinks a category below
        # `category_comparable`, so reporting the symptom would send the reader
        # after a harness flake that renews itself every run.
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
    elif verdict.reason == REASON_CATEGORY_UNMEASURED:
        print("\n⚠ EVAL INCONCLUSIVE — the recall safety net did not cover every category.")
        # Kind selected by CAUSE, not by which branch printed it: the workflow
        # dedups its alert on the kind, and the two causes need two different
        # alerts (see REASON_BASELINE_SAMPLE_MISMATCH). A run with both reports the
        # mismatch, because that one is actionable by the person reading the alert.
        kind = (
            REASON_BASELINE_SAMPLE_MISMATCH if mismatched
            else REASON_CATEGORY_UNMEASURED
        )
        print(f"{REASON_PREFIX} {kind} — {sample_detail}")
    elif verdict.reason == REASON_NO_ELIGIBLE_CATEGORY:
        # Nothing was verdict-eligible, so nothing was enforced. Reporting that as
        # a pass is the vacuous green again, one level down: the comparison ran,
        # but every category was too noisy to rule on.
        print(
            "\n⚠ EVAL INCONCLUSIVE — no category was verdict-eligible: the baseline's "
            "own spread is wider than its scores everywhere, so this run enforced "
            "nothing. Nothing here is evidence that memory is healthy."
        )
        print(
            f"{REASON_PREFIX} {REASON_NO_ELIGIBLE_CATEGORY} — "
            f"0/{len(verdicts)} categories could produce a verdict"
        )
    else:
        eligible = [v for v in verdicts if v.eligible]
        print(
            f"\n✓ All categories within tolerance "
            f"({len(eligible)}/{len(verdicts)} verdict-eligible)."
        )

    return rc


def main() -> int:
    logging.basicConfig(level=logging.WARNING, format="%(levelname)s: %(message)s")
    parser = argparse.ArgumentParser(description="LongMemEval-S CI regression gate")
    parser.add_argument(
        "--update-baseline",
        action="store_true",
        help="Run benchmark and write results to the arm's baseline file (mode-scoped)",
    )
    parser.add_argument(
        "--served-commit",
        default="",
        help=(
            "The commit the version pin confirmed prod was serving. Recorded in the "
            "baseline as `commit` so the file states which binary it measured. Empty "
            "is honest (branch arm, or the endpoint could not be read) and simply "
            "omits the field — an absent `commit` must read as 'unstated', never as "
            "'the same binary as this run'."
        ),
    )
    parser.add_argument(
        "--repeat",
        type=int,
        default=1,
        help=(
            "Baseline capture only: run the dataset N times and record the MEAN plus "
            "the observed per-category spread (default: 1). A judged eval with 4 "
            "questions per category moves 0.25 per flipped answer, so a 1-pass "
            "baseline is a coin toss; use 3 for the paid end-to-end arm."
        ),
    )
    parser.add_argument(
        "--allow-partial-capture",
        action="store_true",
        help=(
            "Write the retrieval baseline even when the run was incomplete or served "
            "in a degraded retrieval mode. Off by default: that baseline is the "
            "threshold of a REQUIRED check, and one captured on a smaller sample "
            "turns a later sample change into a REGRESSION verdict (evc-mesh#362)."
        ),
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
        "--arm",
        choices=[ARM_PROD, ARM_BRANCH],
        default=ARM_PROD,
        help=(
            "Which system this run measures (ADR-0003). 'prod' = the deployed "
            "server (baseline_retrieval.json); 'branch' = cmd/api built from the "
            "PR head on an ephemeral DB (baseline_retrieval_branch.json). The "
            "arm selects the baseline file AND is written into it, so a file "
            "used in the wrong arm is refused rather than silently compared."
        ),
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

    if args.repeat > 1 and not args.update_baseline:
        # A gate run must reach ONE verdict from ONE measurement. Averaging passes
        # to decide whether to go red would hide exactly the variance the verdict
        # needs to account for — that is the baseline's job, not the verdict's.
        parser.error("--repeat is only meaningful with --update-baseline")
    if args.repeat < 1:
        parser.error("--repeat must be >= 1")
    if args.allow_partial_capture and not args.update_baseline:
        # It only ever gates a write. Accepting it on a gate run would read as
        # "loosen the verdict", which it does not do — a flag whose name suggests
        # a power it lacks is worse than no flag.
        parser.error("--allow-partial-capture is only meaningful with --update-baseline")

    # Ensure script dir is on path for mesh_client_stdio import
    sys.path.insert(0, str(SCRIPT_DIR))

    return cmd_run(args)


if __name__ == "__main__":
    raise SystemExit(main())
