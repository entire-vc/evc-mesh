# LongMemEval-S research harness — temporal-reasoning fix

Moved here from an uncommitted third-party clone (`~/bench/metronix-memory` on the Mac
Mini) 2026-08-23, `#7266421c`. This is the **research** harness that produced
[`ADR-temporal-reasoning-fix.md`](./ADR-temporal-reasoning-fix.md) — the full LongMemEval-S
benchmark against Mesh `recall`, judged by an LLM, used to validate the temporal-recall
fix. It is separate from `scripts/memory-bench/` one level up, which is the lean CI harness
(recall-only merge gate + advisory nightly LongMemEval-S run) — this directory is the
ablation/one-off-research counterpart, not wired into any CI gate.

## What's here

```
ADR-temporal-reasoning-fix.md            — the write-up; numbers below reproduce its table
data/question_categories.json            — question_id -> question_type, 500 rows, ~17KB
results/full500.jsonl.eval-openai_gpt-4o             — baseline (pre-fix), judged
results/full500-minefix-20260821.jsonl.eval-openai_gpt-4o — post-fix, judged
scripts/mesh_client.py                   — MeshMemoryClient: talks to Mesh recall/remember
scripts/run_benchmark.py                 — generates hypothesis answers over the haystack
scripts/evaluate_results.py              — invokes the vendored LLM judge (paid, needs a key)
scripts/env_config.py                    — .env.benchmark loader (var names only, no values)
scripts/score_by_category.py             — aggregates an already-judged file into a table
```

**Not committed:** the 500-question fixture set (`longmemeval_s_cleaned.json`, ~246MB,
source: HuggingFace `xiaowu0162/longmemeval-cleaned`) and the LongMemEval judge's vendored
script (`vendor/evaluate_qa.py`, from the original benchmark repo). Both are needed only to
*re-run* the benchmark (answer generation + LLM judging), not to *re-score* an already-judged
results file. Re-running end-to-end: `python3 scripts/run_benchmark.py download --variant s`
re-fetches the fixture set; the vendored judge script isn't reproduced here — re-running the
judge step needs it restored from the original harness or a Mesh-side equivalent.

## Reproducing the numbers (no API key, no network, no Mesh connection)

The claim in the ADR is that the temporal-reasoning fix moved the score from
30.8% → 73.7% on that category, 70.0% → 82.2% overall. Both numbers come straight out
of the two committed judged-results files — reproduce them with:

```bash
python3 scripts/score_by_category.py results/full500.jsonl.eval-openai_gpt-4o
python3 scripts/score_by_category.py results/full500-minefix-20260821.jsonl.eval-openai_gpt-4o
```

Expected output for the post-fix file:

```
knowledge-update: 72/78 = 92.3%
multi-session: 94/133 = 70.7%
single-session-assistant: 56/56 = 100.0%
single-session-preference: 24/30 = 80.0%
single-session-user: 67/70 = 95.7%
temporal-reasoning: 98/133 = 73.7%
OVERALL: 411/500 = 82.2%
```

This is pure aggregation over rows that already carry an `autoeval_label` from a prior
judge run — it costs nothing and needs no credentials, which is what makes it a genuine
positive control rather than "the files are present."

## Re-running the actual benchmark (not needed to verify the ADR's numbers)

Requires a Mesh agent key for a **dedicated bench workspace** (`mesh_client.py` refuses
to run against any other workspace — see its module docstring) and an OpenAI-compatible
chat + judge API key. Copy `.env.benchmark.example` to `.env.benchmark` in this directory,
fill it in, then:

```bash
python3 scripts/run_benchmark.py download --variant s   # fetches the 246MB fixture set
python3 scripts/run_benchmark.py run --variant s --out results/my-run.jsonl
python3 scripts/evaluate_results.py --results results/my-run.jsonl
python3 scripts/score_by_category.py results/my-run.jsonl.eval-openai_gpt-4o
```

This is genuinely expensive (paid LLM calls for 500 answer-generation + 500 judge calls)
and out of scope for this migration — see the ADR's "Full-500 confirmation" section for
what the last real run cost and how long it took.
