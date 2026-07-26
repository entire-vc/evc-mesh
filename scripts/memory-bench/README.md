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
* the run's `search_mode` differs from the baseline's (see below);
* a **category** lost enough questions that its surviving sample is no longer
  comparable to the baseline's (`category-unmeasured`, see blindness #4);
* **no category was verdict-eligible** — the baseline's own run-to-run spread is
  wider than its scores everywhere, so nothing could have failed (see
  *[The baseline is a distribution](#the-baseline-is-a-distribution-not-a-sample)*).

Those last two are both "this category got no verdict", and the gate keeps them
apart on purpose. `category-unmeasured` is an **anomaly** — *this run* lost
questions to a harness error, so a single occurrence is enough to make the run
inconclusive. Spread-blindness is a **standing property of the baseline**: it is
true every run until the baseline is re-snapped, so firing on it per category
would pin the arm at INCONCLUSIVE for ever, which is the silent no-op rather
than a fix for one. It therefore forces exit 2 only when it holds for *every*
category and the run enforced literally nothing. The table shows the difference:
`⚠ UNMEASURED` should provoke someone, `ⓘ no verdict` should not.

**Every one of these rules applies to BOTH arms.** The mode gate and the
missing-baseline check were once written `if retrieval_only and …`, so they
guarded the required arm and skipped the advisory one — which then compared a
`hybrid` run against the legacy mode-less `baseline.json` and published
`✗ REGRESSION` for the difference on roughly half of all nightlies, while the
recall gate on the *same commit* reported retrieval healthy at `0.909 vs 0.864
PASS`. An invalid comparison does not become valid by being advisory; *advisory*
says who the verdict blocks, not whether the verdict may be false. The
counterpart is that the advisory arm now **alerts on its own blindness** too
(one tracking issue, deduped on reason kind) — otherwise tightening its
comparison rules would only have traded a visible wrong red for a silent no-op
on an arm nobody is required to look at.

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

Both arms write this shape. They did not always: `--update-baseline` built the
mode-scoped payload only inside `if retrieval_only:`, and the advisory arm's
`else` wrote a bare flat `{category: score}` dict. So re-snapping `baseline.json`
produced *another* mode-less file, and once the mode gate applied to that arm it
could never have been satisfied — the arm would have sat at INCONCLUSIVE for
ever, quietly, because it is not a required check. If you are ever tempted to
"just tighten the comparison" on an arm, check that its **writer** can produce a
baseline the tightened reader will accept.

## The baseline is a distribution, not a sample

The end-to-end arm answers **4 questions per category** with a chat model and
grades them with an LLM judge. One flipped answer therefore moves a category by
**0.25 — exactly the default tolerance.** This is not theoretical: across four
consecutive nightlies of *identical code*,

| category | 07-21 | 07-22 | 07-25 | 07-26 | spread |
|---|---|---|---|---|---|
| single-session-assistant | 1.000 | 1.000 | 0.250 | 1.000 | **0.750** |
| multi-session | 0.500 | 0.000 | 0.000 | 0.000 | 0.500 |
| overall | 0.591 | 0.500 | 0.364 | 0.409 | 0.227 |

A baseline snapped from **one** run records a coin toss. The one this replaced
(`fb957ed`, 2026-07-05, never re-snapped) happened to land on the **maximum ever
observed in 6 of 7 categories**, so the arm was measuring every night against a
lucky maximum — which is what actually made it red, independently of the mode
bug. Fixing the mode gate alone would have made 07-25 and 07-26 *comparable* and
still red.

So `--update-baseline` takes `--repeat N`: it runs the dataset N times, records
the **mean** as the baseline and the observed **max−min** per category as
`spread`, and the verdict threshold becomes

```
baseline − max(--tolerance, spread)
```

```json
{
  "search_mode": "hybrid",
  "captured_at": "2026-07-26T13:00:00Z",
  "top_k": 10,
  "n_runs": 3,
  "scores": { "multi-session": 0.167, "overall": 0.470 },
  "spread": { "multi-session": 0.500, "overall": 0.091 }
}
```

Where that threshold falls to zero or below, **no score can fall under it**, so
the category cannot produce a verdict. It is reported as `ⓘ no verdict` with the
numbers that make it so — never as a `✓`. A category that cannot fail must not be
counted among those that passed; that is manufactured coverage, the same disease
as a required check that skips. If *no* category is verdict-eligible the run
exits `2` (`GATE_REASON: no-eligible-category`): the comparison happened, but it
enforced nothing.

`--repeat` is rejected on a verdict run. Averaging passes to decide whether to go
red would hide the very variance the verdict needs to account for — quantifying
that variance is the baseline's job.

### Capture the baseline where it will be judged

Run the workflow with the **`update_baseline`** input (and `repeat`, default 3)
rather than snapping on a laptop:

```bash
gh workflow run memory-bench.yml -f update_baseline=true -f repeat=3
# then download the `memory-bench-baseline` artifact and commit baseline.json
```

Same runner, same `MESH_BENCH_KEY`, same chat/judge models as the nightly that
will be compared against it. A baseline captured against a different key or a
different model is not comparable to the run it judges — the same class of
not-comparable this workflow already exits `2` over.

### Observability

The same fail-open is now instrumented in Mesh, so a dead embedder pages
somebody instead of quietly degrading recall for days:

* `mesh_memory_embed_failures_total{op="recall"}` — embedder errors on the recall path
* `mesh_memory_recall_total{search_mode="hybrid"|"bm25-only"}` — how recalls were served

Alert on `rate(mesh_memory_embed_failures_total{op="recall"}[15m]) > 0`.

## The four ways this gate goes blind

The gate is required, which makes its *silence* more dangerous than its red. Each
of these has actually happened; each is now pinned by `test_gate_blindness.py`,
which CI runs on **every** PR (independently of the scope check below — a test
that guards against a wrongful no-op must not be skipped by that same no-op).

1. **A memory file missing from `MEMORY_PATHS`.** The gate no-ops and reports
   green having measured nothing, and "required" then certifies a run that never
   happened. #347 rewrote the authorization on memory `DELETE`, touching only
   `internal/handler/memory_handler.go`, and the gate never ran. **Adding a
   memory source file? Add it to `MEMORY_PATHS` in the same PR** — the test fails
   until you do.

2. **A transient Mesh restart mid-run.** `mesh-mcp` authenticates at startup and
   *exits* if that call fails, so the client sees only `Connection closed`, never
   the cause. A push to `main` triggers this bench **and** the backend deploy
   concurrently, so mesh-api restarts underneath the run: every question in that
   window errors, the error budget blows, and the gate reports INCONCLUSIVE — it
   switches itself off precisely on the commits that changed memory. A PR run can
   hit the same window whenever an unrelated merge deploys. Questions now retry
   against a fresh `mesh-mcp` (`CONNECT_RETRIES`), with a circuit breaker so a
   genuinely dead API fails fast instead of paying the backoff 24 times.

3. **An unreadable reason.** anyio reports failures as `ExceptionGroup: unhandled
   errors in a TaskGroup (1 sub-exception)` — a string that names the plumbing and
   hides the fault. `flatten_exc` inlines the leaves, and the CI step pipes stderr
   into `gate.log` (`2>&1`), so the uploaded artifact contains the *reason* the
   gate went blind and not just the fact that it did.

4. **A category scored on a shrunken sample.** The error budget is global
   (`--max-error-rate`, 10% of 24 questions ≈ 2), but every verdict is *per
   category*, and each category has only 4 questions. Nothing used to connect the
   two, so both dropped questions could land in the same category — which was then
   scored on 2 answers, compared against a baseline captured on 4, and printed as
   a bare `1.000 ✓` under "All categories within tolerance". That is not
   hypothetical: scheduled run `30191444472` (2026-07-26) lost `gpt4_4929293a` and
   `gpt4_7f6b06db`, **both `temporal-reasoning`**, and reported that category green
   on half its questions.

   The tolerance (0.25) is a claim about the *denominator* — "one question's worth
   of variance at n=4". At n=3 the quantum is 1/3 > 0.25, so a single unlucky
   answer clears the tolerance by itself. Both directions bite: a harness
   `BrokenResourceError` can manufacture `EXIT_REGRESSION` and block a merge no
   author can unblock, and a real 25% drop can hide inside the widened noise.

   So `decide_verdict` sets aside any category whose surviving sample no longer
   fits the tolerance, marks it `⚠ UNMEASURED`, and returns `EXIT_INCONCLUSIVE`
   (reason kind `category-unmeasured`) — never green, never a merge block. A
   category that lost *every* question disappears from the score dict entirely, so
   it is detected from the baseline side rather than by iterating the scores.
   Measured regressions still outrank this: otherwise one flaky question anywhere
   in the run would suppress every merge block in the repo. The table now prints
   the `Sample` column, because `1.000` from 4 questions and `1.000` from the 2
   that survived used to render identically.

   **The fix for a `category-unmeasured` run is to stop losing questions, not to
   widen the tolerance.** Widening it to cover n=2 would silence the alarm by
   making the gate unable to detect anything.

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

## What the end-to-end arm actually measures

Both arms ran the same night on the same haystack (run `30191444472`, 2026-07-26):

| category | retrieval hit@10 | end-to-end | lost at answer+judge |
|---|---|---|---|
| knowledge-update | 1.000 | 0.250 | −0.750 |
| multi-session | **1.000** | **0.000** | **−1.000** |
| single-session-assistant | 1.000 | 1.000 | 0.000 |
| single-session-preference | 1.000 | 0.250 | −0.750 |
| single-session-user | 0.500 | 0.500 | 0.000 |
| temporal-reasoning | 1.000 | 0.500 | −0.500 |
| **overall** | **0.909** | **0.409** | **−0.500** |

For `multi-session`, memory put a gold session in the top-10 for **4/4**
questions and `openai/gpt-4o-mini` answered **0/4**. The answer+judge stage
discards about half of everything retrieval delivers, and the same memory stack
scored **70.0%** on the full 500-question set with answers on `deepseek-chat` and
judge `gpt-4o`. So a low number on this arm is, by default, a statement about the
**cheap chat model**, not about Mesh memory — which is the other half of why it
is advisory. Read the recall gate first: if retrieval is green and this arm is
not, the loss is downstream of memory.

The bench writes its haystack to the shared workspace under `bench-<qid>` /
`lme-bench` tags and deletes it in a `finally` block. If you ever see `lme-bench`
memories surviving a run, cleanup was skipped — they pollute real agents' recall.
