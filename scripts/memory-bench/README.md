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
down by an out-of-credit judge API — which makes it safe to fail *closed* on a
measured regression. It still depends on prod's *embedder*, though, and that
dependency is handled by mode-scoping the baseline rather than by wedging the
repo (see below).

## Exit codes and failure semantics

The distinction between 1 and 2 is load-bearing:

| Code | Meaning | Blocks the merge? | CI behaviour |
|---|---|---|---|
| `0` | measured, comparable, within tolerance | no | ✅ green |
| `1` | **REGRESSION** — the gate ran, the run and the baseline are comparable, and recall got worse | **yes** | ❌ red |
| `2` | **INCONCLUSIVE** — the gate could not measure, or could not measure *comparably* | no | ✅ green **+ loud alert** |

**Exit 1 is the only blocking condition.** It means the gate measured a real,
comparable, worse number — a fault the PR author caused and can fix.

**Exit 2 does not block, but it always alerts.** It covers:

* the harness could not run (Mesh unreachable, too many errored questions);
* **no baseline exists** — a pass against nothing is a *vacuous green*;
* the run's `search_mode` differs from the baseline's (see below).

Why exit 2 does not fail the check: the causes are all infrastructure the PR
author cannot touch. A required check that goes red on a prod outage blocks
*every* PR in the repo and blames each author for a fault they did not cause —
a permanent merge-wedge (cf. evc-mesh#320). In practice such a check gets
bypassed, and a bypassed gate protects nothing. **The cure for a silent
fail-open is an ALERT, not a wedge.** So CI writes a prominent ⚠️ block to the
job summary *and* opens/updates a single tracking issue ("Memory recall gate is
INCONCLUSIVE (safety net blind)") — one issue, commented only when the reason
kind changes, never one issue per PR.

An eval that did not run has not measured a regression. Errored questions are
**excluded** from the scores, never scored as wrong answers.

> This is not hypothetical. Before this split, an infra error was recorded as
> `correct: False`. When the OpenRouter account behind `LME_JUDGE_API_KEY` ran
> out of credit, all 24 questions errored with `402`, every category scored
> `0.000`, and CI reported `✗ REGRESSION detected in: knowledge-update,
> multi-session, …` — on a PR that in fact *improved* recall from 64% to 83%.
> The check was reporting a memory collapse that never happened.

## The baseline is MODE-SCOPED

Mesh `recall` is a hybrid search: a sparse **BM25** arm plus a dense **vector**
arm, merged with RRF. The dense arm needs a live embedder — and `recall`
**fails open** when the embedder dies: it silently drops the dense arm and
serves BM25-only results, with a 200 and one log line.

Recall now reports which arms actually served it. The REST envelope of
`GET /api/v1/memories/search` carries two additive fields:

```json
{ "items": [...], "total": 10, "limit": 10, "offset": 0,
  "search_mode": "hybrid",   // or "bm25-only"
  "degraded": false }        // true whenever search_mode != "hybrid"
```

Hit@k in `bm25-only` mode is systematically lower than in `hybrid` mode. So a
score drop across modes measures **the embedder's health, not the PR's code**.
The baseline therefore records the mode it was captured in:

```json
{
  "search_mode": "hybrid",
  "captured_at": "2026-07-13T00:00:00Z",
  "top_k": 10,
  "scores": { "single-session-user": 0.75, "overall": 0.83 }
}
```

Before any score is compared, the gate compares **modes**. If they differ — or
if either is unknown (a legacy baseline with no `search_mode`, or a server too
old to report one) — it does **not** compare the scores at all and exits `2`:

```
INCONCLUSIVE: baseline captured in mode 'hybrid' but this run served 'bm25-only'
(prod embedder degraded). Scores across modes are not comparable — this is NOT a
code regression. Re-snap the baseline once the embedder is healthy.
```

A cross-mode score drop can **never** return exit 1.

**Re-snap the baseline whenever the embedder's health state changes:**

```bash
python run_ci.py --retrieval-only --update-baseline   # records the current search_mode
```

### Observability

The same fail-open is now instrumented in Mesh, so a dead embedder pages
somebody instead of quietly degrading recall for days:

* `mesh_memory_embed_failures_total{op="recall"}` — embedder errors on the recall path
* `mesh_memory_recall_total{search_mode="hybrid"|"bm25-only"}` — how recalls were served

Alert on `rate(mesh_memory_embed_failures_total{op="recall"}[15m]) > 0`.

## Making the recall gate a required check

Do it in this order. Skipping step 1 wedges every PR.

1. **Baseline it, against the DEPLOYED server.** `baseline_retrieval.json` must
   exist, must be generated while the embedder is healthy (`search_mode:
   "hybrid"`), and must be snapped against a Mesh that actually reports
   `search_mode` — a baseline captured during a dense-arm outage bakes in the
   degraded BM25-only scores, and one snapped against an older server records
   `"unknown"` and can then only ever produce INCONCLUSIVE:
   ```
   python run_ci.py --retrieval-only --update-baseline
   ```
   Verify the file says `"search_mode": "hybrid"` before going further.
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
