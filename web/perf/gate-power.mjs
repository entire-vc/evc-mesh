#!/usr/bin/env node
// Power of the perf-counters gate, from measured runs. The gate in
// perf/counters.spec.ts fails a path when the MINIMUM of PERF_REPEAT runs of
// a metric is over its ceiling in budget.json. For a metric that jitters, that
// gate has two error rates, and they pull the ceiling in opposite directions:
//
//   false red  — unchanged code fails:      P(min of N > C) = S(C)^N
//   miss       — a real +d regression passes: 1 − P(min of N of X+d > C)
//                                            = 1 − S(C−d)^N
//
// where S(c) is the share of single runs above c. A ceiling at the per-run
// maximum drives false red to 0 and, on a metric that spreads wider than d,
// drives the catch rate to ~0 as well. This script prints both numbers per
// metric for the ceiling in budget.json, and the smallest PERF_REPEAT/ceiling
// pair that keeps false red under --alpha while catching +d at --power.
//
// Runs are treated as independent draws, with the first run of each path
// pooled apart from the later ones: on some paths it is systematically
// different (board.open's first run is always 43 DOM mutations, the later
// ones 45), and a model that ignores that reports a 30% false red nobody
// has ever seen. Where a ceiling sits above every sample, S is 0 and the real
// tail is unknown: the "≤" column is the rule-of-three bound (3/n).
//
// Independence is an assumption, and CI broke it: runs of ONE job share the
// runner's load and the ephemeral API's latency, and the counters follow that
// latency (board.drag before #da03cd09: a slow POST /move put every run of the
// job in the 448/16/~100 mode). On 303 reports from 04–10.10 the model put the
// joint false red at 0.28%; 2.0% of those jobs actually went red on code that
// passed on retry. So every row also carries the OBSERVED job-level rate — the
// share of reports whose own gated minimum is over the ceiling — and
// `tightest` must keep both under --alpha: the model's rule-of-three bound
// covers the unseen tail, the observed rate covers what the model cannot see.
// (The observed rate is not rule-of-three bounded itself: 3/jobs would need
// 300+ reports before any ceiling passes at α=1%.) Only reports with at least
// --repeat runs count, minimised over their first --repeat runs.
//
// Usage, from web/:
//   node perf/gate-power.mjs [--repeat 3] [--shift 1] [--alpha 0.01]
//        [--power 0.95] [--max-repeat 25] [--only board.drag] report.json…
//   node perf/gate-power.mjs --selftest
//
// Reports are web/perf-counters-report.json artifacts of the perf-counters
// job; a calibration record (PERF_REPEAT=25 PERF_RECORD=1) is the best input.
// Pool only reports of the same code — a merged fix changes the distribution.

import { readFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const GATED = [
  "react_commits",
  "board_card_commits",
  "layout_count",
  "recalc_style_count",
  "dom_mutations",
];

/** Share of samples strictly above c. */
export function survival(samples, c) {
  let above = 0;
  for (const x of samples) if (x > c) above++;
  return above / samples.length;
}

/**
 * A metric's runs, split by position. The first run of a path is not drawn
 * from the same distribution as the later ones (measured: board.open's first
 * run is always 43 DOM mutations, the later ones 45), so the gate's minimum
 * is modelled as first run × (N−1) later runs, each set pooled separately.
 * Either set may be empty; the other one then stands in for it.
 */
export function dist(first, rest) {
  return {
    first: first.length ? first : rest,
    rest: rest.length ? rest : first,
  };
}

/** P(min of N runs > c). bound=true replaces an empty tail by 3/n (rule of three). */
function pAllAbove(d, c, repeat, bound = false) {
  const tail = (xs) => {
    const s = survival(xs, c);
    return bound && s === 0 ? Math.min(1, 3 / xs.length) : s;
  };
  return tail(d.first) * tail(d.rest) ** (repeat - 1);
}

export function falseRed(d, ceiling, repeat) {
  return pAllAbove(d, ceiling, repeat);
}

/** Upper bound on false red where no sample is above the ceiling. */
export function falseRedBound(d, ceiling, repeat) {
  return pAllAbove(d, ceiling, repeat, true);
}

/** A +shift regression moves every run up by shift; caught when min > ceiling. */
export function catchRate(d, ceiling, repeat, shift) {
  return pAllAbove(d, ceiling - shift, repeat);
}

/**
 * The lowest observed value whose false-red BOUND at `repeat` is ≤ alpha —
 * the tightest ceiling the data can defend at that repeat. Lower ceiling =
 * more sensitive, so nothing above it is worth considering. null when even the
 * largest observed value is not defensible (too few samples).
 */
export function tightest(d, repeat, alpha, jobMins = []) {
  const grid = [...new Set([...d.first, ...d.rest, ...jobMins])].sort((a, b) => a - b);
  for (const c of grid)
    if (falseRedBound(d, c, repeat) <= alpha && observedFalseRed(jobMins, c) <= alpha)
      return c;
  return null;
}

/**
 * Share of jobs whose own gated minimum is over the ceiling — what CI
 * actually did, correlation within a job included. 0 when there are no jobs.
 */
export function observedFalseRed(jobMins, ceiling) {
  return jobMins.length ? survival(jobMins, ceiling) : 0;
}


/**
 * Smallest repeat in [from, maxRepeat] whose tightest ceiling also catches a
 * +shift regression with probability ≥ power. null when no repeat up to
 * maxRepeat does it — the spread itself has to shrink.
 */
export function recommend(d, { from, maxRepeat, alpha, power, shift }) {
  // Job-level minima exist only for the repeat the reports were taken at, so
  // the search over other repeats is the independent model alone.
  for (let n = from; n <= maxRepeat; n++) {
    const c = tightest(d, n, alpha);
    if (c !== null && catchRate(d, c, n, shift) >= power)
      return { repeat: n, ceiling: c };
  }
  return null;
}

/** Smallest shift the gate catches with probability ≥ power. */
export function minDetectable(d, ceiling, repeat, power) {
  const all = [...d.first, ...d.rest];
  // A ceiling below the whole sample catches +1 already; the loop must run once.
  const limit = Math.max(1, ceiling - Math.min(...all) + 1);
  for (let k = 1; k <= limit; k++) {
    if (catchRate(d, ceiling, repeat, k) >= power) return k;
  }
  return Infinity;
}

function pct(p) {
  if (p === 0) return "0";
  if (p < 0.0001) return "<0.01%";
  return `${(p * 100).toFixed(p < 0.1 ? 2 : 1)}%`;
}

function parseArgs(argv) {
  const opts = {
    repeat: 3,
    shift: 1,
    alpha: 0.01,
    power: 0.95,
    maxRepeat: 25,
    only: null,
    files: [],
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => {
      const v = argv[++i];
      if (v === undefined) throw new Error(`${a} needs a value`);
      return v;
    };
    if (a === "--repeat") opts.repeat = Number(next());
    else if (a === "--shift") opts.shift = Number(next());
    else if (a === "--alpha") opts.alpha = Number(next());
    else if (a === "--power") opts.power = Number(next());
    else if (a === "--max-repeat") opts.maxRepeat = Number(next());
    else if (a === "--only") opts.only = next();
    else if (a === "--budget") opts.budget = next();
    else if (a.startsWith("--")) throw new Error(`unknown flag ${a}`);
    else opts.files.push(a);
  }
  for (const k of ["repeat", "maxRepeat", "shift"]) {
    if (!Number.isInteger(opts[k]) || opts[k] < 1)
      throw new Error(
        `--${k === "maxRepeat" ? "max-repeat" : k} must be a positive integer, got ${opts[k]}`,
      );
  }
  for (const k of ["alpha", "power"]) {
    if (!(opts[k] > 0 && opts[k] < 1))
      throw new Error(`--${k} must be between 0 and 1, got ${opts[k]}`);
  }
  return opts;
}

/**
 * key → pooled per-run samples (first run / later runs) plus each report's own
 * gated minimum over its first `repeat` runs (jobMins), across every report.
 */
export function poolReports(reports, repeat) {
  const pooled = new Map();
  for (const report of reports) {
    for (const [path, { runs }] of Object.entries(report.paths ?? {})) {
      for (const metric of GATED) {
        const key = `${path}.${metric}`;
        if (!pooled.has(key)) pooled.set(key, { first: [], rest: [], jobMins: [] });
        const entry = pooled.get(key);
        runs.forEach((r, i) => {
          if (typeof r[metric] === "number") entry[i === 0 ? "first" : "rest"].push(r[metric]);
        });
        const gatedRuns = runs.slice(0, repeat).map((r) => r[metric]);
        if (gatedRuns.length === repeat && gatedRuns.every((x) => typeof x === "number"))
          entry.jobMins.push(Math.min(...gatedRuns));
      }
    }
  }
  return pooled;
}

function poolRuns(files, repeat) {
  return poolReports(
    files.map((f) => JSON.parse(readFileSync(f, "utf8"))),
    repeat,
  );
}

export function analyse(pooled, budget, opts) {
  const rows = [];
  for (const [key, split] of pooled) {
    if (opts.only && !key.startsWith(`${opts.only}.`)) continue;
    const samples = [...split.first, ...split.rest];
    if (samples.length === 0) continue;
    const d = dist(split.first, split.rest);
    const jobMins = split.jobMins ?? [];
    const ceiling = budget[key];
    if (ceiling === undefined) continue;
    const tight = tightest(d, opts.repeat, opts.alpha, jobMins);
    const rec = recommend(d, {
      from: opts.repeat,
      maxRepeat: opts.maxRepeat,
      alpha: opts.alpha,
      power: opts.power,
      shift: opts.shift,
    });
    rows.push({
      key,
      n: samples.length,
      lo: Math.min(...samples),
      hi: Math.max(...samples),
      ceiling,
      falseRed: falseRed(d, ceiling, opts.repeat),
      falseRedBound: falseRedBound(d, ceiling, opts.repeat),
      caught: catchRate(d, ceiling, opts.repeat, opts.shift),
      minDetectable: minDetectable(d, ceiling, opts.repeat, opts.power),
      jobs: jobMins.length,
      observed: observedFalseRed(jobMins, ceiling),
      tight,
      tightCaught: tight === null ? 0 : catchRate(d, tight, opts.repeat, opts.shift),
      rec,
      recFalseRedBound: rec ? falseRedBound(d, rec.ceiling, rec.repeat) : null,
    });
  }
  return rows;
}

function print(rows, opts) {
  const head = [
    "metric",
    "n",
    "range",
    "ceil",
    `falseRed@N=${opts.repeat}`,
    "≤",
    "observed (jobs)",
    `caught(+${opts.shift})`,
    `minDetect@${pct(opts.power)}`,
    `tightest@N=${opts.repeat} (caught +${opts.shift})`,
    `suggest N/ceil (α≤${pct(opts.alpha)}, +${opts.shift} caught ≥${pct(opts.power)})`,
  ];
  const lines = rows.map((r) => [
    r.key,
    String(r.n),
    `${r.lo}–${r.hi}`,
    String(r.ceiling),
    pct(r.falseRed),
    pct(r.falseRedBound),
    r.jobs ? `${pct(r.observed)} (${r.jobs})` : "-",
    pct(r.caught),
    r.minDetectable === Infinity ? "never" : `+${r.minDetectable}`,
    r.tight === null ? "none" : `${r.tight} (${pct(r.tightCaught)})`,
    r.rec
      ? `N=${r.rec.repeat} ceil=${r.rec.ceiling} (α≤${pct(r.recFalseRedBound)})`
      : `none up to N=${opts.maxRepeat}`,
  ]);
  const widths = head.map((h, i) =>
    Math.max(h.length, ...lines.map((l) => l[i].length)),
  );
  const fmt = (cols) => cols.map((c, i) => c.padEnd(widths[i])).join("  ");
  console.log(fmt(head));
  for (const l of lines) console.log(fmt(l));
  const joint = 1 - rows.reduce((acc, r) => acc * (1 - r.falseRed), 1);
  const jointBound =
    1 - rows.reduce((acc, r) => acc * (1 - r.falseRedBound), 1);
  const blind = rows.filter((r) => r.caught < opts.power).map((r) => r.key);
  const jobs = Math.max(0, ...rows.map((r) => r.jobs));
  const observedJoint = jobs ? rows.reduce((a, r) => a + r.observed * r.jobs, 0) / jobs : 0;
  // Runs within one job are correlated when CI did worse than the model's own bound.
  const correlated = rows.filter((r) => r.observed > r.falseRedBound).map((r) => r.key);
  console.log(
    `\njoint false red at N=${opts.repeat} over ${rows.length} metrics (independent): ${pct(joint)} (bound ${pct(jointBound)})`,
  );
  if (jobs)
    console.log(
      `observed: ≤${pct(observedJoint)} of ${jobs} jobs went red on at least one metric (sum over metrics, upper bound)` +
        (correlated.length
          ? `\nruns within a job are NOT independent for (observed > model bound):\n  ${correlated.join("\n  ")}`
          : ""),
    );
  console.log(
    `metrics that let a +${opts.shift} regression through more than ${pct(1 - opts.power)} of the time: ${blind.length}${blind.length ? `\n  ${blind.join("\n  ")}` : ""}`,
  );
}

function selftest() {
  let bad = 0;
  const expect = (name, got, want) => {
    const ok =
      typeof want === "function"
        ? want(got)
        : typeof want === "number"
          ? Math.abs(got - want) < 1e-12
          : Object.is(got, want);
    if (!ok) {
      bad++;
      console.error(`FAIL ${name}: got ${JSON.stringify(got)}`);
    }
  };
  const iid = (xs) => dist(xs, xs);
  const rec = (d) =>
    recommend(d, {
      from: 3,
      maxRepeat: 25,
      alpha: 0.01,
      power: 0.95,
      shift: 1,
    });

  const flat = iid(Array(20).fill(56));
  expect(
    "flat: ceiling at the value never false-reds",
    falseRed(flat, 56, 3),
    0,
  );
  expect("flat: +1 always caught", catchRate(flat, 56, 3, 1), 1);
  expect(
    "flat: ceiling one above floor misses +1",
    catchRate(flat, 57, 3, 1),
    0,
  );
  expect(
    "rule of three when no sample is above",
    falseRedBound(flat, 56, 3),
    (3 / 20) ** 3,
  );

  // The board.drag shape before the sensor fix: wide, no floor. Ceiling=max.
  const wide = iid([
    1332, 1392, 1392, 1392, 1392, 1392, 1392, 1392, 1392, 1401, 1401, 1401,
    1401, 1401, 1401, 1401, 1401, 1401, 1402, 1402, 1404, 1404, 1410, 1410,
    1410,
  ]);
  expect("wide, ceiling=max: no false red", falseRed(wide, 1410, 3), 0);
  expect(
    "wide, ceiling=max: +1 caught only (3/25)^3",
    catchRate(wide, 1410, 3, 1),
    (3 / 25) ** 3,
  );
  expect(
    "wide, ceiling=max: blind below the spread",
    minDetectable(wide, 1410, 3, 0.95),
    (k) => k > 70,
  );
  expect("wide: nothing reaches +1 at 95% by N=25", rec(wide), null);
  // The mirror mistake: a ceiling at the per-run median false-reds.
  expect(
    "wide, ceiling=median: false red over 1% per metric",
    falseRed(wide, 1401, 3),
    (p) => p > 0.01,
  );

  // Two-valued jitter (447 most runs, 448 sometimes): ceiling=max 448 is blind
  // to +1, ceiling=floor 447 catches it and false-reds only on (2/15)^N.
  const twoValued = iid([...Array(13).fill(447), 448, 448]);
  expect(
    "two-valued, ceiling=max: +1 mostly missed",
    catchRate(twoValued, 448, 3, 1),
    (p) => p < 0.01,
  );
  expect(
    "two-valued, ceiling=floor: +1 always caught",
    catchRate(twoValued, 447, 3, 1),
    1,
  );
  // A lone calibration record has one first run, so the rule-of-three bound
  // on it is 1; about ten reports' worth of first runs make 447 defensible.
  const twoValuedTwoRecords = dist(Array(10).fill(447), [
    ...Array(12).fill(447),
    448,
    448,
  ]);
  expect(
    "two-valued: floor at N=3",
    rec(twoValuedTwoRecords),
    (r) => r && r.repeat === 3 && r.ceiling === 447,
  );
  expect(
    "two-valued: tightest at N=3 is the floor",
    tightest(twoValuedTwoRecords, 3, 0.01),
    447,
  );

  // Spread jitter (12/13/16): the floor is only safe with more repeats.
  const spread = iid([
    12, 12, 12, 12, 13, 13, 13, 13, 13, 13, 13, 13, 13, 16, 16,
  ]);
  expect(
    "spread: needs N>3 to put the ceiling on the floor",
    rec(spread),
    (r) => r && r.repeat > 3 && r.ceiling === 12,
  );

  // Positional: first run always 43, later runs 45. The gate's min is always
  // 43; pooling the runs as one set would claim false red (2/3)^3 ≈ 30%.
  const positional = dist(Array(10).fill(43), Array(20).fill(45));
  expect(
    "positional: ceiling 43 never false-reds",
    falseRed(positional, 43, 3),
    0,
  );
  expect("positional: +1 always caught", catchRate(positional, 43, 3, 1), 1);
  expect(
    "pooled positional would lie",
    falseRed(iid([...Array(10).fill(43), ...Array(20).fill(45)]), 43, 3),
    (p) => p > 0.25,
  );

  // A ceiling below every sample: +1 is caught, and minDetectable must agree.
  expect("ceiling below the floor: +1 caught", catchRate(flat, 50, 3, 1), 1);
  expect(
    "ceiling below the floor: minDetectable is 1",
    minDetectable(flat, 50, 3, 0.95),
    1,
  );

  // One side empty: the other stands in (a single PERF_REPEAT=1 report has no later runs).
  const onlyFirst = dist([447, 447, 448], []);
  expect(
    "empty rest: falls back to first",
    falseRed(onlyFirst, 447, 3),
    (1 / 3) ** 3,
  );
  const onlyRest = dist([], [447, 447, 448]);
  expect(
    "empty first: falls back to rest",
    falseRed(onlyRest, 447, 3),
    (1 / 3) ** 3,
  );

  // Correlated within a job (the CI shape): 8 jobs of 400 have every run at
  // 99, the rest are spread 70–85. Pooled as independent runs the model sees
  // 24 of 1200 above 98 and calls the false red (2%)^3 ≈ 0.0008%; CI went red
  // in 2% of jobs. The observed rate must say so and `tightest` must not pick 98.
  const correlatedReports = [];
  for (let j = 0; j < 400; j++) {
    const v = j < 8 ? [99, 99, 99] : [70 + (j % 16), 72 + (j % 14), 75 + (j % 11)];
    correlatedReports.push({
      paths: { "board.drag": { runs: v.map((x) => ({ recalc_style_count: x })) } },
    });
  }
  const corr = poolReports(correlatedReports, 3).get("board.drag.recalc_style_count");
  const corrD = dist(corr.first, corr.rest);
  expect("correlated: independent model calls 98 safe", falseRed(corrD, 98, 3), (p) => p < 0.0001);
  expect("correlated: observed job rate at 98 is 2%", observedFalseRed(corr.jobMins, 98), 0.02);
  expect(
    "correlated: tightest refuses 98 once jobs are counted",
    tightest(corrD, 3, 0.01, corr.jobMins),
    99,
  );
  expect(
    "correlated: without jobs the model alone would take a ceiling ≤ 98",
    tightest(corrD, 3, 0.01),
    (c) => c !== null && c <= 98,
  );
  expect("no jobs: observed rate is 0", observedFalseRed([], 5), 0);
  expect(
    "short report (fewer runs than --repeat) gives no job minimum",
    poolReports([{ paths: { p: { runs: [{ layout_count: 1 }] } } }], 3).get("p.layout_count").jobMins,
    (xs) => xs.length === 0,
  );

  if (bad) {
    console.error(`[gate-power] selftest: ${bad} case(s) behaved wrongly`);
    process.exit(1);
  }
  console.log("[gate-power] selftest OK");
}

if (import.meta.url === `file://${process.argv[1]}`) {
  if (process.argv.includes("--selftest")) {
    selftest();
    process.exit(0);
  }
  const opts = parseArgs(process.argv.slice(2));
  if (opts.files.length === 0) {
    console.error(
      "usage: node perf/gate-power.mjs [flags] perf-counters-report.json…  (or --selftest)",
    );
    process.exit(2);
  }
  const budget = JSON.parse(
    readFileSync(opts.budget ?? resolve(here, "budget.json"), "utf8"),
  );
  print(analyse(poolRuns(opts.files, opts.repeat), budget, opts), opts);
}
