# Memory benchmark — LongMemEval-S

Two arms, deliberately split by **cost** and **determinism**.

| Arm | Cost | Deterministic | Runs on | Merge gate? |
|---|---|---|---|---|
| **Recall gate** (`--retrieval-only`) | free — no LLM | yes | every PR | **yes** (target) |
| **End-to-end LongMemEval** (default) | paid API + LLM judge | no | nightly / dispatch / push | never |

## Why they are split

The end-to-end eval answers questions with a chat model and grades them with an
LLM judge. That measures the *whole stack* — including `gpt-4o-mini` — and it
depends on a paid third-party API. Both properties disqualify it as a merge gate:
it can go red for reasons that have nothing to do with this repo's memory code.

The recall gate ingests the same haystack, runs `recall`, and asks one question:
**did a gold `answer_session_ids` session come back in the top-k?** That is the
signal our memory code actually owns. It needs no LLM, so it cannot be taken
down by an out-of-credit API — which makes it safe to fail *closed*.

## Exit codes

The distinction between 1 and 2 is load-bearing:

| Code | Meaning |
|---|---|
| `0` | within tolerance of the baseline |
| `1` | **REGRESSION** — the eval ran, and memory quality dropped |
| `2` | **INCONCLUSIVE** — the eval *could not run* (Mesh down, judge API out of credit, harness broken) |

An eval that did not run has not measured a regression. Errored questions are
**excluded** from the scores, never scored as wrong answers.

> This is not hypothetical. Before this split, an infra error was recorded as
> `correct: False`. When the OpenRouter account behind `LME_JUDGE_API_KEY` ran
> out of credit, all 24 questions errored with `402`, every category scored
> `0.000`, and CI reported `✗ REGRESSION detected in: knowledge-update,
> multi-session, …` — on a PR that in fact *improved* recall from 64% to 83%.
> The check was reporting a memory collapse that never happened.

## Making the recall gate a required check

Do it in this order. Skipping step 1 wedges every PR.

1. **Baseline it.** `baseline_retrieval.json` must exist and be generated while
   the embedder is healthy — a baseline captured during a dense-arm outage bakes
   in the degraded BM25-only scores:
   ```
   python run_ci.py --retrieval-only --update-baseline
   ```
2. **Confirm it can actually pass** — at least one green `Memory recall gate` run
   on a real PR.
3. **Then** add the context to branch protection:
   ```
   gh api -X PATCH repos/entire-vc/evc-mesh/branches/main/protection/required_status_checks \
     -f 'contexts[]=Memory recall gate'
   ```
   The context name must match the **check-run name on a PR** exactly, and the
   workflow must have **no `paths:` filter** on `pull_request` — a required
   context that never reports blocks the PR forever (cf. evc-mesh#320). The gate
   runs on every PR and no-ops internally when no memory path changed.

## Local run

```bash
export MESH_API_URL=https://mesh.entire.host
export MESH_AGENT_KEY=<bench agent key>
export MESH_MCP_BIN=~/bin/mesh-mcp
python run_ci.py --retrieval-only            # free
python run_ci.py                             # paid: also needs LME_JUDGE_API_KEY
```

The bench writes its haystack to the shared workspace under `bench-<qid>` /
`lme-bench` tags and deletes it in a `finally` block. If you ever see `lme-bench`
memories surviving a run, cleanup was skipped — they pollute real agents' recall.
