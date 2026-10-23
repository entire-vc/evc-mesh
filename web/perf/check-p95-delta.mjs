#!/usr/bin/env node
/**
 * Gate for the task-open p95 (docs/perf/p95-task-open.mjs, CI job
 * `perf-task-open`). The harness measures the same click -> heading -> next
 * frame path on a build of main and a build of the branch, 8 configurations
 * (build x gitlab on/off x cpu 4x/1x) of 20 opens each. This script pairs
 * `branch|<cfg>` with `main|<cfg>` and fails when any pair's p95 delta is over
 * the threshold.
 *
 *   node perf/check-p95-delta.mjs <p95.json> [--confirm-with <second.json>] [--max-delta-ms 100] [--report out.json]
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

/**
 * Confirmation of a red first run by a second full run. A configuration is
 * confirmed only when it is over the threshold in BOTH runs: p95 of 20 opens is
 * the 2nd-largest sample, so on identical code a random configuration is over
 * in any one run (pipeline 15290: round 1 gitlab-on|cpu4x +249, round 2
 * gitlab-off|cpu4x +240, same code, equal p50). A real slowdown is over in the
 * same configurations twice (red control: all four, +512..+782 / +525..+773).
 * Structural errors (missing pair, short run, NaN) of either run stay errors.
 */
export function confirm(first, second, maxDelta) {
  const a = evaluate(first, maxDelta);
  const b = evaluate(second, maxDelta);
  const structural = [...a.errors, ...b.errors].filter((e) => !/p95 delta/.test(e));
  const overA = new Set(a.rows.filter((r) => r.over).map((r) => r.cfg));
  const both = b.rows.filter((r) => r.over && overA.has(r.cfg));
  const errors = [...new Set(structural)];
  for (const r of both) errors.push(`${r.cfg}: p95 delta over ${maxDelta} ms in both runs (last ${r.delta.toFixed(0)} ms)`);
  return { rows: b.rows, errors };
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
  // confirmation: noise lands on different configurations, a regression on the same ones
  const cfgs = ["gitlab-on|cpu4x", "gitlab-off|cpu4x", "gitlab-on|cpu1x", "gitlab-off|cpu1x"];
  const withOver = (over) => {
    const o = full(170);
    for (const c of over) o[`branch|${c}`].p95 = 400;
    return o;
  };
  const ccases = [
    ["confirm: over in different configs passes (noise)", withOver([cfgs[0]]), withOver([cfgs[1]]), 0],
    ["confirm: over in the same config fails", withOver([cfgs[1]]), withOver([cfgs[1]]), 1],
    ["confirm: regression in all configs fails", withOver(cfgs), withOver(cfgs), 1],
    ["confirm: clean second run passes", withOver([cfgs[0]]), full(170), 0],
    ["confirm: short second run fails", withOver([cfgs[0]]), (() => { const o = full(170); o["branch|gitlab-on|cpu4x"].n = 5; return o; })(), 1],
  ];
  for (const [name, x, y, want] of ccases) {
    const got = confirm(x, y, 100).errors.length ? 1 : 0;
    const ok = got === want;
    console.log(`${ok ? "PASS" : "FAIL"}  ${name}`);
    if (!ok) bad++;
  }
  process.exit(bad ? 1 : 0);
}

const argv = process.argv.slice(2);
const opt0 = (name) => (argv.includes(name) ? argv[argv.indexOf(name) + 1] : null);
if (argv.includes("--selftest")) selftest();
else {
  const file = argv.find((a) => !a.startsWith("--") && !["--max-delta-ms", "--report", "--confirm-with"].includes(argv[argv.indexOf(a) - 1]));
  const confirmFile = opt0("--confirm-with");
  const opt = (name, d) => (argv.includes(name) ? argv[argv.indexOf(name) + 1] : d);
  const maxDelta = Number(opt("--max-delta-ms", 100));
  const report = opt("--report", null);
  if (!file || !Number.isFinite(maxDelta)) {
    console.error("usage: check-p95-delta.mjs <p95.json> [--confirm-with second.json] [--max-delta-ms N] [--report out.json]");
    process.exit(2);
  }
  const first = JSON.parse(readFileSync(file, "utf8"));
  const { rows, errors } = confirmFile
    ? confirm(first, JSON.parse(readFileSync(confirmFile, "utf8")), maxDelta)
    : evaluate(first, maxDelta);
  print(rows, maxDelta);
  if (report) writeFileSync(report, JSON.stringify({ max_delta_ms: maxDelta, rows, errors }, null, 1));
  if (errors.length) {
    for (const e of errors) console.error(`perf-task-open: ${e}`);
    process.exit(1);
  }
  console.log("perf-task-open: within threshold");
}
