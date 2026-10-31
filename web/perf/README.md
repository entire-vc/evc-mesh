# Perf budget — the ratchet for Mesh web

`budget.json` sets a ceiling per metric. CI fails the build when a metric goes
over its ceiling. It never fails when a metric goes *under* — going under is
the whole point.

## The one rule

**Improved a number → lower its ceiling in the same MR.** The ceiling is not
"the biggest we've ever measured", it's "the biggest we're willing to accept
today". A ceiling left above the real value stops protecting anything.

**Raising a ceiling needs a stated reason in the MR description and lead
sign-off.** "Tests were red so I bumped the number" is not a
reason — find out why the metric grew, then decide whether that growth is
worth it before landing it.

## What's gated today

| Metric | What it measures | Checked by |
|---|---|---|
| `login.initial_js_kb_gz` | gzip bytes of the JS `/login` loads before first paint: the `index.html` entry chunk plus everything it statically imports (walked from `dist/.vite/manifest.json`, not guessed from filenames). Route-split (`React.lazy`) chunks are excluded on purpose — they load later, not before first paint. | `perf-bundle` CI job → `check-bundle-budget.mjs` |
| `total_js_kb_gz` | gzip bytes of every JS chunk vite emits, entry or not. | same |

Both are computed with `zlib.gzipSync(buf, { level: 9 })` on the built files
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

## `login.initial_js_kb_gz` vs `total_js_kb_gz` after route splitting

Routes use `React.lazy`, so the two numbers diverged: on
`9514eba3` `login.initial_js_kb_gz` is 148.96 (was 432.07) and
`total_js_kb_gz` is 488.75 (was 432.07). Ceilings are 150 and 490.

`total_js_kb_gz` went **up** by ~56 KB: 95 chunks are gzipped one by one, and
small chunks compress worse than one big one (gzip -9 over the same files:
~7% more as separate files than as one stream); the rest is code that landed on
`main` since the 432 baseline. It is not what a user downloads on `/login`, so
it is a guard against unbounded growth, not a target. Raising it needed a
reason and lead sign-off (rule above) — see the MR.

`login.initial_js_kb_gz` also moved for a second, unrelated reason: the
`AppLayout` warm-up (`5bb8f632`, see `board.open.dom_mutations` below) added
`prefetch-next-route.ts` and its runtime-flags check to the code the signed-in
shell loads eagerly — 148.96 → 149.13 KB gz (**+0.17 KB gz**). Not a route-split
chunk, so it counts against the initial-load ceiling like any other
always-loaded code; stated here so the number isn't mistaken for drift.

`total_js_kb_gz` 644.5 → 644.6 (**+0.1 KB gz**): the collapsed doc tree
 added the expand / remember / reveal logic to the lazy docs chunk. The
build measured 644.53 after removing a duplicated key-change reset in
`doc-tree.tsx`, so the ceiling moved by the smallest step above that. Lead
sign-off: the product lead, on the card.

`total_js_kb_gz` 644.6 → 645.1 (**+0.5 KB gz**): cross-device sync of the
doc tree expansion (`docs-tree-sync.ts` plus the load / debounced write / flush
wiring in `doc-tree.tsx`) lands in the lazy docs chunk. CI measured 645.06 on
the previous base, 0.51 above main, so the ceiling moved by the smallest step
above that. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 649.4 → 656.0 (**+6.6 KB gz**): the new "My Runner" page
(`React.lazy` route chunk with its sections, API client and `runner` locale
namespace). Main already measured 649.3 against the 649.4 ceiling, so there was
no headroom; `login.initial_js_kb_gz` is unchanged (172.07). The last
0.4 KB is the client-side no-secrets check, the enable-404 message and the
per-workspace role guard added after review; the build measured 655.98. Lead
sign-off: the product lead, on the card (655.6 signed, 656.0 requested).

`total_js_kb_gz` 656.0 → 656.4 (**+0.4 KB gz**, main `a07e0fa2`): main measured
656.39 against the 656.0 ceiling after the signed Spark catalog client
(`bd7b56e2`, lazy extensions chunk) landed; the MR that surfaced it is
backend-only and does not touch the web bundle. The ceiling moved by the
smallest step above the measurement. `login.initial_js_kb_gz` unchanged. Lead
sign-off: the product lead.

`total_js_kb_gz` 657.5 → 658.6 (**+1.1 KB gz**): the dashboard widget layer
(`core/dashboard`: registry, data hooks, `dashboard.layout` / `dashboard.widget.*`
slot hosts with error boundary, mesh-default views) in the lazy dashboard route
chunk. It is the shared contract every theme draws the dashboard through, so the
code has to exist once in core. The build on the MR head (rebased on main
`ef00ed99`) measures 658.5; the ceiling moved by the smallest step above that.
`login.initial_js_kb_gz` unchanged. Lead sign-off: the product lead.

`total_js_kb_gz` 656.4 → 658.5 (**+2.1 KB gz**): the T4c slot contract for
`auth.screen` (slot boundary, context, registry, container and default view)
costs +1.83 KB gz; the build measured 658.34. Main (`c4860b7e`) already measured
656.5 against the 656.4 ceiling. Lazy loading and container dedup were tried and
do not help: the metric sums every chunk. `login.initial_js_kb_gz` unchanged.
Revert: revert the MR. Lead sign-off: the product lead, on the card (658.4 proposed,
658.5 signed for flake headroom).
After the previous change raised the ceiling to 658.6 on main, this MR was rebased onto it and
the combined build measures 660.26; the same +1.83 KB gz moved onto the new base
gives 660.4 (same headroom as signed).

`total_js_kb_gz` 660.4 → 665.1 (**+4.7 KB gz**) and `login.initial_js_kb_gz`
172.15 → 172.3 (**+0.15 KB gz**), (T4a): the kanban board split into a
container and the `board.layout` / `board.column` / `board.card` slot views
(slot types, board data module, three mesh-default views, slot fallback boundary
and registry version counter). The views are the shared contract every theme draws
the board through, so the code exists once in core; the sum covers every chunk, so
lazy loading does not help. CI measured 665.02 / 172.26 (local build 664.98) (the title primitives now take the container's stable actions and href, which keeps board cards out of router context); each ceiling moved
by the smallest step above that. The registry additions (`withSlotFallback`,
registry version) are in the eager chunk, hence the small login move. Revert: revert
the MR. Lead sign-off: the product lead, on the card.

`total_js_kb_gz` 668.0 → 671.8 (**+3.7 KB gz**), T4c-3: the docs tree and the document
page split into a container and the `doc.tree` / `doc.view` slot views (slot contract
types, two mesh-default views, the core blocks the doc.view draws the editor and
comments through). The views are the shared contract every theme draws the docs page
through, so the code exists once in core; the sum covers every chunk, so lazy loading
does not help. `login.initial_js_kb_gz` measured 172.22, under the existing 172.3
ceiling, so it does not move. CI measured 671.70 on the rebased base; the ceiling
moved by the smallest step above that. Revert: revert the MR. Lead sign-off: the product lead,
on the card.

`total_js_kb_gz` 729.3 → 715.0 (**−14.3 KB gz**) and `login.initial_js_kb_gz`
183.8 → 175.1 (**−8.7 KB gz**): the modules `/login` loads before first paint
used to ship as 26 chunks, most of them a few hundred bytes (`button`, `badge`,
`input`, `select`, `cn`, `utils`, `workspace`, …), each with its own gzip header
and import/export glue. The `app-initial` group in `vite.config.ts` puts every
`$initial` module, the initial icons included, into one chunk; the module set of
`/login` is unchanged (same sources in the source maps), it is now the entry plus
the rolldown runtime. Two traps, both in the config comment:

- Locale JSON stays out of the group: the test-only eager glob in
  `src/i18n/index.ts` marks every catalog `$initial`, and without the exclusion
  the lazy namespaces landed in the entry chunk (login 214.7).
- The initial icons cannot keep their own `icons-initial` chunk: it imported the
  lucide helper from `app-initial` while `app-initial` imported the icons back,
  the icons ran first and `/register` threw `t is not a function`. The manifest
  has no static-import cycle now (as on main).

The local build on main `ba95e157` measured 729.22 / 183.77, with the group
714.92 / 174.96. After merging main `866a6bfb` (its refresh-500 auth fix adds to
the eager code) the build measures 714.93 / 175.04; each ceiling moved to the
smallest 0.1 step above that.

Trade-off: a deploy that changes any initial module now invalidates the one
initial chunk rather than a few small ones. Revert: revert the MR (restore the
`icons-initial` group, drop `app-initial`, restore both ceilings).

## Proving the gate actually gates (do this again after touching the script)

A budget check that can't go red isn't a check (a deliberate failure proves enforcement). Two
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
(`board.drag` before the stable-sensor fix described below).

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

### `board.open.dom_mutations` 43 → 74 → 43

**CI recalibration, 2026-10-03:** the current DOM ceilings are **45** for
`board.open` and **448** for `board.drag`. These supersede the historical
43/447 floor claims below; the gate still uses min-of-3, without retries.
Across the latest 50 pipelines (main and MR/push branches), 34 reports contain
102 `board.open`, 96 `board.drag`, and 84 `comment.send` raw samples. Their
nearest-rank p95 DOM counts are 45, 448, and 25 respectively (ranges 43–50,
447–448, and 25–25). `comment.send` stays at 25; all other ceilings stay fixed.

Five job attempts exceeded the old DOM ceilings. Three of these share the
same SHA, `9b8a9b567d`: two fail on drag at 448, one on open at 45. Six reports
have the identical current `web/` tree, including those three failures and
three successful jobs. This window has no successful perf job on that exact
failing SHA, so it does not establish an exact-SHA false-red rate. The old
claim that the first open sample is *always* 43 is contradicted by job
113879 (48/45/45) and main job 111030.

The measured p95 removes the 1–2 mutation jitter without granting a
percentage margin to every metric. It deliberately gives up detection of
`+1` drag / `+2` open DOM mutations at their old floors. A `+20%` DOM increase
still exceeds these ceilings even from the observed floors:
`ceil(43 × 1.2) = 52 > 45`, `ceil(447 × 1.2) = 537 > 448`, and
`ceil(25 × 1.2) = 30 > 25`. Recalibrate again if the scenario or UI changes;
neither this empirical p95 nor ten green jobs guarantees a zero future tail.

The board is a lazy chunk. The first `import()` of it makes Vite
insert one `<link rel="modulepreload">` (or stylesheet) into `<head>` per
dependency the entry chunk does not already carry — 30 JS + 2 CSS for the
board — and the observer watches the whole `document`, `<head>` included.
74 − 43 = 31 ≈ those 32 insertions. It happens once per session, adds no
layout (`layout_count` unchanged at 3) and no React work. Measured on
pipeline 5163 (runs 74 / 76 / 76, minimum gated); the ceiling was raised to 74
with lead sign-off.

It is back at **43** because the chunk is now warmed *before* the click:
`AppLayout` calls `prefetchNextRouteOnIdle()` once the signed-in shell is on
screen, which `import()`s the dashboard and board chunks on idle
(`src/lib/prefetch-next-route.ts`). The 32 links are still inserted — but
during the idle warm-up, which the counter does not see (it is reset when the
action starts), not during the click. Until now only the login *submit* did
this, and the counters path (and every reload, bookmark or second tab with a
live session) never goes through it — that is why the number rose in the
first place.

Why not narrow the observer to `#root`: that would have kept the number at 43
by making the metric blind to `<head>`, and the user would still have paid for
the chunk on the click. Warming fixes the thing the number was pointing at.

Measured locally on this change (`5bb8f632`), 4x CPU, same fixture:

| | `board.open.dom_mutations` (3 runs) | gated (min) |
|---|---|---|
| without the `AppLayout` warm-up (= `main`) | 74 / 76 / 76 | 74 |
| with it | 43 / 45 / 45 | **43** |

The 43 / 45 / 45 shape repeated in 5 of 5 recordings (first run 43, later two
45) — the minimum is stable, so the ceiling is exactly 43. `layout_count` 3
and `react_commits` 12–13 are unchanged. Wall time is in the report but not a
proof here: on loopback the chunk download costs nothing, so the local `ms`
(1303 / 720 / 1485 → 1052 / 703 / 629) mostly shows the noise. What the user
should gain is the ~33 file requests no longer made on the first click on a
real connection — not measured here, only reasoned.

**Zero headroom, by construction, not by accident:** the ceiling is 43
because the gated minimum of 43 / 45 / 45 *is* 43 — there is no slack between
"what we measured" and "what fails the build". Any regression that adds even
one DOM mutation to the warm first render lands the minimum at 44 and the job
goes red. That is the intended behavior of a ratchet at its own floor, not a
bug to pad out with margin.

`board.open`'s setup waits until the board chunk has actually been fetched
(resource timing) before the action starts, so the result does not depend on
whether the idle callback happened to fire inside the 500 ms quiet window. It
also means that if the warm-up ever stops working, `board.open` fails in that
wait, by name, instead of as an unexplained +31 (both proven red on this MR:
ceiling 42 → `43 > ceiling 42`; warm-up removed → `waitForFunction` timeout).

The warm-up is off when `prefetchNextRoute` is `false` (`/config.json` or
`window.__MESH_FLAGS__` — unit test `prefetch-next-route.test.ts`) and on a
data-saver connection (`navigator.connection.saveData`).

`task.open.dom_mutations` 52 → 56 (+4) is the same change, found by running the
whole spec: with `board.open` red, Playwright reports the other three paths as
"did not run", so this one was invisible in CI. Measured locally on `558c1170`
(52) vs `9514eba3` + fix (56, three runs 56 / 56 / 56). The +4 is the same
mechanism as `board.open`: the first `import()` of `task-detail` inserts 4
`<link rel="modulepreload">` into `<head>` (`task-detail`, `_task-panel`,
`_template`, `_circle`; the rest of its closure is already loaded by the
board). Lead sign-off: the product lead.

### `board.drag` — drag coverage, and why `board_card_commits` barely moved

Baseline (`main` before this MR, min of 3, 4x CPU): `react_commits` 36,
`board_card_commits` 1374, `layout_count` 15, `recalc_style_count` 84,
`dom_mutations` 707. `board.tsx` had no memoization at all: `BoardColumn` and
`SortableTaskCard` were plain functions, per-task `onClick`/`onEditClick`
were fresh closures created inside `BoardColumn`'s own render (defeating any
memo on the card even if one existed), and `useProjectStore`/`useTaskStore`/
`useCustomFieldStore`/`useMemberStore` were all read with no selector at all
— any field changing on any of them (including ones `board.tsx` never reads)
re-rendered the whole page.

Fixed, in this MR:
- `useShallow` selectors on all four stores, reading only the fields
  `board.tsx` actually uses.
- `SortableTaskCard` and `BoardColumn` wrapped in `React.memo` with a custom
  comparator (not the default shallow-props one): `BoardColumn`'s comparator
  compares its `tasks` array by content (`tasksArrayEqual` — same objects in
  the same order, not same array reference) and its `holderNameById` map by
  content (`holderMapsEqual`), because both are rebuilt fresh on every
  `tasksByStatus` change even when their content didn't — `stores/task.ts`'s
  `moveTask` + `groupByStatus` always give an untouched task back its old
  object reference, which is what makes the content comparison exact rather
  than approximate.
- `onClick`/`onEditClick` changed from `(task) => onTaskClick(task)` closures
  built fresh per task inside `BoardColumn` to the stable top-level
  callbacks passed straight through — the closure was rebuilding a new
  function identity for every card on every `BoardColumn` render, which
  would have defeated `SortableTaskCard`'s memo regardless of anything else.
- The `BoardCol` objects `columns`'s `useMemo` builds are also rebuilt fresh
  on every recompute (new `{id, title, color, status}` object even when
  nothing in it changed) — reconciled against the previous render's objects
  by id + shallow content (`board.tsx`, the `boardColCacheRef` cache) so an
  unrelated column's `col` prop stays referentially stable across a drag too.
- `TaskCard` itself wrapped in `React.memo` (default shallow comparison —
  its only caller is `board.tsx`, and it now always receives stable
  `onClick`/`onEditClick` via `useCallback` in `SortableTaskCard`).

Measured after, locally (min of 3, 9 total runs across 3 separate
invocations, exact reproduction on `react_commits`/`board_card_commits`/
`layout_count`/`dom_mutations` every time): `react_commits` 35,
`board_card_commits` 1332, `layout_count` 13, `recalc_style_count` 86–102,
`dom_mutations` 447 — a **37% cut in `dom_mutations`** (707 → 447), a real
drop in `layout_count` (15 → 13), and essentially no change in
`react_commits` or `board_card_commits`.

**Ceiling methodology, round 3 — read this before touching `board.drag`'s
numbers in `budget.json` again.** Two earlier attempts were wrong in two
different ways, and both mistakes are cheap to repeat by "simplifying":

1. **Pipeline 5865, original min-of-3 sample:** `layout_count` 16,
   `dom_mutations` 448 — a single 3-run CI sample. Flaked `main` twice and
   was reverted, because these two metrics have no deterministic floor for
   this path (see the `InternalContext` explanation below): min-of-3 on a
   noisy distribution just picks whichever of 3 draws happened to land low,
   and a later min-of-3 gate run can legitimately land lower still.
2. **Median-of-25 (rejected, commit `642f4d7b`):** replacing the ceiling with
   the median of 25 local runs looks more stable, but it's the wrong
   statistic for *this* gate. The gate (`assertWithinBudget` in
   `perf/counters.spec.ts`) fails when `gated`, the **minimum** across
   `PERF_REPEAT` runs (default 3 in a normal CI job), exceeds the ceiling.
   A median is, by construction, higher than roughly half of all possible
   values in the distribution — so roughly half of all 3-sample mins drawn
   from the same distribution land *above* the median purely by chance, and
   the gate reddens on unchanged code. Working the actual numbers (all
   25³ possible ordered triples from the 25-value local sample) put the
   false-red rate at **18.5%** — unacceptable for a job that runs on every
   MR.

**Current ceiling: the max of 25 CI-measured runs**, not local ones — this
file's long-standing rule that a ratchet's baseline is what CI measured,
never a developer machine's numbers hoped to transfer, still applies. A
maximum is the only one of the three statistics that is structurally safe
against a min-of-N gate: it is an upper bound on every possible N-sample
minimum drawn from the same distribution, so it cannot false-red on
unchanged code no matter how the gate's `PERF_REPEAT` samples happen to
fall, while still reddening on an actual regression, which shifts the whole
distribution (including its max) upward.

Data source: pipeline
6026 (historical internal pipeline; link unavailable in this public snapshot), job
`perf-counters` (92000), 25 repeats via `PERF_REPEAT=25 PERF_RECORD=1` on
commit `0ebe26ad` in an isolated calibration run. These recording overrides
are diagnostic only: the normal `perf-counters` job remains unmodified,
`PERF_REPEAT` still defaults to 3, and `PERF_RECORD` is never set.

Range observed across the 25 runs (min–max): `react_commits` 35–36,
`board_card_commits` 1332–1410, `layout_count` 14–18, `recalc_style_count`
78–99, `dom_mutations` 448–837. One run out of 25 came in low across every
metric at once (the same low corner pipeline 5865's min-of-3 happened to
sample) — consistent with the "no deterministic floor" diagnosis above, not
a second regression. `budget.json` gates on the **max** of each column:
`react_commits: 36`, `board_card_commits: 1410`, `layout_count: 18`,
`recalc_style_count: 99`, `dom_mutations: 837`.

**Superseded in part by "Choosing a ceiling" above:** the
maximum is safe against false red but, measured with `perf/gate-power.mjs`,
it leaves this path blind to any regression smaller than its spread. Keep
the warning against the median; replace "use the max" with the script's
`tightest@N`.

**Do not shrink these toward the median or the min again.** The gate stays
min-of-`PERF_REPEAT` — that part is unchanged and correct, and matches every
other path in this file. It is only `board.drag`'s *ceiling* that departs
from "ceiling ≈ observed floor", and only because this specific path's
`board_card_commits`/`dom_mutations` don't have one (dnd-kit context churn,
below). A future "this ceiling looks loose, let's tighten it" pass needs to
re-derive false-red risk against the min-of-3 gate first, not just eyeball
the numbers — that is exactly how both prior attempts went wrong.

**Why the last two barely moved, verified by reading `@dnd-kit/core`'s own
source (`core.cjs.development.js`), not guessed:** every `useDraggable`,
`useDroppable` and `useSortable` call anywhere in the tree — regardless of
which column or `SortableContext` it lives in — reads
`React.useContext(InternalContext)`. `DndContext` recomputes that context's
value with `useMemo` on
`[activatorEvent, activators, active, activeNodeRect, dispatch, draggableDescribedById, draggableNodes, over, measureDroppableContainers]`
— already as tight as dnd-kit's own maintainers made it — and `active`/
`over`/`activeNodeRect` change on essentially every pointer-move tick that
crosses a collision boundary during a drag. A new context value re-renders
**every** consumer, board-wide, independent of props — which is exactly the
one thing `React.memo` cannot intercept: memo only blocks a re-render
triggered by the *parent* passing unchanged props, never one triggered by
the component's *own* hook subscribing to a context that changed. Confirmed
empirically too: `board_card_commits` was 1374/1380/1332 across three
successive rounds of memoization work that each measurably cut
`dom_mutations`, and never dropped in proportion.

The one lever that would cut it further is fewer *mounted* `useSortable`
consumers at once — i.e. virtualizing each column's card list
(`@tanstack/react-virtual`). Not done here: it needs each column to become an
independently-scrolled fixed-height viewport, which is a visible layout
change requiring screenshot review against an agreed reference, plus
integration work reconciling dnd-kit's
`SortableContext` `items` with a virtualized index range and the drag
overlay. Flagged as an explicit follow-up, not silently dropped.

`board.drag`'s setup resets the dragged fixture task back to its starting
column via a direct API call before every repeat (`perf/seed-fixture.mjs`'s
`DRAG_TASK_TITLE`, `perf/counters.spec.ts`) — a second UI drag would pollute
that repeat's baseline with the previous repeat's own cleanup, the same
reasoning `comment.send` uses for `wipeComments()`.

### `board_card_commits` and `dom_mutations` DO have a floor after all

The round-3 write-up above concluded `board_card_commits`/`dom_mutations` had
no deterministic floor because every `useSortable`/`useDraggable`/
`useDroppable` consumer reads `DndContext`'s `InternalContext`, which
recomputes on essentially every pointer-move tick during a drag. That's true
of the context recompute itself, but two *additional*, independent sources
of variance were stacked on top of it and, it turns out, dominated these two
specific counters:

1. **`sensors` was a new array reference on every `BoardPage` render.**
   `useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }))`
   built its options object inline at the call site, so any unrelated
   `BoardPage` re-render (a store update, a filter change, anything) handed
   `DndContext` a brand-new `sensors` array, which flows into the same
   `InternalContext` `useMemo` as `activators` — one more full board
   re-render stacked on top of the drag-tick churn the round-3 diagnosis
   already knew about, and whether an unrelated re-render happened to land
   during a given run's drag window is exactly the kind of timing accident
   that shows up as run-to-run variance rather than a fixed count. Fixed by
   hoisting the options object to a module-level `POINTER_SENSOR_OPTIONS`
   constant (same pattern applied in `calendar.tsx`, which had the identical
   inline-object pattern on its own `sensors`).
2. **The post-drop layout-FLIP animation fired on a timing-dependent
   window.** `@dnd-kit/sortable`'s default `useSortable` behaviour animates
   shifted siblings into their new position after a drop, gated by an
   internal `wasDragging` flag the library clears on a bare
   `setTimeout(50)`. Whether the post-drop re-render this test's assertions
   observe lands inside or outside that 50 ms window is not deterministic
   (it depends on the test runner's own scheduling, not on anything the app
   controls), and when it lands inside the window every shifted card
   re-renders once more and re-applies its transform style — directly
   inflating `dom_mutations`. Fixed by passing
   `animateLayoutChanges={() => false}` to `SortableTaskCard`'s
   `useSortable()` call. Cards still snap to their new slot instantly on
   drop; only the animated slide-into-place is removed (checked visually at
   1440 and 393 — no jump or flash, just an instant reflow instead of an
   animated one).

Neither fix touches the underlying `InternalContext` recompute-on-every-tick
behaviour round 3 documented — `layout_count` and `recalc_style_count` still
vary run to run (12–16 and 56–98 over 15 CI runs) for that reason, and still
need the max-of-N ceiling methodology below. But `board_card_commits` and
`dom_mutations` were not actually hostage to that mechanism the way round 3
assumed: measured over 15 repeats in CI (pipeline
6073 (historical internal pipeline; link unavailable in this public snapshot) on a
a dedicated, uncontended runner during isolated calibration),
`board_card_commits` is **exactly 1282 on all 15/15 CI runs** (was
1332–1410) and `dom_mutations` is **447 on 13/15 runs, 448 on 2/15** (was
447–837) — a spread of 0 and 1 respectively, against this card's acceptance
bar of 0 and ≤5.

CI is this repo's authoritative measurement environment for ratchets (see
round 3's methodology above), but it's worth being precise about what local
measurement shows on the shared fleet dev host: repeated local 15-run
samples land `board_card_commits` at 1282 on the large majority of runs,
with an occasional single run at 1281 when the host is under heavy
concurrent load from unrelated processes (observed at `load average
7.5–10.8` on 10 cores, ~28 concurrent Docker containers from other tenants
of the same machine) — never above 1282. That's consistent with a genuinely
fixed floor that a contended host can occasionally undershoot inside the
harness's measurement window, not a reopened source of unbounded variance:
a value landing *below* the ceiling never fails `assertWithinBudget`
(`gated > ceiling`), and the red-control check below still catches a real
+1 regression reliably. Don't re-read an isolated local 1281 as evidence the
fix regressed — check CI first.

`budget.json`'s `board.drag.*` ceilings are updated to the max of that same
15-run CI sample: `react_commits: 36`, `board_card_commits: 1282`,
`layout_count: 16`, `recalc_style_count: 98` — except `dom_mutations`, which
is set to **447**, not the sample max of 448. Fault-injection on pipeline
6130 (historical internal pipeline; link unavailable in this public snapshot) (16
parallel runs: 8 clean, 8 with one extra DOM mutation injected) showed a
ceiling of 448 catches that injected +1 regression 0/8 times (the injected
run also lands on 448 or 449, indistinguishable from clean noise), while 447
catches it 8/8 times. Taking the calibration max here would ship a gate that
cannot see the regression it exists to catch, so the ceiling is the tightest
value the same calibration data supports (447), not the loosest one that
happens to pass. The `board_card_commits`/`dom_mutations` ceilings are no
longer loose statistical margin over a noisy floor — they're a tight bound
over two counters that are now (near-)constant, so a future regression on
either one will redden on the very next PR rather than needing another
25-run calibration pass to notice.

### Runs of one job are not independent; `board.drag` followed the API

`gate-power.mjs` treats runs as independent draws. On CI they are not: the
three runs of one job share the runner's load and the ephemeral API's
latency, and several counters follow that latency. Measured on 303
`perf-counters` reports from 04–10.10 (all jobs, `rep*.json` artifacts):

| | |
|---|---|
| model's joint false red at N=3 | 0.28% (bound 0.59%) |
| jobs that went red on a budget and passed on retry of the same SHA | 6 of 303 (2.0%) |
| `board.drag.recalc_style_count > 98` | model 0.05%, observed 1.02% (3 jobs, every run 99–107) |
| `task.open.layout_count > 5` | model 0.04%, observed 0.66% (2 jobs, every run 6) |

The table now carries an **observed (jobs)** column — the share of reports
whose own gated minimum is over the ceiling — and `tightest` must keep
both the model's bound and the observed rate under `--alpha`. The summary
lists every metric where CI did worse than the model's own bound.

`board.drag` was the clearest case, and its cause was in the app, not the
runner. `stores/task.ts` `moveTask` awaited `POST /tasks/:id/move` before
touching local state, so after a drop the card was drawn back in its source
column for the whole round trip and jumped to the target when the answer
landed. A fast API hid that inside one render; a slow one did not. Every
CI run fell in one of two modes:

| | runs | dom_mutations | layout_count | recalc_style_count (median) | ms (median) |
|---|---|---|---|---|---|
| answer before the drop settled | 781 | 447 | 12–13 | 78 | 2074 |
| answer after it | 104 | 448 | 13–16 | 100 | 3110 |

Reproduced locally by delaying every non-GET `/api` request with
`page.route` (not committed): 1500 ms put all six runs in the second mode
(448 / 16 / 97–101 — over the old ceiling of 98 on unchanged code). After
the fix `moveTask` updates the store first and rolls back on a refusal, and
a task list fetched before the move was confirmed is overlaid with it (a
WS-triggered refetch answered from pre-move state was the second road to
the same snap-back — authed-e2e job 160044). 25-run records after the fix,
per condition:

| condition | react_commits | board_card_commits | layout_count | recalc_style_count | dom_mutations |
|---|---|---|---|---|---|
| plain | 33 | 1080 | 11–13 | 72–81 | 442 |
| writes +1500 ms | 32–33 | 1079–1080 | 11–13 | 74–81 | 442 |
| reads +0–400 ms random | 32–33 | 1079–1080 | 11–13 | 72–82 | 442 |
| CPU throttle 8x | 32–33 | 1079–1080 | 13 | 75–84 | 442 |

New `board.drag` ceilings, from 6 CI reports of this code (job 179703 and
five retries of it: every run 33 / 1080 / 13 / 442, recalc 75–87) plus the
118 local runs above. `react_commits` 36 → 33, `board_card_commits`
1282 → 1080, `layout_count` 16 → 13, `dom_mutations` 448 → 442: constant on
CI, so the ceiling is the value. `recalc_style_count` 98 → 86: it still
follows the runner, so it was priced on the 210 pre-fix CI jobs whose every
run was single-render (same work as the fixed path) plus the 6 new ones —
216 jobs, gated minima 57–84. `gate-power.mjs` at N=3:

| ceiling | model bound | observed (216 jobs) | +k caught 95% |
|---|---|---|---|
| 82 (`tightest`) | 0.29% | 0.93% | +18 |
| 84 | 0.03% | 0 | +20 |
| **86** | **0.003%** | **0** | **+22** |
| 98 (old) | — | 1.02% (pre-fix) | +34 |

86 rather than `tightest`: 82 and 83 sit inside what 216 jobs already did,
and a recalc ceiling catches only double-digit regressions at any of these
values, so two more steps of margin cost almost nothing in sensitivity.

The read paths (`task.open`, `board.open`) still follow the order their GETs
land in — 0–400 ms of random read latency moves `task.open.layout_count` to
6–8. That is the fetch race described under "Minimum of three"; it is an app
property, and the observed column is what keeps it from being priced at zero.
`view.switch` had the same race (172/325 DOM mutations) until the list view
started waiting for statuses and custom fields; see the latency section below.

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
team-relay flag are separate sections that each paint when they arrive.

`login.initial_js_kb_gz` 172.7 → 179.2 (**+6.5 KB gz**) and `total_js_kb_gz` 672.3 →
685.9 (**+13.6 KB gz**), themes T5: the theme runtime lives in the eagerly
loaded `AppLayout` (registry of slot views, per-slot error boundary, version check,
style apply and per-slug cache, `useWorkspaceTheme`), which is what the
no-flash switch needs. Main measured within ~0.1 KB of its 172.7 / 672.3 ceilings, so there
was no headroom. The shared copies for theme bundles (React, react-dom, i18n, the
primitives) are a lazy chunk loaded only when a theme ships views (first attempt put
them in the entry: 205 KB gz initial). The build measured 178.32 / 684.9 locally;
the ceilings carry ~0.5 KB for the local-vs-CI difference. Lead sign-off: the product lead,
requested on the card.

`login.initial_js_kb_gz` 179.2 → 179.4 (**+0.2 KB gz**) and `total_js_kb_gz` 685.9 → 686.1
(**+0.2 KB gz**): the colour-mode store (`core/themes/mode.ts`: light / dark /
system, server sync, `prefers-color-scheme` watcher) is loaded with `AppLayout`, because
the mode must be applied before the first screen after sign-in. CI measured 179.33 / 686.03 (after review fixes: per-workspace cached modes, a guard against a late sync
overwriting a fresh choice); the ceilings moved by the smallest step above that. Lead sign-off: the product lead.

`login.initial_js_kb_gz` 180.8 → 181.2 (**+0.4 KB gz**) and `total_js_kb_gz` 687.7 → 689.0
(**+1.3 KB gz**), T6.1: the slot contract types move to the `@entire-vc/mesh-theme-sdk`
package (type-only, no bytes) and the core gains `SlotContextProvider` / `useSlotContext`
and an explicitly typed `sdk` export object in `core/themes/shared.ts`, so an SDK
declaration the core lacks fails `tsc`. The provider and the export object are the only
runtime additions; the sum covers every chunk, so lazy loading does not help. CI measured
181.1 / 688.88 (perf-bundle job 354106) on the base carrying the 180.8 / 687.7 ceilings; the
ceilings moved by the smallest 0.1-step above that. Revert: revert the MR. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 689.0 → 700.5 and `login.initial_js_kb_gz` 181.2 → 181.6
(theme UI: **+11.5 KB gz total / +0.5 KB gz login** over main): the workspace
Theme tab, instance themes page, seven new SchemaForm widgets and their
en/ru strings. CI measured 700.18 / 181.52 on the base of 2026-10-23. If main
moves before the merge, recompute from the new main with the same increment.
Lead sign-off: the product lead, 2026-10-23.

`total_js_kb_gz` 700.5 → 701.0 (**+0.5 KB gz**), runner chat turn events: ten `chat_*` runner
error codes (`chat_stale_claim`, `chat_seq_gap`, `chat_rate_limited`, …) are added to
`apiErrors` in `en` and `ru`, so the UI can name them when a run is refused. Main sat on its
700.5 ceiling; CI measured 700.81 with them (perf-bundle, pipeline 17356). The ceiling moved by
the smallest step above that. Lead sign-off: the product lead.

`total_js_kb_gz` 701.0 → 701.2 (+0.2 KB gz): after main took the theme Remove refactor,
CI measured 701.1 on this MR (perf-bundle, pipeline 17441, over the 701.0 ceiling by 0.1).
Same `apiErrors` strings as above; the ceiling moved by the smallest 0.1-step above the
measurement. Revert: set the ceiling back to 701.0. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 703.1 → 703.3 (+0.2 KB gz): the theme preview now labels the picker-only
`status`, `estimated_hours` and `created_at` fields, which adds three board labels in `en`
and `ru`. perf-bundle measured 703.29 on this MR on top of main `a8a64693` (pipeline 17734,
job 371633, over the 703.1 ceiling by 0.19). The ceiling moved by the smallest 0.1-step above
the measurement. Revert: set the ceiling back to 703.1. Lead sign-off: the product lead.

`total_js_kb_gz` 703.9 → 720.6 (**+16.7 KB gz**) and `login.initial_js_kb_gz` 182.1 → 183.0
(**+0.9 KB gz**), (Chat E3): the chat screen (page, feed with Markdown, composer with
image paste, thread sidebar, reducer, project settings, en/ru strings). The page is a
`React.lazy` route chunk, but the total sums every chunk, so lazy loading does not help it.
The entry grows by the project tab (`CHAT_PROJECT_TAB`, `MessageSquare`), the `useChatEnabled`
hook in the header and the `chat` i18n namespace registration; none of the chat screen code
is in the entry. perf-bundle measured 182.95 / 720.55 on this MR (pipeline 18003, job
378075, over the 182.1 / 703.9 ceilings by 0.85 / 16.65). Each ceiling moved by the smallest
0.1-step above the measurement. Revert: revert the MR. Lead sign-off: the product lead, requested on the card.

`login.initial_js_kb_gz` 183.0 → 183.1 (+0.1 KB gz) and `total_js_kb_gz` 720.6 → 721.0 (+0.4 KB gz):
the merge result of the earlier change (Chat E3) with the neighbouring branch on main, not new code. perf-bundle
measured 183.04 / 720.96 on main `a664a287` (pipeline 18044, job 379510, over the 183.0 / 720.6
ceilings by 0.04 / 0.36), against 182.95 / 720.55 on the MR branch. Each ceiling moved by the smallest
step above the measurement; no further raise for this screen. Revert: set the ceilings back to
183.0 / 720.6. Lead sign-off: the product lead, on the card.

`login.initial_js_kb_gz` 183.1 → 183.2 (+0.1 KB gz) and `total_js_kb_gz` 721.0 → 721.4 (+0.4 KB gz),
viewport containment in `DropdownMenuContent` (measure in a layout effect, shift state, transform
merge) so the tab bar "⋮" menu no longer clips at 393px. Main measured 183.01 / 720.99 (pipeline 18243),
so there was no headroom; perf-bundle measured 183.16 / 721.33 on this MR (pipeline 18235, job 383444,
over by 0.06 / 0.33). The sum covers every chunk and the component is in the entry, so lazy loading does
not help. Each ceiling moved by the smallest step above the measurement. Revert: set the ceilings back to
183.1 / 721.0 together with reverting the MR. Lead sign-off: the product lead, requested on the card.

`login.initial_js_kb_gz` 183.2 → 183.3 (+0.1 KB gz) and `total_js_kb_gz` 721.4 → 723.1 (+1.7 KB gz),
a read-only status panel for instance-managed GitLab/GitHub in workspace Settings and a
workspace VCS tab in project settings (`status-panel.tsx`, shared by both lazy screens) plus its
en/ru strings in the lazy `integrations` namespace. None of it is in the entry; the entry grows only
by the new shared chunk's name in the preload map. Main measured 183.16 / 721.23 (pipeline 19145,
job 400947); perf-bundle measured 183.21 / 723.00 on this MR (pipeline 19138, job 400839, over by
0.01 / 1.6). Each ceiling moved by the smallest 0.1-step above the measurement. Revert: set the
ceilings back to 183.2 / 721.4 together with reverting the MR. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 723.1 → 723.3 (+0.2 KB gz): user chat messages keep the line breaks they
were typed with (`remarkSoftBreaks` in `lib/milkdown/soft-breaks.ts`, a `breaks` mode of the read-only
markdown renderer that the chat feed turns on for the user's bubble) plus a
`data-testid` on the layout's sidebar wrapper. The ceiling had 0.1 of headroom (the status-panel change measured
723.00); perf-bundle measured 723.24 on this MR (pipeline 19367, job 406535, over by 0.14). The sum
covers every chunk, so lazy loading does not help, and the helper is the fix itself. The ceiling moved
by the smallest 0.1-step above the measurement. Revert: set the ceiling back to 723.1 together with
reverting the MR. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 723.3 → 725.2 (+1.9 KB gz): the repository list of a project's GitLab/GitHub
tab with each repository's source and Bind/Unbind (`vcs-repos.tsx` in the lazy project-settings chunk)
plus its en/ru strings in the lazy `integrations` namespace (about 1.2 KB gz of the growth). Main
measured 723.3 locally, at the ceiling with no headroom; perf-bundle measured 725.13 on this MR
(pipeline 19637, job 412001, over by 1.83). The sum covers every chunk, so lazy loading does not help.
The ceiling moved by the smallest 0.1-step above the measurement. Revert: set the ceiling back to
723.3 together with reverting the MR. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 725.2 → 726.1 (+0.9 KB gz): the instance-managed GitHub/GitLab Settings
dialog lists the repositories sending links (`VcsRepoList` in `status-panel.tsx`), offers the
instance-settings link and an explicit own-credentials opt-in, plus their en/ru strings in the lazy
`integrations` namespace and `?ext=` preselection on the instance settings page. Before the repository-list change landed,
main measured 723.16 (pipeline 19715, job 413328) and perf-bundle measured 724.08 on this MR
(pipeline 19676, job 412676, +0.92); on top of the repository-list change (725.13) that is 726.05. The sum covers every
chunk, so lazy loading does not help. The ceiling moved by the smallest 0.1-step above the
measurement. Revert: set the ceiling back to 725.2 together with reverting the MR. Lead sign-off:
the product lead, requested on the card.

`total_js_kb_gz` 726.1 → 727.5 (+1.4 KB gz): the My Runner Resources and Revision history
blocks read and edit the workspace runtime catalog (catalog loader/saver and entry summaries in
`lib/my-runner.ts`, the bindings section, filter and revision rows in the lazy my-runner chunk) plus
their en/ru strings in the lazy `runner` namespace. perf-bundle measured 724.41 on this MR before
the repository-list change landed (pipeline 19709, job 413207, over 723.3 by 1.11); 727.49 locally after merging main with
the repository-list change and the Settings-dialog change. The sum covers every chunk, so lazy loading does not help. The ceiling moved by the
smallest 0.1-step above the measurement. Revert: set the ceiling back to 726.1 together with reverting
the MR. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 727.5 → 728.7 (+1.2 KB gz): a catalog theme is applied through the
workspace theme route instead of the extension connect wizard (`extension-connect.tsx`), the
Installed list and catalog show a theme as active or not selected (`installed.tsx`, `catalog.tsx`,
`extension-installed.ts`, `useExtensionCatalog.ts`), plus their en/ru strings in the lazy
`integrations` and `settings` namespaces. perf-bundle measured 727.18 on this MR against the 726.1
ceiling (pipeline 19843, job 416467, over by 1.08), and the ceiling was first raised to 727.2. After
the My Runner change raised the ceiling to 727.5 on main, main was merged into this MR: 728.61 locally, the same
+1.1 KB on top of the new base. The sum covers every chunk, so lazy loading does not help. The
ceiling moved by the smallest 0.1-step above the measurement. Revert: set the ceiling back to 727.5
together with reverting the MR. Lead sign-off: the product lead, on the card (727.2 for the same growth).

`login.initial_js_kb_gz` 183.3 → 184.0 (+0.7 KB gz) and `total_js_kb_gz` 728.7 → 729.5 (+0.8 KB gz),
the project header strip shows an extension tab only for an extension that works per project
(`levels.project`) and is bound to this project (`lib/project-extension-tabs.ts`: a per-project
bindings cache with ordered reads, fail-closed, plus `levels` in the slot registry). The header is
part of the app shell, so the hook lands in the login chunk. The first version pulled
`lib/extension-settings` into the shell (185.04 / 724.13 on pipeline 19743, job 414081); the hook now
calls the bindings endpoint itself with a type-only import, which measures 183.97 / 724.06 locally
on top of 723.3, 183.97 / 727.00 merged with main after the repository-list change and the Settings-dialog change, 183.96 / 728.30
after the My Runner change, and 184.00 / 729.44 after the theme change.
What remains is the fix itself. Each ceiling moved by the smallest 0.1-step above the measurement.
Revert: set the ceilings back to 183.3 / 728.7 together with reverting the MR. Lead sign-off:
the product lead, on the card, against perf-bundle on pipeline 19844 (job 416492: 183.97 / 727.00, total then
726.1 → 727.1), confirmed for 728.4 after the My Runner change; 729.5 after the theme change requested on the card. Login
headroom is 0.00 KB gz: the next growth of the shell needs its own ratchet argument, not another bump.

`total_js_kb_gz` 729.5 → 729.0 and `login.initial_js_kb_gz` 184.0 → 183.8 (lowered): main went
0.08 over the 726.1 ceiling after the hidden built-in Runner change landed without a measurement
against it (726.18, pipeline 19829, job 416190). The cut is `build.modulePreload.polyfill: false` in
`vite.config.ts`: the polyfill sat in the entry chunk, and every supported browser handles
`<link rel="modulepreload">` natively; where one doesn't, the entry's static imports still load
through the module graph. It saves 0.31 KB gz total and about 0.25 KB gz of the login chunk;
measured locally on top of the earlier change: 728.96 / 183.75. Each ceiling moved to the smallest 0.1-step above
the measurement. Revert: set the ceilings back to 729.5 / 184.0 together with reverting the MR.

`total_js_kb_gz` 729.0 → 729.3 (+0.3 KB gz): two extension changes landed on main without headroom.
the earlier change (five extension states from one function, `lib/extensionState.ts` and the shared
`ExtensionStateBadge` / `ExtensionStateNote` in Catalog, Installed, the extension page and the
instance admin, plus the separate `builtin` trust badge) took main from 728.43 (pipeline 20332, job
428561) to 729.01 (pipeline 20353, job 428988); the instance allow-list change after it (`582a771f`)
took it to 729.22 (pipeline 20354, job 429019, same value locally). The extensions chunk is lazy, but
the metric sums every chunk; the locale keys the change replaced were removed in the same MR and no
unused ones remain. The ceiling moved by the smallest 0.1-step above the measurement.
`login.initial_js_kb_gz` unchanged (183.77). Revert: set the ceiling back to 729.0 together with
reverting the earlier change. Lead sign-off: the product lead, on the card.

`total_js_kb_gz` 715.0 → 717.3 (raised): the Runner section split into Overview / Agents / Resources /
History tabs, with the single on/off switch, the agents table with inline limits and catalog
revision restore (en + ru strings). Main (17cb347b, one initial chunk) measures 714.93 against its
715.0 ceiling; the branch measures 717.22, a delta of 2.29 (my-runner chunk +1.78, runner locales
+0.5 after shortening the confirm texts). The ceiling is main's plus 2.3 and holds only while the
branch delta to its main base stays at or under +2.3. `login.initial_js_kb_gz` unchanged (175.04).
Lead sign-off: the product lead, on the card. Revert: set it back to 715.0 together with reverting the MR.

`total_js_kb_gz` 729.3 → 730.5 (+1.2 KB gz), (U4, themes as a catalog section): Catalog gets
its own Themes section with the theme icon and badge, Installed lists themes with the same
enable switch and Settings window as extensions (statuses from `extensionState()`), the theme
settings panel moves Save / Close / Reset into a footer, and workspace settings link the theme to
Extensions → Mesh (`workspace-theme-link.tsx`). The old theme picker and Choose/Configure branches
in `extension-connect.tsx` were removed in the same MR. All of it sits in lazy chunks, but the metric
sums every chunk. pipeline 20394 (job 430209) measured 730.5 on the previous base; on top of
`866a6bfb` the local build measures 730.42. The ceiling moved by the smallest 0.1-step above that.
`login.initial_js_kb_gz` unchanged (183.8). Revert: set the ceiling back to 729.3 together with
reverting the earlier change. Lead sign-off: the product lead, on the card.

After `7487fd5c` (one initial chunk) lowered the ceilings to 715.0 / 175.1 on main, the earlier change was merged
with it and the combined build measures 716.14 / 175.03; the same +1.2 KB gz moved onto the new base
gives 716.2 (same delta as signed). `login.initial_js_kb_gz` stays at main's 175.1. Revert: set the
ceiling back to 715.0 together with reverting the earlier change.

After `d42e0af7` (Runner tabs, above) raised the ceiling to 717.3 on main, the earlier change was merged with it
again; by the lead's rule the ceiling is main's value at merge plus the signed +1.2 KB gz: 717.3 + 1.2
= 718.5. `login.initial_js_kb_gz` stays at main's 175.1. Revert: set the ceiling back to 717.3
together with reverting the earlier change.

`total_js_kb_gz` 718.5 → 717.8 (main + 2.5): the Runner becomes one product managed on the
Runner screen. The new runner program card (address, consent with capabilities and domains,
health, pending update; on/off stays with the one switch of the admin card) and the merged runner/controller rows add about 1.5 KB
gz to the lazy `my-runner` chunk; the shared runner ids moved into their own 0.72 KB gz chunk
(`extension-settings-route` shrank by 0.53); the new `runner` and `integrations` locale keys add
0.33 + 0.41 (ru). The removed Controllers list (`controllers-status.tsx`) had no keys of its own left.
Measured locally on the MR merged with main `db43680c`: 717.73 / 175.04; plain main `db43680c`
measures 715.19 / 175.03, so the ceiling sits below main's 718.5 and is the measurement plus the
smallest 0.1-step. `login.initial_js_kb_gz` stays at 175.1 (+0.01, the chunk import map). Revert:
set the ceiling back to 718.5 together with reverting the earlier change. Lead sign-off: the product lead, on the card
(up to +3.05 over main signed; this is +2.54).

`total_js_kb_gz` 717.8 → 723.8 (+6.0 KB gz, two parts):

- Main itself is over its ceiling. Plain main `ee7669d9` measures 720.71 / 175.03 locally against
  717.8 (+2.91): the 717.8 above was measured on `db43680c`, and the changes merged after that
  base were not measured against it.
- The My Runner resources editor built only from the runner manifest schema (the JSON textarea is
  gone) adds 3.06 KB gz on top of that main: the `my-runner` chunk +1.38 (entry pickers,
  credential handling, field errors from the server), `schema-form` +0.95 (checklist and map
  widgets, stale-option labels, server errors at the field), `runner` +0.38 and `locale-ru`
  +0.47 (help text for every catalog field), measured per chunk against the same main build.
  The manifest and the test fixtures are not bundled. The earlier note on this MR that removing
  the JSON editor offsets the form was wrong: it compared against main ceilings, not main builds.

Merged with main `ee7669d9` the build measures 723.77 / 175.03; the ceiling is the smallest 0.1
step above that. `login.initial_js_kb_gz` unchanged. Revert: set it back to 717.8 together with
reverting the MR. Lead sign-off: the product lead, on the card.

`total_js_kb_gz` 723.8 → 722.1 and `login.initial_js_kb_gz` 175.1 → 173.2 (both lowered):

- Main `d3b9526c` measured 723.94 / 175.03 (red by 0.14): the 723.8 above was measured with main
  `ee7669d9`, and the theme panel fix merged in between (`4fa37385`) adds to the settings chunk.
- `src/i18n/format.ts` passed the whole date-fns `ru` locale to `formatDistanceToNow`, which reads
  only `locale.formatDistance`; the rest of the object (localize, match, formatLong, formatRelative
  tables) sat in the initial chunk. It now passes `{ formatDistance }` from the locale's own file.
  Output is unchanged; `src/i18n/__tests__/i18n.test.ts` pins it for en and ru. `enUS` stays whole:
  date-fns bundles it as its default locale regardless.

Measured locally on main `d3b9526c` with the change: 722.07 / 173.18 (−1.87 / −1.85); each ceiling is
the smallest 0.1 step above that. Revert: restore 723.8 / 175.1 together with reverting the MR.

`total_js_kb_gz` 722.1 → 722.7 (+0.6 KB gz); `login.initial_js_kb_gz` unchanged (173.2):

- Runner agent settings pick the agent, its access grant and the workspace by name instead of three
  typed UUIDs: `grant-picker` / `workspace-picker` in `schema-form` (`mesh-refs.tsx`), agent options in
  the Runner resource editor, their en/ru strings. Per chunk against main `a4c00d12`: `schema-form`
  +1.02, `my-runner` +0.19, `locale-ru` +0.14, `integrations` +0.10 KB gz; the initial chunk is untouched.
- Trimmed before raising: the "every grant into the workspace" mode and its reporter-grant widget
  (that field has no edit form yet), a duplicate "not found" string, the date guard (−0.08). The rest is
  the two grant requests, the 403 fallback and the empty/loading/error states the screen needs; lazy
  loading does not help, this metric counts every chunk.

Main `a4c00d12` measured 721.08 against the 722.1 ceiling; with the change 722.69 locally, so the
ceiling is the smallest 0.1 step above that. Revert: restore 722.1 together with reverting the MR.
Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 722.7 → 724.2 (+1.5 KB gz); `login.initial_js_kb_gz` unchanged (173.2):

- Two MRs that each fit the 722.7 ceiling landed one after the other, and their sum does not fit:
  the schema-form pickers by name (722.56 on its own branch) and the My Runner binding section. Main `e144e905` measures 723.32. The perf-bundle job is skipped on main
  pipelines, so the overrun showed up only on the next MR's merge gate.
- Members edit dialog (the earlier change: owners/admins set a member's email, username and password) on top:
  `workspace-settings` +0.49, `locale-ru` +0.26, `settings` +0.19 KB gz. Main `f40dab5d` measures 724.12.
- Trimmed before raising: looked for unused ru/en locale keys (none outside the dynamic `apiErrors`
  codes) and for dead code in the grown chunks; the growth is the two features themselves.

Main `f40dab5d` measured 724.12 against the 722.7 ceiling, so the ceiling is the smallest 0.1 step
above that. Revert: restore 722.7 after trimming ≥1.42 KB gz. Lead sign-off: the product lead, requested on the card.

`total_js_kb_gz` 724.2 → 727.1 (+2.9 KB gz); `login.initial_js_kb_gz` 173.2 → 173.3 (+0.03 measured):

- 724.2 is main's own ceiling (`9da16ab4b`, main `f40dab5d` measures 724.12); +2.9 is this MR, the
  Runner screen reworked: agents edited from their row in a dialog, Accounts / Pools / Profiles as
  separate tabs, readable profile names and the on-screen explanation of what a profile is, workspace
  isolation of agent settings moved to the Agents tab. Per chunk against main `f40dab5d`: `my-runner`
  +1.98, `locale-ru` +0.60, `runner` (en strings) +0.45, `index` +0.04 KB gz (the "Runner" header
  breadcrumb, a route entry in the initial chunk).
- Nothing left to trim without dropping accepted UI: the growth is the dialog, the tabs and the copy
  the lead signed off; lazy loading does not help, this metric counts every chunk.

CI `perf-bundle` on the branch measured 727.09 / 173.23 (local build: the same), so the ceiling is the
smallest 0.1 step above that. Revert: restore 724.2 / 173.2 together with reverting the MR.
Lead sign-off: the product lead, signed 2026-10-29 on the card.

`total_js_kb_gz` 727.1 → 727.2 (+0.1 KB gz), main's own growth:

- The list view now pages in the table's sort order (`b9baaf972`, merged in parallel with the Runner
  screen): `list-view.tsx` +26 lines. Each MR passed its own measurement; together main `29d95756f`
  measures 727.2 in CI `perf-bundle` (job 461259), over the 727.1 ceiling by 0.1.
- No UI share changed: the Runner screen's +2.9 stays as signed above.

The ceiling is the smallest 0.1 step above the CI measurement. Revert: restore 727.1 after trimming
≥0.1 KB gz. Lead sign-off: the product lead, signed 2026-10-29 on the card (raise only main's measured share).

`total_js_kb_gz` 727.2 → 727.4 (+0.2 KB gz), keyboard access to the Runner agent dialog:

- The dialog opened without taking focus, so Escape and Tab from the keyboard never reached it
  (found in the live check of the Runner screen). The dialog now takes focus on open and gives it
  back to the row's Edit button on close: `my-runner` chunk, total 727.20 → 727.32 measured on
  main `c247a98a8`.
- Cheaper variants do not work: React applies `autoFocus` only to form fields, not to the dialog
  box, so the focus effect is the smallest fix.

The ceiling is the smallest 0.1 step above 727.32. Revert: restore 727.2 together with reverting
this change. Lead sign-off: the product lead, signed 2026-10-29 on the card.

`total_js_kb_gz` 727.4 → 727.6 (+0.2 on main's ceiling at merge, the earlier change; +0.11 KB gz measured); `login.initial_js_kb_gz` unchanged (173.3):

- Avatar initials taken from the first letter or digit of each word (`components/ui/avatar.tsx`:
  `/[\p{L}\p{N}]/u` instead of the word's first character), so "Hana (QA Mesh)" shows "HQ", not "H(".
  The emitted code grows by about 25 bytes, `index` by 9 bytes gz.
- The rest is hash churn, not code: `index` gets a new content hash, and every lazy chunk that imports
  from it carries the new file name, about +4 bytes gz each across the route chunks. Main had 0.01 KB
  of headroom when measured (727.09 against 727.1), so any change to the initial chunk lands over the ceiling.
- Nothing to trim: the diff is one regex and one split pattern.

CI `perf-bundle` on the branch measured 727.21 / 173.24 against main at 727.1 (+0.11), so the raise is the smallest 0.1 step above
that. Revert: lower by 0.2 together with reverting the MR. Lead sign-off: the product lead, signed 2026-10-29 on the card for +0.2 on main's ceiling at merge (if another raise lands first, rebase and recount from main).

`total_js_kb_gz` 727.6 → 728.2 (+0.76 KB gz measured), the earlier change; `login.initial_js_kb_gz` unchanged (173.28 against 173.29 on main):

- Lifecycle of external extension iframe screens: Mesh tells the screen about a refused token renewal
  (`mesh.context.error`), revokes it on 403/404/409 (`mesh.revoked`), renews the token at 80% of its
  lifetime, and accepts optional `mesh.app-ready` / `mesh.app-error` with an error screen. The code is in
  `components/extensions/external-frame.tsx`, `lib/extension-frame-protocol.ts` and two strings per locale,
  all in lazy chunks.
- Nothing to trim without dropping a criterion of the change: the growth is the renewal, revocation and
  app-error paths themselves.

CI `perf-bundle` measured 727.40 on main `0461cafe` (job 477809) and 728.16 on the branch `745d7f43` (job 478099),
so the ceiling is the smallest 0.1 step above that. Revert: restore 727.6 together with reverting the MR.
Lead sign-off: the product lead, signed 2026-10-29 on the card (if main raises its ceiling first through another signed MR,
take main's ceiling plus 0.6).

`total_js_kb_gz` 728.2 → 729.3 (+1.0 KB gz measured on top of the earlier change) and `login.initial_js_kb_gz` 173.3 → 173.5 (+0.11 KB gz measured):

- Extension screens get deep links and a project-scoped external screen: the section protocol
  (`normalizeSection`, `frameSrc` with a section, `mesh.state`) and the screen page live in the lazy
  extension-screen chunk; that is most of the total.
- The initial chunk carries what the shell needs to draw the project strip and the left menu before
  any lazy chunk loads: the `project.view` slot in the slot registry (`useProjectScreens`), the merged
  project-tab candidates, the active-tab match for a section path in the header and the sidebar, and
  the `loaded` flag of the project store (an unknown project is "unavailable", not a spinner).
- Nothing to trim without moving the strip itself out of the shell.

CI `perf-bundle` on the branch measured 728.4 / 173.41 against ceilings 727.6 / 173.3. After the previous change landed first
(main ceiling 728.2) the branch was re-measured on the merged tree (local build, same script): 729.14 / 173.44
(the review fixes, hash validation and the sidebar fallback, cost 0.06 KB); the last one, encoded separators refused in
the hash too, brought the total to 729.21. The ceilings are the smallest 0.1 step above that: 729.3 / 173.5
(main's ceiling + 1.1, the signed maximum).
Revert: lower both together with reverting the MR. Lead sign-off: the product lead, 2026-10-29 on the card, on the condition
to recount from the merged tree if the earlier change lands first.
