#!/usr/bin/env node
// Perf-budget ratchet, bundle half. Reads the built dist/, computes gzip
// byte counts by wire size (not vite's own build-log estimate — that one
// uses esbuild's summary and isn't meant to be diffed against a fixed
// ceiling), compares against web/perf/budget.json, and exits 1 on any
// metric over its ceiling. See web/perf/README.md for the ratchet rule.
//
// Run after `pnpm build`, from web/: `node perf/check-bundle-budget.mjs`.
//
// It also refuses a dist/ that was not built from the current commit
// (dist/.build-sha, written by the build-sha plugin in vite.config.ts): when
// `pnpm build` fails before vite runs, the previous dist/ stays on disk, and
// reading it passed the budget on code that no longer built. A missing
// .build-sha is a refusal too, never a skipped check.
// `node perf/check-bundle-budget.mjs --selftest` proves that refusal logic.

import {
  readFileSync,
  writeFileSync,
  existsSync,
  mkdtempSync,
  mkdirSync,
  rmSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { gzipSync } from "node:zlib";
import { resolve, join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";

const __dirname = dirname(fileURLToPath(import.meta.url));
const webRoot = resolve(__dirname, "..");
const distDir = resolve(webRoot, "dist");
const manifestPath = join(distDir, ".vite", "manifest.json");
const budgetPath = resolve(__dirname, "budget.json");
const reportPath = resolve(webRoot, "perf-bundle-report.json");

function fail(message) {
  console.error(`[check-bundle-budget] ${message}`);
  process.exit(1);
}

/** The commit the checkout is at: CI's SHA first, local git second — the same
 * order vite.config.ts uses to stamp the build. CI_COMMIT_SHA counts only in
 * this repo's own pipeline: in entire-vc/deploy it is the deploy repo's commit,
 * not the evc-mesh checkout being built. null = cannot tell. */
function currentCommit(env = process.env) {
  if (env.CI_COMMIT_SHA && env.CI_PROJECT_PATH === "entire-vc/evc-mesh") return env.CI_COMMIT_SHA.trim();
  try {
    return execSync("git rev-parse HEAD", { cwd: webRoot, stdio: ["ignore", "pipe", "ignore"] })
      .toString()
      .trim();
  } catch {
    return null;
  }
}

/** Was `distDir` built from `expectedSha`? Anything short of a positive match
 * — no expected commit, no stamp, empty stamp, different commit — is a "no". */
function distFreshness(distDir, expectedSha) {
  if (!expectedSha) {
    return {
      ok: false,
      reason:
        "cannot determine the current commit (no CI_COMMIT_SHA, `git rev-parse HEAD` failed) " +
        "— refusing to check a dist/ whose freshness cannot be verified.",
    };
  }
  const stampPath = join(distDir, ".build-sha");
  if (!existsSync(stampPath)) {
    return {
      ok: false,
      reason:
        `${stampPath} not found — dist/ was not stamped by the current build ` +
        "(built before this check existed, or the build-sha plugin in vite.config.ts is gone). Run `pnpm build`.",
    };
  }
  const stamped = readFileSync(stampPath, "utf8").trim();
  if (!stamped) {
    return { ok: false, reason: `${stampPath} is empty — dist/ has no build commit. Run \`pnpm build\`.` };
  }
  if (stamped !== expectedSha) {
    return {
      ok: false,
      reason:
        `dist/ is not from HEAD: dist/.build-sha = ${stamped}, current commit = ${expectedSha}. ` +
        "It is left over from an older build (or the build failed before vite ran) — run `pnpm build`.",
    };
  }
  return { ok: true, reason: null };
}

if (process.argv.includes("--selftest")) {
  // The refusal is the point of this check, so prove each way it can refuse
  // (and that the one good case still passes) without touching the real dist/.
  const tmp = mkdtempSync(join(tmpdir(), "bundle-fresh-"));
  const SHA = "a".repeat(40);
  const OTHER = "b".repeat(40);
  const dir = (name, stamp) => {
    const d = join(tmp, name);
    mkdirSync(d);
    if (stamp !== undefined) writeFileSync(join(d, ".build-sha"), stamp);
    return d;
  };
  const cases = [
    ["stamp matches HEAD", distFreshness(dir("ok", `${SHA}\n`), SHA), true],
    ["stamp from another commit", distFreshness(dir("stale", `${OTHER}\n`), SHA), false],
    ["stamp file missing", distFreshness(dir("missing", undefined), SHA), false],
    ["stamp file empty", distFreshness(dir("empty", "\n"), SHA), false],
    ["current commit unknown", distFreshness(dir("nohead", `${SHA}\n`), null), false],
  ];
  rmSync(tmp, { recursive: true, force: true });
  let bad = 0;
  for (const [name, got, wantOk] of cases) {
    const pass = got.ok === wantOk;
    if (!pass) bad++;
    console.log(`${pass ? "ok  " : "FAIL"} ${name}: ${got.ok ? "accepted" : "refused"}${got.reason ? ` — ${got.reason.slice(0, 90)}` : ""}`);
  }
  // Which SHA counts as "current": CI's only in this repo's own pipeline.
  const gitHead = currentCommit({});
  const shaCases = [
    ["own pipeline: CI_COMMIT_SHA wins", currentCommit({ CI_COMMIT_SHA: "deadbeef", CI_PROJECT_PATH: "entire-vc/evc-mesh" }), "deadbeef"],
    ["deploy pipeline: CI_COMMIT_SHA ignored", currentCommit({ CI_COMMIT_SHA: "deadbeef", CI_PROJECT_PATH: "entire-vc/deploy" }), gitHead],
    ["no project path: CI_COMMIT_SHA ignored", currentCommit({ CI_COMMIT_SHA: "deadbeef" }), gitHead],
  ];
  for (const [name, got, want] of shaCases) {
    const pass = got === want && (name.startsWith("own") || got !== "deadbeef");
    if (!pass) bad++;
    console.log(`${pass ? "ok  " : "FAIL"} ${name}: ${got}`);
  }
  if (bad) fail(`selftest: ${bad} case(s) behaved wrongly`);
  console.log("[check-bundle-budget] selftest OK");
  process.exit(0);
}

if (!existsSync(distDir)) {
  fail(`dist/ not found at ${distDir} — run \`pnpm build\` first.`);
}
const freshness = distFreshness(distDir, currentCommit());
if (!freshness.ok) fail(freshness.reason);

if (!existsSync(manifestPath)) {
  fail(
    `${manifestPath} not found. vite.config.ts must set build.manifest: true ` +
      `(it does, as of this script's own MR — if this fires, someone removed it).`
  );
}

function gzipKb(absPath) {
  const buf = readFileSync(absPath);
  return gzipSync(buf, { level: 9 }).length / 1024;
}

function round2(n) {
  return Math.round(n * 100) / 100;
}

const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));

/**
 * What /login actually loads before first paint: the index.html entry chunk
 * plus everything it STATICALLY imports, walked recursively. `dynamicImports`
 * is deliberately not walked — those are route-split chunks fetched on
 * demand (React.lazy), not part of the initial load. Today there is no route
 * splitting yet (Б1), so this equals the whole app; once Б1 lands, this walk
 * is what keeps the metric honest without editing the script.
 */
function collectStaticEntryFiles(manifest) {
  const entryKey = Object.keys(manifest).find(
    (k) => manifest[k].isEntry && manifest[k].src === "index.html"
  );
  if (!entryKey) {
    fail(
      `manifest.json has no isEntry chunk for index.html. Keys found: ` +
        `${Object.keys(manifest).join(", ") || "(none)"}`
    );
  }

  const seen = new Set();
  const files = new Set();

  function walk(key) {
    if (seen.has(key)) return;
    seen.add(key);
    const chunk = manifest[key];
    if (!chunk) {
      fail(`manifest.json references chunk "${key}" that has no entry of its own.`);
    }
    if (chunk.file && chunk.file.endsWith(".js")) files.add(chunk.file);
    for (const imp of chunk.imports ?? []) walk(imp);
  }
  walk(entryKey);

  return [...files];
}

/** Every JS chunk vite emitted, entry or not — from the manifest, not a
 * directory listing, so a chunk vite renamed or moved is still found. */
function collectAllJsFiles(manifest) {
  const files = new Set();
  for (const chunk of Object.values(manifest)) {
    if (chunk.file && chunk.file.endsWith(".js")) files.add(chunk.file);
  }
  return [...files];
}

const entryFiles = collectStaticEntryFiles(manifest);
const allJsFiles = collectAllJsFiles(manifest);

if (allJsFiles.length === 0) {
  fail("manifest.json lists zero .js chunks — the build produced no JS, or the manifest is empty.");
}

const loginInitialJsKbGz = round2(
  entryFiles.reduce((sum, f) => sum + gzipKb(join(distDir, f)), 0)
);
const totalJsKbGz = round2(
  allJsFiles.reduce((sum, f) => sum + gzipKb(join(distDir, f)), 0)
);

const actual = {
  "login.initial_js_kb_gz": loginInitialJsKbGz,
  total_js_kb_gz: totalJsKbGz,
};

if (!existsSync(budgetPath)) {
  fail(`${budgetPath} not found — see web/perf/README.md.`);
}
const budget = JSON.parse(readFileSync(budgetPath, "utf8"));

// Keys under these paths belong to the per-action counters and are checked by
// perf/counters.spec.ts (perf-counters job), which also fails on a missing
// one. Anything else this script does not compute is still an error.
const COUNTER_PATHS = ["board.open.", "task.open.", "view.switch.", "comment.send.", "board.drag."];

const failures = [];
for (const [metric, ceiling] of Object.entries(budget)) {
  if (COUNTER_PATHS.some((p) => metric.startsWith(p))) continue;
  const value = actual[metric];
  if (value === undefined) {
    failures.push(`${metric}: budget.json names a metric this script does not compute`);
    continue;
  }
  if (value > ceiling) {
    failures.push(
      `${metric}: ${value} KB gz > ceiling ${ceiling} KB gz (over by ${round2(value - ceiling)} KB)`
    );
  }
}
// The reverse gap is also worth flagging, but not failing on: a metric this
// script computes that budget.json never mentions means it can grow
// unbounded with nobody noticing. Surfaced in the report, not gated.
const uncovered = Object.keys(actual).filter((k) => !(k in budget));

const report = {
  commit: currentCommit(),
  measured_at: new Date().toISOString(),
  entry_files: entryFiles,
  all_js_files: allJsFiles,
  metrics: actual,
  budget,
  uncovered_metrics: uncovered,
  passed: failures.length === 0,
  failures,
};

writeFileSync(reportPath, JSON.stringify(report, null, 2) + "\n");
console.log(JSON.stringify(report, null, 2));

if (failures.length > 0) {
  console.error("\n[check-bundle-budget] FAILED — over budget:");
  for (const f of failures) console.error(`  - ${f}`);
  process.exit(1);
}
console.log("\n[check-bundle-budget] OK — within budget.");
