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
  "spread": { "multi-session": 0.500, "overall": 0.091 },
  "sample_sizes": { "multi-session": [4, 4], "overall": [22, 24] }
}
```

### A baseline has to state its own denominator

`sample_sizes` records how many questions each figure was measured on. Without
it the sample gate is one-sided: it catches a **run** whose sample shrank, and
is structurally blind to a **baseline** captured on a smaller one.

That is not a hypothetical. `baseline_retrieval.json`'s `temporal-reasoning: 1.0`
rests on **2** of that category's 4 questions — the other two build an invalid
memory key and have failed in every run since 07-21 (evc-mesh#362). Fix that bug
and a fully-measured 4-question run is compared against a 2-question figure: one
miss reads as −0.25, two as `✗ REGRESSION`, on a **required** check — a red for a
change in sample, not in quality, blocking the very PR that restores the sample.

So when both sides state their denominator and the two differ, the category is
set aside as `⚠ UNMEASURED` rather than compared. Baselines written before this
field existed record nothing, and an absent denominator reads as *unknown* — never
as *matching*. `--update-baseline` also warns at capture time when it is snapping
a figure on an incomplete sample, while that is still cheap to fix.

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
rather than snapping on a laptop. **`baseline_arm`** picks which file:

```bash
# advisory arm → baseline.json, artifact `memory-bench-baseline`   (paid)
gh workflow run memory-bench.yml -f update_baseline=true -f repeat=3

# required arm → baseline_retrieval.json, artifact `recall-gate-baseline`  (free)
gh workflow run memory-bench.yml -f update_baseline=true -f baseline_arm=retrieval -f repeat=3
```

Same runner, same `MESH_BENCH_KEY`, same embedder and chat/judge models as the run
that will be compared against it. A baseline captured against a different key or a
different model is not comparable to the run it judges — the same class of
not-comparable this workflow already exits `2` over.

`baseline_arm=retrieval` re-snaps in the recall job and does **not** start the paid
end-to-end job; `both` re-snaps each in its own arm.

#### The required arm refuses a capture it cannot stand behind

`baseline_retrieval.json` is the threshold of a **required** check, so
`--retrieval-only --update-baseline` does not write it at all unless the run was
complete (`24/24`) and served in `hybrid`. It exits `2` with
`GATE_REASON: capture-refused` and leaves any existing baseline untouched.

The warn-and-write behaviour that the advisory arm keeps is what produced the
`temporal-reasoning: 1.0` on 2 of 4 questions above: 2 failures out of 24 is 8.3%,
just under the 10% `--max-error-rate`, so the run was never declared inconclusive
and the warning went into a log nobody read again for six days. On the advisory
side that trade is still right — its baseline blocks no merge and each pass costs
money, so refusing there buys no baseline at all instead of a flawed one.

`--allow-partial-capture` overrides the refusal. It is for the case where a
degraded baseline genuinely beats none; say in the commit message which figures
rest on an incomplete sample, since `sample_sizes` will then record a denominator
that permanently sets those categories aside as not-comparable.

#### Check the artifact before committing it

The refusal above covers the *run*. Two defects survive a clean run and live in
the *file*, which is what the gate reads for weeks, so check the artifact after
downloading it and before committing:

```bash
python3 scripts/memory-bench/check_captured_baseline.py path/to/baseline_retrieval.json
```

* **No `sample_sizes`.** The denominator guard reads that field; a file missing
  it — or written under the superseded name `samples` — loads as `{}` and the
  guard is inert *by design*, with nothing printed. That is how
  `temporal-reasoning: 1.0` sat on 2 of 4 questions for six days.
* **A category blinded by its own spread.** Thresholds are
  `baseline - max(tolerance, spread)`. Where that reaches ≤0 the category is
  ruled ineligible and prints `ⓘ no verdict` for the life of the baseline. A
  capture that does this exits `0`.

Both rules are imported from `run_ci.py` rather than restated, so the checker
cannot drift from the gate. `test_check_captured_baseline.py` runs it against
the committed file on every PR.

### Observability

The same fail-open is now instrumented in Mesh, so a dead embedder pages
somebody instead of quietly degrading recall for days:

* `mesh_memory_embed_failures_total{op="recall"}` — embedder errors on the recall path
* `mesh_memory_recall_total{search_mode="hybrid"|"bm25-only"}` — how recalls were served

Alert on `rate(mesh_memory_embed_failures_total{op="recall"}[15m]) > 0`.

### Per-question results: `results/recall_gate.json`

`results/` existed from the gate's first commit and nothing ever wrote to it —
the only artifact was `gate.log`, whose per-question line carried a tick or a
cross. So `hit@10 = 0` was the same artifact whether the gold session ranked
12th or was never retrieved at all, and those have different causes and
different fixes. Answering "which one?" cost a live probe against prod
(#c6b1ecee) instead of a five-minute read.

Both arms now write every question's result, plus the run envelope
(`search_mode`, `top_k`, `repeat`, `scores`, `sample_sizes`), to
`results/recall_gate.json` (`results/longmemeval.json` for the advisory arm).
Both are already inside the `upload-artifact` paths. Three fields beyond the
verdict:

| field | meaning |
|---|---|
| `gold_rank` | 1-based position of the first gold session in the **full** tag-filtered ranked list, not the `top_k` window. `null` = no gold row reached the client at any `k`. |
| `rows_returned` | How many of this question's fixtures survived to the client. |
| `haystack_size` | How many were stored. `rows_returned` alone is not interpretable: 32 means nothing until you know it is 32 of 45. |

`gold_rank` is 1-based so that `0` is impossible and `is None` is the only
"absent" — a sentinel of `0` collides with 0-based counting, and a sentinel of
`k` is indistinguishable from a row ranked last inside the window.

Two things worth knowing before you read these numbers:

* **`hit` and `gold_rank` cannot disagree.** The scored window is a prefix of
  the retained list, so a hit is exactly a gold row at a rank `<= top_k`.
  `test_gold_rank.py` pins that across every position. Without it the artifact
  could ship a rank that contradicts the score printed next to it.
* **`null` means "did not reach the client", not "not indexed".** `scope` and
  `tags_any` are post-filters over a workspace-wide candidate pool
  (#2c087b2a), so a fixture can be indexed perfectly and still never arrive.
  Read `gold_rank` together with `rows_returned` vs `haystack_size`: when
  `rows_returned < haystack_size` the pool truncated, and that — not the index
  — is the first thing to suspect.
* **Check the CEILING before you call it truncation.** `rows_returned` cannot
  exceed the page the server may return, so a shortfall has a second, entirely
  structural cause that looks identical in the artifact. The effective window is
  `RECALL_CANDIDATE_LIMIT` minus `graphBoostReserve(limit)` = limit/4 whenever
  graph boost is on, i.e. **limit\*3/4, not limit**. Measured: at limit 50 the
  window was 38 and `rows_returned` was pinned at exactly **38 on all 24
  questions** — and the ratio 97.1% → 79.3% was caused by a *correct* fix
  (#384), not a regression. So: **`rows_returned` constant across differing
  haystacks means you are reading the window, not the pool.** Varying *with* the
  haystack is the healthy shape; varying with the unrelated fleet corpus is the
  real leak (#2c087b2a). `test_bench_window.py` pins the effective window against
  the real dataset.
* **A few rows short is normal, and it is not the index.** `rows_returned` is
  `|dense ∪ sparse|`, and the dense arm cannot see a memory until its embedding
  lands: `Remember()` commits the row and then does `go s.embedAndStore(...)`,
  while `vectorCandidateIDs` requires `embedding IS NOT NULL OR EXISTS(chunk)`.
  This harness stores the haystack and searches with **no settle delay**, so the
  newest rows are still in flight. Measured over two runs (30525387007 limit 50,
  30522592913 limit 80): `dense_rows < haystack_size` on **24/24 both times**,
  deficit **mean 6.4, stdev 1.6, range 3–10**, uncorrelated with haystack size
  (r = −0.36 / −0.09) and **unchanged by the limit** (6.46 vs 6.38) — the
  signature of a fixed in-flight tail, not of a window cut. Zero embed errors in
  either run's `api.log` during the measurement, so nothing failed; it had not
  finished. Consequence: `rows_returned == haystack_size` on all 24 is **not
  reachable by any window setting**, and an AC that demands it is unattainable
  for reasons that have nothing to do with retrieval quality.

Nothing here feeds scoring, thresholds or the exit code. The artifact is
written *before* the verdict branches, so it still exists on an INCONCLUSIVE or
a refused capture, and a failure to write is logged but never changes the
verdict: an observability artifact must not be able to fail a required check.

## The eight ways this gate goes blind

The gate is required, which makes its *silence* more dangerous than its red. Each
of these has actually happened; each is now pinned by a self-check
(`test_gate_blindness.py`, `test_gate_arm.py`, `dense_arm_control.py --selftest`)
that CI runs on **every** PR — independently of the scope check below, because a
test that guards against a wrongful no-op must not be skipped by that same no-op.

Ways 1–5 end in a no-op or an INCONCLUSIVE: the gate either did not run or could
not measure. **Way 6 is different in kind** — the gate ran, measured cleanly,
compared against a valid baseline, and reported an accurate number *about the
wrong program*. A green that certifies the wrong object is worse than an
INCONCLUSIVE, because INCONCLUSIVE at least says so. **Way 7 is different again**
— the gate never reached a verdict, because it was competing with itself for the
one resource it measures, and ran out of clock.

Ways 6 and 7 share a root, which is why they arrived together: **the single live
production server was treated as though this gate owned it.** From that one
assumption follow both "we measured the wrong binary" and "we measured nothing,
because four of us measured at once".

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

5. **A deterministic failure parked under a budget built for transient ones.**
   `--max-error-rate` forgives 10% of a run because the questions it forgives get
   measured again next run. That premise fails the moment a question errors for a
   reason that cannot change: `gpt4_4929293a` and `gpt4_7f6b06db` failed in **13
   of 13 job executions across 6 days**, 2/24 = 8.3% sat under the 10% budget
   every time, both arms reported a clean verdict over 22 questions, and the error
   line — printed in 100% of runs — read as furniture. A per-run percentage cannot
   express "the same 8%, for ever".

   Each errored question is now classified `transient` or `persistent`, from a
   single run and with no cross-run state:

   - the message does not match the client's own transient predicate (the client
     does not retry these by construction, so next run fails identically), **or**
   - the message *does* look transient but the question spent its whole retry
     allowance and every attempt died — a permanent failure wearing a transient
     message, which is what `BrokenResourceError` was here. (Exhausting the
     allowance only counts when there was one: with the circuit breaker open it is
     1 attempt of 1, which proves nothing.)

   One predicate serves both the retry policy and the budget — a second copy would
   drift, and the two halves disagreeing about "transient" is how this hid.
   `--max-persistent-errors` (default **0**) turns any persistent error into
   INCONCLUSIVE with reason kind `persistent-errors`, which the workflow alerts on
   separately because the fix is a different person's (fix the harness or the
   dataset; no re-snap and no waiting clears it).

   It **never downgrades a REGRESSION.** These questions failed on every run for
   six days; if a persistent error could demote a measured drop to INCONCLUSIVE,
   the gate would have stopped blocking bad memory PRs altogether — a bigger hole
   than the one it closes. Ranking stays `REGRESSION > INCONCLUSIVE > OK`.

6. **Measuring production instead of the branch under review** (#2a079432, fixed
   by [ADR-0003](../../dev-docs/adrs/0003-recall-gate-measures-the-branch.md)).
   The required check took its target from `secrets.MESH_API_URL` — the deployed
   server — and nothing in the workflow ever built `cmd/api` from the branch (the
   two `go build` steps compile `mesh-mcp`, the *client*, from another repo). So
   it answered "how good is recall on the server that is already running?" while
   presenting itself as an answer to "does this PR make recall worse?" Those
   coincide only when the PR is already deployed, which for a PR is never.

   **What it cost — incident #b052cdda.** PRs #388, #389 and #391 — the chunking
   epic's schema, write path and backfill — each got a green `Memory recall gate`
   measured against the pre-chunking binary. After merge and deploy it emerged
   that the write path had moved vectors into `memory_chunks` while no read path
   consumed them, so `memories.embedding` was NULL for every new memory and those
   memories left the dense arm entirely. The check that exists to catch exactly
   that could not have seen it — three times in a row, each time reporting green.

   The gate is now two arms with two targets (see the ADR for the table).
   `Memory recall gate` builds and boots `cmd/api` from the PR head
   against an ephemeral postgres and a local `BAAI/bge-small-en-v1.5`, and is the
   **required** context — it keeps the old job's *name* on purpose, see below.
   `Memory recall canary (prod)` keeps the old target and
   answers the question it could always answer — "is what we deployed still
   good?" — on `push: main`, nightly and dispatch. Their scores are **not
   comparable** (different embedder, different corpus), so they have separate
   baselines, each recording its own `arm`, and a baseline used in the wrong arm
   is refused (`arm-mismatch`) rather than silently compared.

   **A second, independent blindness surfaced from the same run** (30316983402)
   and is fixed alongside: the scored dataset cannot detect the loss of the dense
   arm *at all*. Every LongMemEval question asks about a fact stated in roughly
   the words the question uses, so BM25 answers them alone. That run reported
   `single-session-user 1.000`, `overall 0.9583` — its best numbers ever — while
   `VectorSearch` returned **zero rows for the whole haystack**, and still
   announced `search_mode: hybrid, degraded: false` (the mode reports that the
   embedder answered, not that the dense arm returned anything).

   `dense_arm_control.py` is the missing question: one query whose gold session
   shares **no content word** with it, so BM25 cannot reach it, surrounded by
   distractors that carry the query's vocabulary on purpose. Measured against the
   CI embedder, gold is cosine rank 1 with a +0.083 margin and keyword rank
   nowhere.

   Crucially the branch job runs it **twice**: once normally (gold must be
   found), then again with the embedder killed and the corpus untouched, where
   gold must be **missed**. That second run is the positive control on the
   control — the step missing every previous time this harness went blind. An
   assertion whose ability to go red was never demonstrated is not evidence, and
   here the demonstration happens on every run, against the same code the green
   came from.

7. **Starving itself out of a verdict on the one server it shares** (2026-07-28,
   fixed by the `concurrency:` block on the prod arms). Ways 1–6 are all about a
   run that finished. This one never does.

   `memory-bench.yml` had no `concurrency:` at all — not on the workflow, not on
   any job — while every prod-facing job ingests a haystack into the single
   `secrets.MESH_API_URL` and searches it. Runs did not queue; they overlapped
   *inside the server* and slowed each other down. A pass that takes ~17 min
   alone took 30m16s with three others in the window, hit `timeout-minutes: 30`,
   and was killed at **14 of 15 checks green with nothing red**.

   **The failure signature is the part worth memorising: `conclusion: cancelled`,
   not `failure`.** A required check that is cancelled has produced no verdict,
   but it does not read like a defect — it reads like an infra hiccup, so the
   instinct is to re-run it, which puts a fifth run into the same contended
   window. Duration is not a diagnosis: the same 30 minutes means "this branch is
   slow" and "four of us are in the API at once", and only the second is true
   here.

   **Who was in the window matters more than the fix.** Three of the four
   competing runs were *this task's own* proof runs — the experiments
   demonstrating that the gate is blind — and the check they strangled was
   @linus's PR #393, the fix for the live regression (#b052cdda) that the
   blindness had let through. Evidence-gathering about a safety net is not
   exempt from the net's costs; it is one of the loads it has to survive.

   Two properties of the fix, both load-bearing:

   - **`cancel-in-progress: false`.** The template default is `true`, which would
     abort a peer mid-run to make room. That is the same lost verdict by a
     different route, and strictly harder to diagnose: a timeout at least leaves
     `30m16s` in the log, whereas a pre-emption leaves a run that simply stops.
     Serialise; never pre-empt.
   - **The required arm is deliberately *not* in the group.** It builds its own
     `cmd/api` over its own `postgres:16` service container, so it contends for
     nothing and must never be made to wait on the canary — a required check that
     queues behind an advisory one has handed its latency to a job nobody is
     blocked on. This is the second dividend of ADR-0003: moving the gate off the
     shared prod removed the contention as well as the wrong-target defect.

   Accepted, so it is not later rediscovered as a bug: a concurrency group holds
   one running plus one pending job, so a third arrival supersedes the pending
   one. For a post-deploy canary that is the right trade — the newer commit is
   the one worth measuring — and it is only safe because this arm is not
   required.

   **What `concurrency:` cannot serialise is a person.** It orders the runs the
   workflow starts; it does nothing about someone deciding to `workflow_dispatch`
   a proof run on top of a colleague's required check. `prod_window.py` is the
   probe for that decision, and it has already failed twice in the two ways worth
   naming:

   - **It fail-opened.** The first version was a shell one-liner wrapping a
     Python fragment that had a syntax error. It printed nothing, the caller's
     `[ -n "$busy" ]` was false, and empty output read as an empty window — so it
     dispatched into three live runs, which is the incident it was written to
     prevent. "Zero runs in flight" and "I could not count the runs in flight"
     are different facts that empty output renders identically. Hence
     **0 = clear, 1 = busy, 2 = PROBE FAILED**, everything unresolvable counted
     busy, and a negative control (a filter value that cannot match must return
     nothing) so that a zero means the filter ran.
   - **It was blind to which arm it was looking at.** The branch arm inherited
     the name `Memory recall gate` because a required context is matched as a
     literal string (see below). So that one name means the *prod* arm on a ref
     carrying the old one-arm workflow and the *branch* arm — ephemeral
     `cmd/api`, own `postgres:16`, contending for nothing — on a ref carrying the
     new one. A name-blind probe therefore calls a run busy that cannot touch
     prod, and while any PR of this file is iterating the window is never clear:
     safe, and useless in the direction it is consulted. The discriminator needs
     no second call — a run whose job list contains `Memory recall canary (prod)`
     is on the two-arm workflow, so its `Memory recall gate` is the branch arm.

   The rename that made the required check report is the same edit that made the
   name stop identifying the arm. Worth stating plainly, because the fix for way
   6 is what created this.
8. **`search_mode: hybrid` reported over a dense arm that returned nothing.**
   The mode is set when the dense arm *ran* end-to-end — embedder alive, query
   vectorised, `VectorSearch` returned no error. It says nothing about that arm
   having matched a single row, and a `VectorSearch` that matches zero rows
   returns `(nil, nil)`: no error, so mode stays `hybrid` and `degraded` stays
   `false`.

   Run `30316983402` is the whole failure in one line. Every bench fixture had
   been created after the chunked-embed deploy (`625ee28`), so every
   `memories.embedding` was `NULL`, and `VectorSearch` filters
   `embedding IS NOT NULL` — **zero rows across the entire haystack**. The gate
   reported `single-session-user 1.000`, `overall 0.9583`, `search_mode: hybrid`,
   `degraded: false`: the best result ever recorded, measured on a corpus whose
   dense arm was not there. Nothing in the envelope contradicted it, because
   nothing in the envelope counted rows.

   The search envelope now carries `dense_rows` (and `sparse_rows`) beside
   `search_mode`, and `resolve_dense_arm_status` reads them:

   - **every** hybrid question reporting `dense_rows == 0` ⇒ `EXIT_INCONCLUSIVE`,
     reason kind `dense-arm-empty`. Never `REGRESSION` — an unembedded corpus is
     not something a PR author can fix, and a required check that reds on it is a
     check that gets bypassed.
   - **all of them, not any one.** The vector arm draws from a relevance-neutral
     candidate pool, so it returns rows for any query while *anything* in the
     workspace is embedded. One zero among non-zeros is a query oddity, not a
     lost arm.
   - **`bm25-only` zeroes are not evidence.** A deployment with no embedder has
     no dense arm to lose; that case is the mode gate's, and one cause must not
     raise two alerts with two different owners.
   - **A server that does not report the field changes nothing** (status
     `unknown`, verdict untouched, and the gate says out loud that it could not
     check). This is load-bearing, not politeness: if a missing field read as
     zero, this gate would wedge the prod arm at INCONCLUSIVE from the moment it
     merged until the server was deployed — broken during exactly the window it
     exists to fix.

   `--update-baseline` refuses an `EXIT_INCONCLUSIVE`-worthy capture here too. A
   floor snapped with no dense arm is a BM25-only floor recorded as the hybrid
   standard: every later healthy run clears it, so the gate goes permanently
   green *and* permanently blind — this failure installed as its own baseline.

   Pinned by `test_gate_dense_arm.py` (harness side) and
   `memory_recall_stats_test.go` / `memory_handler_dense_rows_test.go` (server
   side). Note which direction the positive controls run: the tests that matter
   are the ones proving `dense_rows` can be **non-zero** and that back-compat
   still lets a real regression exit 1 — a gate hard-wired to INCONCLUSIVE would
   pass every "empty arm is caught" test on its own.

### …and the way the pins themselves go blind

Every entry above ends "pinned by `test_…`". That sentence is about a file, and a
file is not a guard — an *invocation* is. On 2026-07-28, counting invocations
instead of reading the tree turned up two self-checks that no workflow step ever
ran:

| self-check | pins | referenced by CI |
|---|---|---|
| `test_gate_dense_arm.py` | way 8 (`dense_rows`) | **0 times** |
| `test_check_captured_baseline.py` | `--update-baseline` refusals | **0 times** |

Both were green locally and both were credited by name in this README. They
arrived with the PRs that wrote them and nobody added the line to
`memory-bench.yml`, so for their whole life the protection they describe did not
exist. That is way 1 — a guard that no-ops to green — applied to the guards.

`test_the_required_job_invokes_every_self_check` now derives the expected set
from the directory (any `test_*.py`, plus any script offering `--selftest`) and
requires each to appear as a real `python scripts/memory-bench/… ` line inside
the **required** job. Two details are load-bearing, both found by mutating it:

- **Derived, never listed.** A hand-kept list in the test needs the same edit
  that gets forgotten in the workflow, so it would reproduce the bug inside the
  test written to catch it.
- **Invocation, not mention.** The first version asked `name in job_block` and
  passed with the invocation *deleted*, because the step's own comment names two
  of the scripts. A source-grep survives an orphaned call; only a line that would
  actually execute counts.

## Making the recall gate a required check

The required context is **`Memory recall gate`** — the branch arm, the one that
measures the PR. `Memory recall canary (prod)` must never be required: it does not
run on `pull_request` at all (ADR-0003), so requiring it would block every PR
forever.

### Why the branch job inherited the old job's name

It is already required, and **a required context is matched against the check-run
name as a literal string.** That makes the name a public interface, and renaming
it is not a refactor — it is a two-sided outage:

- Name the branch job something new (`… (branch)`) and leave protection alone →
  the required `Memory recall gate` is produced by nobody on any PR carrying this
  file. `mergeStateStatus: BLOCKED`, permanently, and `enforce_admins: true`
  closes the override to the repo owner too. This PR sat in exactly that state at
  14 green checks, and no amount of re-running fixed it (cf. evc-mesh#320).
- Move protection to the new string *first* → every **other** open PR is instantly
  BLOCKED, because they still produce the old name.

Both orders need an admin edit and leave a window in which memory is gated by
nothing. Inheriting the name is one line in the PR, costs no admin action, and
has no window. Job **ids** may change freely (`recall-gate-branch` is untouched);
`name:` may not.

The steps below are what remains — capture the baseline — and are what turns a
required check from *present* into *enforcing*. Skipping step 1 leaves the gate
required but running on `no-baseline` → INCONCLUSIVE, which never blocks: real,
but weaker than the name promises.

1. **Baseline it, in the arm that will be judged.** Each arm has its own file and
   they are not interchangeable:

   | arm | file | how to capture |
   |---|---|---|
   | branch (required) | `baseline_retrieval_branch.json` | dispatch on `main` with `update_baseline: true`, `baseline_arm: retrieval-branch`; download the `baseline-retrieval-branch` artifact and commit it |
   | prod (canary) | `baseline_retrieval.json` | dispatch on `main` with `update_baseline: true`, `baseline_arm: retrieval` |

   > This table said something else until 2026-07-28 — "dispatch the workflow on
   > `main`; the branch job writes it with `--arm branch`" — and it was **inert**.
   > The branch job does run on dispatch (it carries no `if:`), but nothing in it
   > ever passed `--update-baseline`, and there was no upload step, so following
   > the instruction produced a normal judging run and no artifact. It would have
   > been discovered by whoever tried to enable the gate after the merge. Both the
   > capture step and `test_the_required_arm_can_capture_its_own_baseline` now
   > exist; the test asserts the *invocation*, since `--arm branch` on the judging
   > call satisfies a substring check while capturing nothing.

   The file must be generated while the embedder is healthy (`search_mode:
   "hybrid"`) — a baseline captured during a dense-arm outage bakes in the
   degraded BM25-only scores, and one snapped against an older server records
   `"unknown"` and can then only ever produce INCONCLUSIVE. Verify it says
   `"search_mode": "hybrid"` **and** the right `"arm"` before going further; a
   file dropped into the other arm's path is refused (`arm-mismatch`), which is
   an INCONCLUSIVE nobody can clear from a PR.
2. **Confirm it can actually pass** — at least one green `Memory recall gate` run
   on a real PR, with a baseline present. A green on `no-baseline` proves the
   plumbing, not the enforcement.
3. **Nothing to do on branch protection.** `Memory recall gate` is already the
   required context and the branch job already produces it; the arms swapped
   underneath a name that never moved. Should the contexts ever need editing,
   note that the API **replaces** rather than appends, so every context has to be
   passed in one call:
   ```
   gh api -X PATCH repos/entire-vc/evc-mesh/branches/main/protection/required_status_checks \
     -f 'contexts[]=Lint' -f 'contexts[]=Test' ... -f 'contexts[]=Memory recall gate'
   ```
   Two standing invariants: the context must match the **check-run name on a PR**
   verbatim, and the workflow must have **no `paths:` filter** on `pull_request` —
   the gate runs on every PR and no-ops internally when no memory path changed
   (a required context born inside a path filter never reports, which is the same
   permanent BLOCKED by another route).

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
discards about half of everything retrieval delivers. So a low number on this arm
is, by default, a statement about the **cheap chat model**, not about Mesh memory
— which is the other half of why it is advisory. Read the recall gate first: if
retrieval is green and this arm is not, the loss is downstream of memory.

The claim above rests **only** on the same-night, same-haystack table: one run,
one corpus, retrieval and answering measured off the same fixtures, so the chat
model is the only thing that differs between the two columns. That is what makes
it an isolation rather than a comparison.

An earlier revision of this file also cited a **70.0%** full-500 score as
corroboration. It has been removed rather than sourced. The number is real and
reproducible — `350/500` in
`~/bench/metronix-memory/benchmarks/longmemeval/results/full500.jsonl.eval-openai_gpt-4o`
on the Mac Mini — but it was measured against **Metronix MCP memory**
(`github.com/mtrnix/metronix-memory`), a different product, with a different chat
and judge model. It changes the memory system *and* the models at once, so it
cannot isolate either, and citing it here read as if this stack had scored it.
Do not reintroduce it without both variables held fixed.

The bench writes its haystack under `bench-<run nonce>-<qid>` / `lme-bench` tags
and deletes it in a `finally` block. The nonce is load-bearing and this paragraph
used to omit it: tenancy follows the **credential**, so every bench process
holding the same `MESH_BENCH_KEY` — every branch, either arm, CI or laptop —
writes into one workspace, and pre-nonce names made concurrent runs delete each
other's haystacks mid-measurement (rationale: `_resolve_run_nonce` and the
"Per-RUN isolation" block in `mesh_client_stdio.py` — the nonce landed in code
with no mention here at all, which is half of why this paragraph went stale).

Cleanup is best-effort by construction: `_sweep` needs a live connection and the
case it exists for is the connection dying. Rows a dead run abandoned are
reclaimed by `_gc_orphans`, which selects on **age** (`lme-bench` umbrella tag,
older than `BENCH_ORPHAN_GC_MIN_AGE_HOURS`, default 2h) and never on ownership —
a peer run's live fixtures are also "not mine", so an ownership rule would
reintroduce the cross-run deletion the nonce exists to stop. Audit with:

```
GET /api/v1/memories?scope=workspace&tags_any=lme-bench&min_importance=0
```

Rows there while no bench is running mean cleanup *and* the collector were both
skipped. **Run that query against the fleet's key too, not only the bench key.**
`_gc_orphans` sweeps the tenant it authenticates as, so a probe run with a fleet
credential leaves fixtures the collector can never reach: measured 2026-07-30,
8 pre-nonce rows (`bench-89527b6b-s0..s7`, no `expires_at`) written into the
fleet workspace at 2026-07-27T01:21Z by a local probe on an older checkout, four
days after the nonce landed. `scope=workspace` memories get no default TTL, so
those surface in real agents' `recall()` indefinitely. Point local probes at the
bench key.

Tags carry the question id verbatim; memory **keys** carry a sanitized form
(`sanitize_key_component`), because Mesh validates `key` against
`^[a-z0-9][a-z0-9-]*[a-z0-9]$` and rejects a non-conforming one with 400. Two of
the 24 LongMemEval ids (`gpt4_4929293a`, `gpt4_7f6b06db`) carry an `_`; before
this was folded, both questions died on their first `remember` and the
`temporal-reasoning` category was permanently scored on 2 of its 4 questions. An
id that is already key-safe is passed through unchanged; one that had to be
folded gets a digest of its raw form appended. This matters because `remember`
UPSERTs on the key: a collision does not error, it silently overwrites another
question's haystack, and both are then scored against half their evidence.

The two branches are kept in **disjoint output spaces** — folded ids always end
`-<8 hex>`, and an id already shaped that way is refused the passthrough — so two
passed-through ids differ because their raw ids differ, and two folded ids sharing
a slug differ by the digest of their raw form. The residual, stated rather than
glossed: a 32-bit digest collision between two ids with the same slug is *not*
excluded by construction, which is why the test asserts distinctness across the
real dataset rather than trusting the argument.
