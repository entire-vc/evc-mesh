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
  2  INCONCLUSIVE — the eval could NOT run (Mesh unreachable, judge/chat API
     down or out of credit, …). An eval that did not run has NOT measured a
     regression. Scoring an un-asked question as "wrong" turns any infra outage
     into a fake quality collapse, so errored questions are excluded from the
     scores and reported separately.

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
        print(f"ERROR: {key} is not set", file=sys.stderr)
        sys.exit(1)
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
    from mesh_client_stdio import MeshMemoryClient

    qid = entry["question_id"]
    qtype = entry["question_type"]

    def errored(stage: str, exc: Exception) -> dict:
        # NOTE: no "correct" key. An errored question was never asked, so it is
        # excluded from the scores rather than counted as a wrong answer — that
        # conflation is what turned an API 402 into a fake 7-category
        # "REGRESSION" on every PR.
        logger.error("%s failed for %s: %s", stage, qid, exc)
        return {
            "question_id": qid,
            "question_type": qtype,
            "error": f"{stage}: {exc}",
            "error_stage": stage,
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

    if retrieval_only:
        gold = gold_session_indices(entry)
        if not gold:
            return errored(
                "gold_labels",
                ValueError("no answer_session_ids resolved against haystack"),
            )
        hit = bool(gold & retrieved_session_indices(results))
        return {"question_id": qid, "question_type": qtype, "correct": hit}

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

    return {"question_id": qid, "question_type": qtype, "correct": correct}


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


def report_errors(results: list[dict]) -> list[dict]:
    return [r for r in results if r.get("error")]


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


def print_table(scores: dict[str, float], baseline: dict[str, float] | None, tolerance: float) -> None:
    header = f"{'Category':<35} {'Score':>7}"
    if baseline:
        header += f" {'Baseline':>9} {'Delta':>8} {'Status':>8}"
    print(header)
    print("-" * (len(header) + 4))
    for cat, score in sorted(scores.items()):
        line = f"{cat:<35} {score:>7.3f}"
        if baseline:
            base = baseline.get(cat, 0.0)
            delta = score - base
            status = "✓" if score >= base - tolerance else "✗ REGRESS"
            line += f" {base:>9.3f} {delta:>+8.3f} {status:>8}"
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

    baseline: dict[str, float] | None = None
    if baseline_file.exists():
        baseline = json.loads(baseline_file.read_text())

    title = "Recall Gate Results" if retrieval_only else "LongMemEval-S Results"
    print()
    print("=" * 70)
    print(f"{title}  ({ran}/{len(results)} questions ran)")
    print("=" * 70)
    print_table(scores, baseline, tolerance)
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
        return EXIT_INCONCLUSIVE

    if args.update_baseline:
        baseline_file.write_text(json.dumps(scores, indent=2) + "\n")
        print(f"\nBaseline updated: {baseline_file}")
        return EXIT_OK

    if baseline is None:
        print(f"\nNo {baseline_file.name} found — run with --update-baseline first.")
        return EXIT_OK

    if errors:
        print(
            f"\nNote: {len(errors)} question(s) errored and were EXCLUDED from the "
            "scores above (within the allowed error budget)."
        )

    regressions = [
        cat for cat, score in scores.items()
        if score < baseline.get(cat, 0.0) - tolerance
    ]
    if regressions:
        print(f"\n✗ REGRESSION detected in: {', '.join(regressions)}")
        return EXIT_REGRESSION

    print("\n✓ All categories within tolerance.")
    return EXIT_OK


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
            "questions are always excluded from the scores, never counted wrong."
        ),
    )
    args = parser.parse_args()

    # Ensure script dir is on path for mesh_client_stdio import
    sys.path.insert(0, str(SCRIPT_DIR))

    return cmd_run(args)


if __name__ == "__main__":
    raise SystemExit(main())
