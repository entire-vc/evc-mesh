# Perf budget — the храповик for Mesh web

`budget.json` sets a ceiling per metric. CI fails the build when a metric goes
over its ceiling. It never fails when a metric goes *under* — going under is
the whole point.

## The one rule

**Improved a number → lower its ceiling in the same MR.** The ceiling is not
"the biggest we've ever measured", it's "the biggest we're willing to accept
today". A ceiling left above the real value stops protecting anything.

**Raising a ceiling needs a stated reason in the MR description and lead
sign-off (Garfield).** "Tests were red so I bumped the number" is not a
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

## `login.initial_js_kb_gz` vs `total_js_kb_gz` after route splitting

Routes are `React.lazy` since !1006, so the two numbers diverged: on
`9514eba3` `login.initial_js_kb_gz` is 148.96 (was 432.07) and
`total_js_kb_gz` is 488.75 (was 432.07). Ceilings are 150 and 490.

`total_js_kb_gz` went **up** by ~56 KB: 95 chunks are gzipped one by one, and
small chunks compress worse than one big one (gzip -9 over the same files:
~7% more as separate files than as one stream); the rest is code that landed on
`main` since the 432 baseline. It is not what a user downloads on `/login`, so
it is a guard against unbounded growth, not a target. Raising it needed a
reason and lead sign-off (rule above) — see the MR.

## Proving the gate actually gates (do this again after touching the script)

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

### `board.open.dom_mutations` 43 → 74 after route splitting

Since !1006 the board is a lazy chunk. The first `import()` of it makes Vite
insert one `<link rel="modulepreload">` (or stylesheet) into `<head>` per
dependency the entry chunk does not already carry — 30 JS + 2 CSS for the
board — and the observer watches the whole `document`, `<head>` included.
74 − 43 = 31 ≈ those 32 insertions. It happens once per session, adds no
layout (`layout_count` unchanged at 3) and no React work. Measured on
pipeline 5163 (runs 74 / 76 / 76, minimum gated). Lead sign-off: Garfield.
Bringing it back to 43 (warm the board chunk on idle from `AppLayout`, or
scope the observer to `#root`) lowers the ceiling again, per the rule above.

`task.open.dom_mutations` 52 → 56 (+4) is the same change, found by running the
whole spec: with `board.open` red, Playwright reports the other three paths as
"did not run", so this one was invisible in CI. Measured locally on `558c1170`
(52) vs `9514eba3` + fix (56, three runs 56 / 56 / 56). The +4 is the same
mechanism as `board.open`: the first `import()` of `task-detail` inserts 4
`<link rel="modulepreload">` into `<head>` (`task-detail`, `_task-panel`,
`_template`, `_circle`; the rest of its closure is already loaded by the
board). Lead sign-off: Garfield.

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
