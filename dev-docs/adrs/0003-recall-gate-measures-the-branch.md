---
created: 2026-07-28T03:40+03:00
updated: 2026-07-28T03:40+03:00
author: Riker
status: accepted
project: mesh
type: adr
tags:
  - mesh
  - ci
  - memory-eval
  - recall
  - testing
---

# ADR-0003 — The recall gate must measure the branch it is gating, not production

**Status:** Accepted · implemented in the same change
**Task:** [2a079432](https://mesh.entire.host/t/2a079432-db11-403c-bf4d-4550ab516ece)
**Found by:** verify pass on [84b0694d](https://mesh.entire.host/t/84b0694d), epic [b052cdda](https://mesh.entire.host/t/b052cdda) (memory chunking)
**Supersedes nothing.** Extends the two-arm split decided 2026-07-13 (`canon-memory-quality-ci-gate-two-arm-split`, #8e8ad761), which split the gate by *cost*; this one splits it by *what is under test*.

---

## Context

`.github/workflows/memory-bench.yml` runs `Memory recall gate` on every PR, and
that check is required on `main`. It takes its target from
`MESH_API_URL: ${{ secrets.MESH_API_URL }}` — `https://mesh.entire.host`, the
deployed production server. No job in the file builds or boots `cmd/api`: the
only `go build` steps compile `mesh-mcp`, the **client**, out of a different
repository (`entire-vc/evc-mesh-mcp`). There are no `services:`, no compose file,
no local override.

So the required check answers "how good is recall on the server that is already
running?" while presenting itself as an answer to "does this PR make recall
worse?" Those coincide only when the PR is already deployed, which for a PR is
never.

### The price already paid

PRs #388, #389 and #391 — the chunking epic's schema, write path and backfill —
each got a green `Memory recall gate`. Production was running the pre-chunking
binary during all three runs, so the gate measured code none of them contained.

After merge and deploy it emerged that the write path had moved vectors into
`memory_chunks` while no read path had been written to consume them, so
`memories.embedding` was NULL for every newly written memory and those memories
fell out of the dense arm entirely. The required check that exists precisely to
catch that could not have seen it. Not "missed it" — could not, by construction.

### Why this is a new failure class

`scripts/memory-bench/README.md` documents five ways the gate goes blind. Every
one of them is the gate **not running** (a path missing from `MEMORY_PATHS`) or
**running but unable to measure** (a mid-run restart, an unreadable error, a
shrunken sample, a permanently-failing question). All five end in a no-op or an
INCONCLUSIVE.

This one ends in a **confident green**. The gate ran, measured cleanly,
compared against a valid baseline, and reported an accurate number — about a
different program. A green that certifies the wrong object is worse than an
INCONCLUSIVE, because INCONCLUSIVE at least says so.

---

## Options considered

### 1. Build and boot the API from the branch, for every PR

Spin up postgres, run migrations, `go build ./cmd/api`, boot it, point the bench
at localhost. Honest by construction: the binary under test is the binary being
reviewed.

The task that opened this ADR costed it as expensive and awkward — "needs an
embedder reachable from the runner or a stub; slower; and it diverges from
'baseline snapped against deployed prod'". Measurement changed all three
estimates:

* **No pgvector.** `migrations/20260315044_memories_vector_columns.sql` stores
  embeddings as `TEXT` and does the cosine in application code, explicitly so
  that "pgvector is not required at the DB level". `memory_chunks` keeps that
  shape. A stock `postgres:16` service is enough.
* **The recipe already exists in this repo.** `ci.yml`'s `integration` job
  already does postgres service → `goose -dir migrations up` →
  `go build -o /tmp/mesh-api ./cmd/api` → boot → run tests against it. This is
  not new infrastructure, it is an existing pattern applied to one more job.
* **It is FASTER, not slower.** Measured 2026-07-28 on the same 24-question
  dataset: ~22 s/question against a local branch build, versus ~42 s/question
  against prod (the 17 min figure in the workflow header). Most of the prod
  run's cost is WAN round-trips per `remember`, and the haystack is ~1100
  memories.

Its real cost is the one the task under-weighted: it **cannot use prod's
embedder**, so its scores are not comparable to prod's, and it needs a baseline
of its own.

### 2. Keep the prod run, demote it to a post-deploy canary

Rename it, move it off `pull_request`, drop the required context, alert on
`push: main` after the deploy.

Cheap and honest about what it measures. But it leaves **nothing** blocking a
memory regression before merge — the hole stays open, it is just correctly
labelled. Rejected as a sole measure.

### 3. Hybrid — branch-build gate on PRs, prod canary after deploy

Option 1 as the required pre-merge check, option 2 as an advisory post-deploy
one.

---

## Decision

**Option 3.** Two arms with different jobs, different targets and different
baselines:

| | `Memory recall gate (branch)` | `Memory recall canary (prod)` |
|---|---|---|
| target | `cmd/api` built from the PR head, on localhost | `secrets.MESH_API_URL` (deployed) |
| database | ephemeral `postgres:16`, freshly migrated | prod |
| embedder | `BAAI/bge-small-en-v1.5`, local CPU, 384-dim | prod's |
| agent key | minted per run by `ci_bootstrap.py` | `secrets.MESH_BENCH_KEY` |
| baseline | `baseline_retrieval_branch.json` | `baseline_retrieval.json` |
| trigger | every PR | `push: main` + nightly |
| status | **required** | advisory, alerts on regression |
| answers | "does this change make recall worse?" | "is the thing we deployed still good?" |

Each arm keeps the exact question it can actually answer. The prod arm was never
wrong about *its* question; it was wrong about being asked the PR's.

### The embedder must be a real model, not a hash stub

The cheap way to give a CI-boot API an embedder is a deterministic hash of the
input text. It would have made this gate blind to the exact class of regression
it exists for, and the failure would have looked like success.

Cosine similarity between hash vectors is noise: a semantically related memory
is no nearer the query than an unrelated one. The dense arm then contributes
nothing to the ranking *by construction*, so severing it changes no score — and
the sabotage branch this ADR requires as proof would have come out **green**,
which reads as "the gate is fine" rather than "the gate cannot see".

So the requirement is not determinism, it is *carrying real semantic signal,
reproducibly*. `scripts/memory-bench/ci_embedder.py` serves
`BAAI/bge-small-en-v1.5` (fastembed, ONNX, CPU) at the OpenAI-shaped endpoint
`internal/embedding/openai.go` already speaks.

Positive control, measured 2026-07-28 — the check a hash stub fails:

```
query:      "What kind of dog does the user own?"
related:    "I adopted a golden retriever puppy last weekend…"   cos = 0.6932
unrelated:  "The quarterly revenue forecast for Frankfurt…"      cos = 0.3508
determinism: same text → identical vector
```

Two further properties are deliberate, not incidental:

* **384 dimensions**, matching prod (`migrations/20260727082_memory_chunks.sql`).
* **A 512-token input window**, matching prod's — which is *why* `memory_chunks`
  exists at all (#e8063a65: prod's embedder silently truncates, so a long memory
  only ever had its first ~15% embedded). An embedder with an 8k window would
  accept whole memories, nothing would ever need chunking, and a broken chunk
  read path would score identically to a working one. The gate would be blind
  again, differently.

### The two baselines are not comparable and must never be compared

Different embedder, different corpus, different isolation regime. A number from
one arm says nothing about the other. This is the same rule that already governs
capture-vs-judgement inside a single arm (`README.md` §"Capture the baseline
where it will be judged"), extended across arms: each baseline is only ever
compared to runs from its own arm, and `baseline_retrieval_branch.json` records
`arm: "branch"` so a file swapped into the wrong place fails loudly instead of
producing a plausible verdict.

`baseline_retrieval.json` is therefore **not** re-snapped by this change: the
prod arm's regime is unchanged, so its baseline stays valid. The new arm gets a
new file, captured in the new regime.

---

## What the branch arm still cannot see

Stated here so nobody reads its green as broader than it is. These are the
canary's job, which is why the canary is kept:

1. **Prod-only infrastructure faults** — a dead embedder endpoint (#402,
   2026-07-14), a bad deploy, an env var missing on the server.
2. **Scale and corpus effects.** The branch arm's database contains only the
   ~1100 fixtures it just wrote. A regression that only manifests against the
   real corpus (a query plan that degrades at 2141 vectors, a tags distribution
   the fixtures do not have) will not show up.
3. **Migration behaviour on existing rows.** Migrations run against an empty
   database here, so a backfill that is wrong for pre-existing data passes.
   Exactly the shape of #391 — which is a caution, not a claim of coverage: the
   branch arm catches #391's *effect* (new memories falling out of the dense
   arm), not a backfill bug that only touches old rows.
4. **Absolute recall quality.** Scores here are relative to this arm's baseline
   and this arm's embedder. `0.875` from the branch arm and `0.875` from prod
   are not the same measurement and comparing them is meaningless.

---

## Consequences

* Two required-check names change hands: `Memory recall gate` (prod-targeted)
  stops running on PRs, and `Memory recall gate (branch)` takes over as the
  required context. **Branch protection must be updated in the same rollout**,
  or the old context never reports and blocks every PR forever (evc-mesh#320,
  already documented in the README).
* The branch arm depends on a HuggingFace model download. It is cached
  (`actions/cache`, keyed on the pinned model name) and, on a miss with the
  network down, fails at startup with a readable error rather than serving bad
  vectors — an INCONCLUSIVE, which the existing alerting already handles.
* CI gains ~10 min on PRs that touch memory paths. PRs that do not still no-op.
* `MEMORY_PATHS` now guards two jobs instead of one; the blind-spot self-check
  (`test_gate_blindness.py`) is unchanged and still runs on every PR.
