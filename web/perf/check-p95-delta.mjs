#!/usr/bin/env node
/**
 * Gate for the task-open p95 (docs/perf/p95-task-open.mjs, CI job
 * `perf-task-open`). The harness measures the same click -> heading -> next
 * frame path on a build of main and a build of the branch, 8 configurations
 * (build x gitlab on/off x cpu 4x/1x) of 20 opens each. This script pairs
 * `branch|<cfg>` with `main|<cfg>` and fails when any pair's p95 delta is over
 * the threshold.
 *
 *   node perf/check-p95-delta.mjs <p95.json> [--max-delta-ms 100] [--report out.json]
 *   node perf/check-p95-delta.mjs --selftest
 *
 * Fail-closed: a missing pair, n != expected, or a non-finite p95 is a
 * failure, not "nothing to compare".
 */
import { readFileSync, writeFileSync } from "node:fs";

const EXPECTED_CONFIGS = 8;
const EXPECTED_N = 20;

export function evaluate(result, maxDelta) {
  const errors = [];
  const rows = [];
  const keys = Object.keys(result);
  if (keys.length !== EXPECTED_CONFIGS) {
    errors.push(`expected ${EXPECTED_CONFIGS} configurations, got ${keys.length}`);
  }
  for (const key of keys) {
    const [build, ...rest] = key.split("|");
    if (build !== "branch") continue;
    const cfg = rest.join("|");
    const b = result[key];
    const m = result[`main|${cfg}`];
    if (!m) {
      errors.push(`no main counterpart for ${key}`);
      continue;
    }
    for (const [name, r] of [[key, b], [`main|${cfg}`, m]]) {
      if (r.n !== EXPECTED_N) errors.push(`${name}: n=${r.n}, expected ${EXPECTED_N}`);
      if (!Number.isFinite(r.p95)) errors.push(`${name}: p95 is not a number`);
    }
    const delta = b.p95 - m.p95;
    rows.push({ cfg, main_p95: m.p95, branch_p95: b.p95, delta, over: delta > maxDelta });
  }
  if (rows.length !== EXPECTED_CONFIGS / 2) {
    errors.push(`expected ${EXPECTED_CONFIGS / 2} branch/main pairs, got ${rows.length}`);
  }
  for (const r of rows) {
    if (r.over) errors.push(`${r.cfg}: p95 delta ${r.delta.toFixed(0)} ms > ${maxDelta} ms`);
  }
  return { rows, errors };
}

function print(rows, maxDelta) {
  console.log(`task-open p95 (ms), threshold: branch - main <= ${maxDelta}`);
  for (const r of rows) {
    console.log(
      `  ${r.cfg.padEnd(22)} main ${Math.round(r.main_p95)}  branch ${Math.round(r.branch_p95)}  delta ${r.delta >= 0 ? "+" : ""}${Math.round(r.delta)}  ${r.over ? "OVER" : "ok"}`
    );
  }
}

function selftest() {
  const mk = (p95, n = EXPECTED_N) => ({ n, p95 });
  const full = (branchP95) => {
    const o = {};
    for (const cfg of ["gitlab-on|cpu4x", "gitlab-off|cpu4x", "gitlab-on|cpu1x", "gitlab-off|cpu1x"]) {
      o[`main|${cfg}`] = mk(170);
      o[`branch|${cfg}`] = mk(branchP95);
    }
    return o;
  };
  const cases = [
    ["equal builds pass", full(170), 0],
    ["+100 ms is the boundary and passes", full(270), 0],
    ["+101 ms fails", full(271), 1],
    ["faster branch passes", full(50), 0],
    ["missing config fails", (() => { const o = full(170); delete o["main|gitlab-on|cpu1x"]; return o; })(), 1],
    ["short run (n=5) fails", (() => { const o = full(170); o["branch|gitlab-on|cpu4x"].n = 5; return o; })(), 1],
    ["NaN p95 fails", (() => { const o = full(170); o["branch|gitlab-on|cpu4x"].p95 = NaN; return o; })(), 1],
  ];
  let bad = 0;
  for (const [name, data, want] of cases) {
    const got = evaluate(data, 100).errors.length ? 1 : 0;
    const ok = got === want;
    console.log(`${ok ? "PASS" : "FAIL"}  ${name}`);
    if (!ok) bad++;
  }
  process.exit(bad ? 1 : 0);
}

const argv = process.argv.slice(2);
if (argv.includes("--selftest")) selftest();
else {
  const file = argv.find((a) => !a.startsWith("--") && argv[argv.indexOf(a) - 1] !== "--max-delta-ms" && argv[argv.indexOf(a) - 1] !== "--report");
  const opt = (name, d) => (argv.includes(name) ? argv[argv.indexOf(name) + 1] : d);
  const maxDelta = Number(opt("--max-delta-ms", 100));
  const report = opt("--report", null);
  if (!file || !Number.isFinite(maxDelta)) {
    console.error("usage: check-p95-delta.mjs <p95.json> [--max-delta-ms N] [--report out.json]");
    process.exit(2);
  }
  const { rows, errors } = evaluate(JSON.parse(readFileSync(file, "utf8")), maxDelta);
  print(rows, maxDelta);
  if (report) writeFileSync(report, JSON.stringify({ max_delta_ms: maxDelta, rows, errors }, null, 1));
  if (errors.length) {
    for (const e of errors) console.error(`perf-task-open: ${e}`);
    process.exit(1);
  }
  console.log("perf-task-open: within threshold");
}
