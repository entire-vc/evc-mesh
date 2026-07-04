#!/usr/bin/env python3
"""LongMemEval-S CI regression gate for evc-mesh memory.

Usage (Mac Mini baseline generation):
  python run_ci.py --update-baseline

Usage (CI regression gate):
  python run_ci.py [--tolerance 0.05]

Exits 0 if all categories are within tolerance of baseline.json.
Exits 1 if any category regresses below (baseline - tolerance).

Required environment variables:
  MESH_API_URL        — Mesh API base URL
  MESH_AGENT_KEY      — Mesh agent key (ci-bench)
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


def run_single(entry: dict, *, chat_client: Any, chat_model: str, judge_client: Any, judge_model: str, top_k: int) -> dict:
    from mesh_client_stdio import MeshMemoryClient

    qid = entry["question_id"]
    qtype = entry["question_type"]

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
        logger.error("ingest_and_search failed for %s: %s", qid, exc)
        traceback.print_exc()
        return {"question_id": qid, "question_type": qtype, "correct": False, "error": str(exc)}

    memory_context = build_memory_context(results)
    user_message = ANSWER_PROMPT.format(
        memory_context=memory_context,
        current_date=entry.get("question_date", ""),
        question=entry["question"],
    )

    try:
        response = chat_complete(chat_client, model=chat_model, system=ANSWER_SYSTEM, user=user_message)
    except Exception as exc:
        logger.error("chat_complete failed for %s: %s", qid, exc)
        return {"question_id": qid, "question_type": qtype, "correct": False, "error": str(exc)}

    correct = judge_answer(
        judge_client,
        model=judge_model,
        question_type=qtype,
        question=entry["question"],
        answer=entry["answer"],
        response=response,
    )
    return {"question_id": qid, "question_type": qtype, "correct": correct}


def compute_scores(results: list[dict]) -> dict[str, float]:
    by_type: dict[str, list[bool]] = {}
    for r in results:
        qtype = r["question_type"]
        by_type.setdefault(qtype, []).append(r.get("correct", False))
    scores: dict[str, float] = {}
    for qtype, corrects in sorted(by_type.items()):
        scores[qtype] = sum(corrects) / len(corrects) if corrects else 0.0
    if results:
        all_correct = [r.get("correct", False) for r in results]
        scores["overall"] = sum(all_correct) / len(all_correct)
    return scores


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

    # Validate required env
    mesh_url = _require_env("MESH_API_URL")
    mesh_key = _require_env("MESH_AGENT_KEY")
    judge_key = _require_env("LME_JUDGE_API_KEY")
    judge_base_url = os.environ.get("LME_JUDGE_BASE_URL", "https://openrouter.ai/api/v1")
    judge_model = os.environ.get("LME_JUDGE_MODEL", "openai/gpt-4o-mini")
    chat_key = os.environ.get("LME_CHAT_API_KEY", judge_key)
    chat_base_url = os.environ.get("LME_CHAT_BASE_URL", judge_base_url)
    chat_model = os.environ.get("LME_CHAT_MODEL", judge_model)
    top_k = int(os.environ.get("LME_TOP_K", "10"))
    tolerance = args.tolerance

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
        )
        elapsed = time.monotonic() - t0
        status = "✓" if r.get("correct") else "✗"
        print(f"{status} ({elapsed:.1f}s)")
        results.append(r)

    scores = compute_scores(results)

    baseline: dict[str, float] | None = None
    if BASELINE_FILE.exists():
        baseline = json.loads(BASELINE_FILE.read_text())

    print()
    print("=" * 70)
    print("LongMemEval-S Results")
    print("=" * 70)
    print_table(scores, baseline, tolerance)
    print("=" * 70)

    if args.update_baseline:
        BASELINE_FILE.write_text(json.dumps(scores, indent=2) + "\n")
        print(f"\nBaseline updated: {BASELINE_FILE}")
        return 0

    if baseline is None:
        print("\nNo baseline.json found — run with --update-baseline first.")
        return 0

    regressions = [
        cat for cat, score in scores.items()
        if score < baseline.get(cat, 0.0) - tolerance
    ]
    if regressions:
        print(f"\n✗ REGRESSION detected in: {', '.join(regressions)}")
        return 1

    print("\n✓ All categories within tolerance.")
    return 0


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
        default=0.05,
        help="Allowed per-category regression below baseline (default: 0.05)",
    )
    args = parser.parse_args()

    # Ensure script dir is on path for mesh_client_stdio import
    sys.path.insert(0, str(SCRIPT_DIR))

    return cmd_run(args)


if __name__ == "__main__":
    raise SystemExit(main())
