# Perf budget — the храповик for Mesh web

`budget.json` sets a ceiling per metric. CI fails the build when a metric goes
over its ceiling. It never fails when a metric goes *under* — going under is
the whole point.

## The one rule

**A feature MR does not edit `web/perf/budget.json` or this README.** The three
bundle metrics are gated by how much the MR adds over main, not by a ceiling
every MR has to move:

1. **Growth over main (merge request pipelines).** `perf-bundle` measures the
   MR and compares it with main's own measurement at the merge-base (the
   `perf-bundle-report.json` artifact of `perf-bundle` on main). Without a
   signature an MR may add at most **+0.5 KB gz to `login.initial_js_kb_gz`**,
   **+1.0 KB gz to `dashboard.landing_js_kb_gz`** and **+2.0 KB gz to
   `total_js_kb_gz`**. More is red. Δlanding's +1.0 is Δlogin's +0.5 for the
   entry both screens share plus +0.5 for the dashboard page's own code.
2. **Signature = the `perf-budget-raise` label set by a signer.** The lead
   (Gary) sets it; the reason goes into the MR description: why the bundle
   grew and why that growth is worth it. "The job was red" is not a reason.
   The label alone signs nothing: the job reads the MR's label history and the
   raise counts only if the last event for the label is an `add` by a GitLab
   user listed in `.gitlab/perf-budget-signers.txt` **as main has it at the
   merge-base** (an MR cannot add its own author there). Label set by anyone
   else, removed, or history unreadable → no signature, red. Labels are read
   when the pipeline is created, so run a new pipeline after setting it.
   Nothing is written into `web/perf/*` for a signed raise.
3. **Absolute ceilings stay, but coarse.** For the bundle metrics
   `budget.json` holds a ceiling with ≥ 5 % headroom over main, rounded up to
   0.1 KB gz (now set on card `337e3184` over main `5d6cda4f4`, login / total /
   landing 180.09 / 902.80 / 252.17 → 189.1 / 948.0 / 264.8; moves are listed
   at the end of `HISTORY.md`). Every
   pipeline, main included, is red above it. Only a dedicated card moves it,
   never a feature MR; that card is also the moment to lower it when main has
   shrunk.
4. **Per-action counters (`board.*`, `task.*`, `view.*`, `comment.*`) stay
   absolute.** Improved a counter → lower its ceiling in the same MR. Raising
   one needs the reason in the MR description and the lead's approval; see
   "Choosing a ceiling" below for how to pick the number.

How the baseline is found, in order, all logged by the job:

- main's `perf-bundle` report for the merge-base itself;
- if the merge-base is a merge that `reuse-check` did not re-test (main reused
  the MR pipeline's result for the same tree, so main's `perf-bundle` keeps no
  report), the report of that merged MR's head: the MR whose merge or squash
  commit it is, found through the API, its head fetched and checked to have the
  merge-base's tree. Same tree, same bundle. The merge gate squashes, so this
  is the ordinary path, not an exception;
- otherwise the nearest first-parent ancestor on main that has a report; the
  log prints which commit was taken (`NOTE: the merge-base itself has no
  report; <sha> is taken instead`).

No report on any of 30 commits back is a red job, not a skip. A report counts
only if its `commit` is the commit asked for and every growth metric is a
number. One exception, for a metric added later (`introducedBy` in
`DELTA_LIMITS`, today only `dashboard.landing_js_kb_gz`): a main report written
before the metric existed has neither the number nor a key for it in its own
`budget`, so it still serves as the baseline for the other metrics and the new
one is printed as `NEW metric` — in that MR only its absolute ceiling holds it.
The MR that adds the metric is the only one in that position: from its merge on,
main's report carries the number and every later MR is gated on its growth. A
report whose `budget` names the metric but has no number for it, or that has no
`budget` at all, is not a baseline; `login` and `total` are never excused.

Every MR job prints the baseline commit, the base values and the growth:

```
[check-bundle-budget] baseline: <sha> (merge-base; pipeline <id> job <id>)
  Δlogin  login.initial_js_kb_gz: base 174.29 -> 174.31  +0.02 KB gz (limit +0.5)
  Δtotal  total_js_kb_gz: base 758.75 -> 759.1  +0.35 KB gz (limit +2.0)
  Δlanding dashboard.landing_js_kb_gz: base 248.79 -> 248.81  +0.02 KB gz (limit +1.0)
```

The journal of ceiling raises up to this change is frozen in
[`HISTORY.md`](HISTORY.md); new entries are not added.


## What's gated today

| Metric | What it measures | Checked by |
|---|---|---|
| `login.initial_js_kb_gz` | gzip bytes of the JS `/login` loads before first paint: the `index.html` entry chunk plus everything it statically imports (walked from `dist/.vite/manifest.json`, not guessed from filenames). Route-split (`React.lazy`) chunks are excluded on purpose — they load later, not before first paint. | `perf-bundle` CI job → `check-bundle-budget.mjs` |
| `dashboard.landing_js_kb_gz` | gzip bytes of the JS the page after sign-in (`w/:ws`, the dashboard, PRD dashboards §5.3) loads before it paints: the login closure above plus the static closure of the `src/pages/dashboard.tsx` route chunk. That chunk is `React.lazy`, so `login.initial_js_kb_gz` never counted it. Its `dynamicImports` (edit mode, widget picker, catalog) are not walked and stay out. No chunk for the page in the manifest (renamed, moved) is a refusal, not a skipped metric: update `DASHBOARD_PAGE_SRC`. | same |
| `total_js_kb_gz` | gzip bytes of every JS chunk vite emits, entry or not. | same |

All three are computed with `zlib.gzipSync(buf, { level: 9 })` on the built files
in `dist/` — the actual bytes a gzip-compressing server would send, not
vite's own build-log estimate (that one is meant for a human skimming build
output, not for diffing against a fixed number run to run).

## Running it locally

```bash
cd web
pnpm build
node perf/check-bundle-budget.mjs
```

Prints the measured values and writes `web/perf-bundle-report.json`
(git-ignored, same file the CI job publishes as an artifact). Exit code is 1
if anything is over budget.

Growth over main locally: measure main the same way, keep its
`perf-bundle-report.json`, then on your branch

```bash
node perf/check-bundle-budget.mjs --base-report /path/to/main-report.json
```

A local run never counts the label as a signature: there is no MR label
history to check. In a merge request pipeline the script finds main's report
itself and logs who set the label. The growth verdict (base commit, base
values, Δ, limits, signature) goes to
`web/perf-bundle-delta.json`, also a job artifact; `perf-bundle-report.json`
keeps its old schema and carries the absolute check only, because main's copy
is what the next MR reads as its baseline.


## A dist/ from another commit is refused

`pnpm build` is `tsc -b && vite build`. When `tsc` fails, vite never runs and
the previous `dist/` stays on disk — and the check used to read it and pass
(seen on `9514eba3`: the build failed with TS1117, the check went green on a
`dist/` from 05:30). A check that can pass without checking the code under
test is a false green.

Now `vite build` stamps `dist/.build-sha` with the commit it was made from
(`CI_COMMIT_SHA`, else `git rev-parse HEAD`), and `check-bundle-budget.mjs`
compares it with the current commit (same order) before it reads anything.
It exits 1 — with `dist/ is not from HEAD` and both SHAs — when they differ,
and equally when the stamp is missing, empty, or the current commit cannot be
determined. There is no "skip" path.

Limits, stated: it compares commits, not file contents, so uncommitted edits
made after a successful build are not noticed locally (CI always builds a
committed tree). `node perf/check-bundle-budget.mjs --selftest` proves the five
refusal/accept cases without a build; `perf-bundle` runs it before the real
check.

In CI, `perf-bundle` does not build: it measures the `web/dist/` artifact that
`build:web` produced in the same pipeline (`needs: [build:web]`). The stamp is
what makes that safe: an artifact whose `.build-sha` is not the pipeline's
commit is refused like any other stale `dist/`.

## Proving the gate actually gates (do this again after touching the script)

`node perf/check-bundle-budget.mjs --selftest` (first step of `perf-bundle`)
proves the growth gate without a build or the network: growth within the
limits passes, `Δtotal +2.5` fails with `Δtotal +2.5 KB gz > +2.0`, the same
with `perf-budget-raise` added by a signer passes, the same label added by
anyone else, re-added after the lead removed it, or with an unreadable label
history fails, `Δlogin +0.51` fails, and a history with no usable report
anywhere fails with `no baseline`. Break the comparison or the signer check
and it goes red (card `cebd90ab` shows that run).

For `dashboard.landing_js_kb_gz` (card `bc91cf36`) it runs a fake manifest:
a chunk the dashboard page imports statically growing by +1.2 KB gz, or a new
5 KB module imported statically, fails `Δlanding`; the editor chunk behind the
page's dynamic import growing by +50 leaves the metric unchanged (only Δtotal
moves); a manifest without the page chunk is refused; a main report from before
the metric is still a baseline for login/total with landing marked new, and one
whose `budget` names landing without a number is not. Walking `dynamicImports`
or excusing every missing metric turns it red (both mutants run on that card).

The ceilings themselves:


A budget check that can't go red isn't a check (see the fleet's `§0x`). Two
ways to redden it on purpose, both already run once for this ratchet's own
MR:

1. **Tighten `budget.json`** to something obviously below the real value,
   run the script, confirm exit 1 and the reported overage — then restore
   the real ceiling. Fastest way to check the comparison logic itself.
2. **Add a real regression** — e.g. a top-level `import * as X from
   "lucide-react"` in `main.tsx` (imports the whole icon set instead of the
   few icons actually used, defeating tree-shaking) — rebuild, confirm the
   job goes red with the real byte delta, then revert. This is the one that
   proves the *pipeline*, not just the script, actually blocks a merge —
   link the red pipeline run in the card that adds or changes this gate.

## Part 2 — per-action counters

`perf-counters` CI job → `perf/counters.spec.ts`. Four authed paths, each
measured from the moment the action starts until the page is quiet again (no
`/api` request in flight and no DOM mutation for 500 ms):

| Path | Action |
|---|---|
| `board.open` | dashboard → click the project in the sidebar → board with 200 tasks |
| `task.open` | board → click a card → task page |
| `view.switch` | board → List tab |
| `board.drag` | board → drag a card from its column into a different one (status change) |
| `comment.send` | task page with a typed comment → click Comment → comment shown |

| Metric (`<path>.<metric>` in `budget.json`) | Source |
|---|---|
| `react_commits` | commits of the root `<Profiler id="app">` |
| `board_card_commits` | commits under the board card `<Profiler id="board-card">` |
| `layout_count` / `recalc_style_count` | CDP `Performance.getMetrics` delta |
| `dom_mutations` | `MutationObserver` records on the whole document |
| `ms` | wall time — in the report only, **never gated** |

Earlier calibrations and ceiling changes of the counters (`board.open.dom_mutations`,
`board.drag`, the floors) are in [`HISTORY.md`](HISTORY.md).

### What it runs against

An **ephemeral** API, not prod: `perf-api-bin` builds `cmd/api` from the same
commit, `perf-counters` boots it over throwaway Postgres/Redis and seeds
`perf/seed-fixture.mjs` — one user, one project, 200 tasks whose titles,
statuses, priorities and labels are a pure function of the task number. The
seed is idempotent and `--selftest` fails the job if a second pass would
create anything. The live socket is answered locally and stays silent, the
service worker is blocked, CPU is throttled 4x, viewport 1440×900.

The counters need a **profiling build**: `VITE_PERF_PROFILER=1` turns the
Profilers on (`src/lib/perf-profiler.tsx`), aliases `react-dom/client` to
`react-dom/profiling` (the normal production build never calls `onRender`)
and writes to `dist-perf/`. Without the flag the Profiler is dead code; the
`perf-bundle` job fails if `onRender` ever appears in `dist/`.

### Minimum of three

Each path is measured 3 times (`PERF_REPEAT`) and the **minimum** of each
metric is gated. The app's own fetches race — when tasks and statuses land in
the unlucky order every board card renders twice (150 → 300 card commits on
`board.open`, also 0 ↔ 150 on `view.switch`). A race can only add work, so
the minimum is the stable figure. It is not perfectly stable: in 1 of 12
calibration runs all three `board.open` samples raced, which is why that
ceiling is 300, not 150. Fixing the race lowers it — per the rule above.

Spread over 12 local runs of the minimum: `dom_mutations` and
`board_card_commits` exact (apart from the race), `react_commits` ±2,
`layout_count` ±1, `recalc_style_count` wide (9–24) — its ceilings carry
~25% headroom, everything else sits at the highest observed minimum.

### Choosing a ceiling: two error rates, one script

A min-of-N gate on a metric that jitters has two ways to be wrong, and they
pull the ceiling in opposite directions. With `S(c)` the share of single
runs above `c`:

| | probability |
|---|---|
| **false red** — unchanged code fails | `P(min of N > C) = S(C)^N` |
| **miss** — a real `+d` regression passes | `1 − S(C − d)^N` |

A ceiling at the per-run **median** makes the first one large (the 18.5% on
`board.drag`, round 3 below). A ceiling at the per-run **maximum** drives it
to zero — and on any metric whose spread is wider than `d`, drives the catch
rate to zero with it: a `+1` regression passes whenever even one of the N
runs lands below the maximum. On `main` today `board.drag.board_card_commits`
(ceiling 1410, 49 CI runs spread 1332–1410) has a modelled catch rate of 0
for a `+1` — no first run in 11 reports landed above 1409 — and needs a
regression of `+79` before it is caught 95% of the time. Neither end is safe by construction;
the ceiling has to be computed.

`perf/gate-power.mjs` does that from real runs. Give it
`perf-counters-report.json` artifacts **of the same code** (a
`PERF_REPEAT=25 PERF_RECORD=1` calibration record plus the reports of recent
ordinary jobs is the best input — the ordinary jobs are what supply
first-run samples, see below):

```bash
cd web
node perf/gate-power.mjs [--repeat 3] [--shift 1] [--alpha 0.01] report.json…
```

Per metric it prints the false-red rate of the ceiling in `budget.json`, its
rule-of-three upper bound (when no sample is above the ceiling the real tail
is unknown, not zero), the catch rate for a `+shift` regression, the
smallest regression caught 95% of the time, the **tightest ceiling the data
defends at this `PERF_REPEAT`** (lowest observed value whose false-red bound
is ≤ `--alpha`), and the smallest `PERF_REPEAT` at which some ceiling both
keeps false red ≤ `--alpha` and catches `+shift` ≥ 95%. `none up to N=25`
means no repeat count rescues that metric: its spread has to shrink first
(`board.drag` on `main`, fixed by an internal ticket).

**The first run is pooled separately.** On some paths it is systematically
different from the later ones — `board.open`'s first run is 43 DOM
mutations in every recorded job, the later runs 45; `view.switch`'s first
run never hits the 173 the later ones sometimes do. Treating all runs as one
pool would report a 30% false red on `board.open.dom_mutations` that no job
has ever shown. A single calibration record carries one first run, which is
why the bound needs ordinary jobs' reports next to it.

**Rule for a new or changed ceiling:** use the `tightest@N` value, never the
raw maximum, and quote the script's row in the MR. A ceiling above that is
headroom the gate cannot use; below it, the false red is no longer bounded.
`node perf/gate-power.mjs --selftest` (run by `perf-bundle`) proves the
arithmetic, including the median and maximum mistakes above.

First application (56 CI reports from 24–26.09): `view.switch`
`react_commits` 8 → 7 and `board_card_commits` 150 → 0. Both were constant
over all 183 recorded runs (the local 0 ↔ 150 race above never showed on
CI); both went from catching a `+1` never to always, with a false-red bound
under 0.01%. The other loose-looking ceilings (`recalc_style_count` on every
path) buy almost nothing by tightening at `PERF_REPEAT=3` — their `tightest`
row still catches `+1` under 3% — and were left alone.

### Running it locally

```bash
scripts/local-stack.sh up                 # or any API you can throw away
cd web
PERF_API_URL=http://localhost:8095 node perf/seed-fixture.mjs --selftest
VITE_PERF_PROFILER=1 pnpm build
PERF_API_URL=http://localhost:8095 npx playwright test -c perf/playwright.perf.config.ts
```

`PERF_RECORD=1` measures and writes `web/perf-counters-report.json` without
gating — use it to take new ceilings. The CI job never sets it.

### Proving this fix's own ceiling gates

Reproducible on demand — not just prose in a Mesh comment. With the stack up
and the fixture seeded per "Running it locally" above:

```bash
# board.drag.board_card_commits ceiling temporarily 1282 → 1281 in budget.json
PERF_API_URL=http://localhost:8095 npx playwright test -c perf/playwright.perf.config.ts -g "board.drag"
```

```
  ✘  1 [perf-counters] › perf/counters.spec.ts:363:1 › board.drag — drag a card to a different column (9.9s)
    Error: perf budget exceeded on board.drag:
      board.drag.board_card_commits: 1282 > ceiling 1281
```

Ceiling restored to 1282 immediately after (never committed at 1281); with
it back, the same command passes. This is the +1 regression the tightened
ceiling exists to catch — the "measured 1282, ceiling 1282" claim above is
not just a calibration number, the gate actually reds one commit above it.

### Proving it gates

The red control used for this job's own MR: the "mounted flag" anti-pattern
in `TaskCard` (`useState(false)` + `useEffect(() => setMounted(true), [])`,
rendered as `data-mounted`) — every card renders twice and writes an
attribute. Result: `board.open.dom_mutations: 193 > ceiling 43`, job red.

Why not the obvious "subscribe the card to the whole task store": it changes
nothing today. `board.tsx` already reads `useTaskStore()` without a selector
and `TaskCard` is not memoised, so every store update already re-renders
every card — the regression is the baseline. A render that changes nothing in
the DOM is caught only above the `board_card_commits` ceiling, i.e. only once
the race above is fixed and that ceiling drops to 150.

### Read latency knob: `PERF_GET_DELAY_MAX_MS`

`PERF_GET_DELAY_MAX_MS=400 PERF_GET_DELAY_SEED=<n>` delays every API GET by a
seeded-random 0..400 ms, so the order the responses land in changes between
seeds. Off by default (the gated CI runs stay deterministic). `PERF_TRACE=1`
prints the API requests each measured action issued.

Measured before the fix (seeds 1-3, 6 runs each, 0 ms delay as control):
`view.switch.dom_mutations` 19 (control) vs 19/172/325 under delay; cause: the
list built its table from the task list, then rebuilt it when statuses / custom
fields landed. Now the skeleton stays until both have answered: 16 on every
seed (ceiling 20 -> 17). `task.open` issued the comments/members GETs twice
(panel mounted for one frame before its own loading skeleton); fixed, but its
layout count under delay (6-8 vs 4) stays: comments, custom fields and the
