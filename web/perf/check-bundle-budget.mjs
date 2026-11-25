#!/usr/bin/env node
// Perf-budget ratchet, bundle half. Reads the built dist/, computes gzip
// byte counts by wire size (not vite's own build-log estimate — that one
// uses esbuild's summary and isn't meant to be diffed against a fixed
// ceiling) and gates them two ways. See web/perf/README.md, "The one rule".
//
// 1. Absolute ceiling (every pipeline): web/perf/budget.json. For the two
//    bundle metrics it is a coarse ceiling with >= 5 % headroom over main;
//    only a dedicated card moves it, never a feature MR.
// 2. Growth over main (merge request pipelines): the MR's measurement minus
//    main's measurement at the merge-base, read from main's own
//    `perf-bundle-report.json` artifact. Without a signature the MR may add
//    at most DELTA_LIMITS; more is red unless the MR carries the
//    `perf-budget-raise` label (the lead's signature). No baseline is red,
//    never a skip.
//
// Run after `pnpm build`, from web/: `node perf/check-bundle-budget.mjs`
// (absolute ceiling only), or `... --base-report <main report.json>` for the
// growth check against a report you measured on main yourself.
//
// It also refuses a dist/ that was not built from the current commit
// (dist/.build-sha, written by the build-sha plugin in vite.config.ts): when
// `pnpm build` fails before vite runs, the previous dist/ stays on disk, and
// reading it passed the budget on code that no longer built. A missing
// .build-sha is a refusal too, never a skipped check.
// `node perf/check-bundle-budget.mjs --selftest` proves the refusal logic, the
// growth arithmetic and the baseline search without a build or the network.

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
const deltaReportPath = resolve(webRoot, "perf-bundle-delta.json");

/** Growth an MR may add over main's measurement at its merge-base without a
 * signature, KB gz. Decided on an internal ticket; changing them is a card of its
 * own, like the absolute ceilings. */
const DELTA_LIMITS = {
  "login.initial_js_kb_gz": { name: "Δlogin", max: 0.5 },
  total_js_kb_gz: { name: "Δtotal", max: 2.0 },
  // The landing page after sign-in (w/:ws, the dashboard): the login closure
  // plus the dashboard page's static closure, so +1.0 = Δlogin's +0.5 for the
  // shared entry plus +0.5 for the page's own code.
  // `introducedBy`: a base report written before this metric existed (its
  // `budget` has no such key) may lack it; see newMetricsOf().
  "dashboard.landing_js_kb_gz": { name: "Δlanding", max: 1.0, introducedBy: "bc91cf36" },
};
/** The lazy route chunk of the page w/:ws lands on (App.tsx imports it with React.lazy). */
const DASHBOARD_PAGE_SRC = "src/pages/dashboard.tsx";
/** The lead's signature for growth above DELTA_LIMITS; the reason goes into the MR description. */
const RAISE_LABEL = "perf-budget-raise";
/** How far back along main's first-parent line to look for a measured commit. */
const MAX_BASE_ANCESTORS = 30;
const PERF_JOB_NAME = "perf-bundle";
const REPORT_ARTIFACT = "web/perf-bundle-report.json";

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

function round2(n) {
  return Math.round(n * 100) / 100;
}

/** Labels of the merge request this pipeline runs for (GitLab passes them comma-separated). */
function parseLabels(raw) {
  return (raw ?? "")
    .split(",")
    .map((l) => l.trim().toLowerCase())
    .filter(Boolean);
}

/**
 * Growth metrics a base report may honestly lack: those marked `introducedBy`
 * in DELTA_LIMITS that the report neither measured nor had a ceiling for (its
 * own `budget` object has no such key) — the report was written by a script
 * that did not know the metric yet. Everything else is required: a report
 * whose budget names the metric but has no number for it is broken, not old.
 */
function newMetricsOf(report) {
  const budget = report?.budget;
  if (!budget || typeof budget !== "object") return [];
  return Object.entries(DELTA_LIMITS)
    .filter(([metric, { introducedBy }]) => introducedBy && !(metric in budget) && report.metrics?.[metric] === undefined)
    .map(([metric]) => metric);
}

/** A report counts as a baseline only when it measured `sha` and has every
 * growth metric as a finite number, except those newMetricsOf() excuses. */
function usableReport(report, sha) {
  if (!report || typeof report !== "object") return "not a JSON object";
  if (report.commit !== sha) return `report.commit is ${report.commit ?? "(none)"}, not ${sha}`;
  const excused = newMetricsOf(report);
  for (const metric of Object.keys(DELTA_LIMITS)) {
    if (excused.includes(metric)) continue;
    if (!Number.isFinite(report.metrics?.[metric])) return `report has no numeric ${metric}`;
  }
  return null;
}

/**
 * Who signed a raise. The label alone is not the signature: anyone with
 * label rights can set it. It counts only when the LAST event for the label
 * on this MR is an `add` by one of `signers` (GitLab usernames,
 * .gitlab/perf-budget-signers.txt). `events` = the MR's resource label events,
 * oldest first, or an Error when they could not be read: then nothing is
 * signed (fail-closed).
 */
function labelSignature(labels, events, signers) {
  if (!labels.includes(RAISE_LABEL)) return { signed: false, labeled: false, note: `no \`${RAISE_LABEL}\` label` };
  if (events instanceof Error || !Array.isArray(events)) {
    return { signed: false, labeled: true, note: `cannot verify who set \`${RAISE_LABEL}\`: ${events?.message ?? "no label events"}` };
  }
  if (signers.length === 0) return { signed: false, labeled: true, note: `no signers on main (${SIGNERS_FILE})` };
  const mine = events.filter((e) => e?.label?.name?.toLowerCase() === RAISE_LABEL);
  const last = mine[mine.length - 1];
  if (!last || last.action !== "add") return { signed: false, labeled: true, note: `no add event for \`${RAISE_LABEL}\`` };
  const who = String(last.user?.username ?? "").toLowerCase();
  if (!signers.includes(who)) {
    return { signed: false, labeled: true, note: `\`${RAISE_LABEL}\` set by ${who || "(unknown)"}, who is not a signer (${signers.join(", ")})` };
  }
  return { signed: true, labeled: true, note: `\`${RAISE_LABEL}\` set by ${who} at ${last.created_at ?? "?"}` };
}

/**
 * Growth of `actual` over `base.metrics`. Over a limit is a failure unless
 * `signature.signed` (labelSignature), then it is a signed raise and passes.
 */
function evaluateDelta(actual, base, signature) {
  const signed = signature.signed === true;
  const rows = [];
  const failures = [];
  const introduced = base.newMetrics ?? [];
  for (const [metric, { name, max }] of Object.entries(DELTA_LIMITS)) {
    if (!Number.isFinite(base.metrics[metric])) {
      if (introduced.includes(metric)) {
        // Main did not measure it yet: nothing to subtract. This MR is still
        // held by the metric's absolute ceiling; the next MR has a baseline.
        rows.push({ metric, name, base: null, actual: actual[metric], delta: null, max, over: false, introduced: true });
        continue;
      }
      failures.push(`${name}: main's report ${base.commit.slice(0, 12)} has no numeric ${metric} — cannot measure growth, refusing to pass.`);
      continue;
    }
    const delta = round2(actual[metric] - base.metrics[metric]);
    const over = delta > max;
    rows.push({ metric, name, base: base.metrics[metric], actual: actual[metric], delta, max, over });
    if (over && !signed) {
      failures.push(
        `${name} ${delta >= 0 ? "+" : ""}${delta} KB gz > +${max.toFixed(1)} over main ${base.commit.slice(0, 12)} ` +
          `(${metric}: ${base.metrics[metric]} -> ${actual[metric]}). Shrink the change, or the lead sets ` +
          `the \`${RAISE_LABEL}\` label with the reason in the MR description, then run a new pipeline` +
          (signature.labeled ? ` (label present but not a signature: ${signature.note}).` : ".")
      );
    }
  }
  return { rows, failures, signed, signature: signature.note, overLimit: rows.some((r) => r.over) };
}

/**
 * Commits whose main measurement may stand in for the merge-base, nearest
 * first: the merge-base, then its first-parent ancestors. A commit whose tree
 * equals one of its parents' (a merge that main's `reuse-check` skipped
 * re-testing) also offers that parent: same tree, same bundle.
 * `git(args)` returns stdout; injected by the selftest.
 */
function baselineCandidates(baseSha, git, max = MAX_BASE_ANCESTORS) {
  const out = [];
  const line = git(["rev-list", "--first-parent", `--max-count=${max}`, baseSha]).split("\n").filter(Boolean);
  for (const sha of line) {
    out.push({ sha, via: sha === baseSha ? "merge-base" : `first-parent ancestor of ${baseSha.slice(0, 12)}` });
    const [, ...parents] = git(["rev-list", "--parents", "-n", "1", sha]).trim().split(/\s+/);
    if (parents.length < 2) continue;
    const tree = git(["rev-parse", `${sha}^{tree}`]).trim();
    for (const p of parents.slice(1)) {
      if (git(["rev-parse", `${p}^{tree}`]).trim() === tree) {
        out.push({ sha: p, via: `same tree as ${sha.slice(0, 12)} (merged head, reused on main)` });
      }
    }
  }
  return out;
}

/**
 * First candidate with a usable report. `fetchReports(sha)` resolves to the
 * reports CI kept for that commit (possibly none). A main commit without its
 * own report is usually one whose pipeline `reuse-check` skipped because the
 * merged MR's head had the same tree (main's `perf-bundle` then exits early
 * and keeps no artifact); `mergedHead(sha)` resolves to that head,
 * `{ sha, via }`, or null, and the head's own report stands in. `log` gets one
 * line per miss, so the job log shows which commit was taken and why the
 * nearer ones were not. null = no baseline at all.
 */
async function findBaseline(candidates, fetchReports, log = () => {}, mergedHead = async () => null) {
  const tryReports = async (cand) => {
    let reports;
    try {
      reports = await fetchReports(cand.sha);
    } catch (err) {
      log(`  ${cand.sha.slice(0, 12)} (${cand.via}): ${err.message}`);
      return null;
    }
    for (const r of reports) {
      const why = usableReport(r.report, cand.sha);
      if (!why) return { ...cand, report: r.report, source: r.source };
      log(`  ${cand.sha.slice(0, 12)} (${cand.via}): ${r.source} unusable — ${why}`);
    }
    if (reports.length === 0) log(`  ${cand.sha.slice(0, 12)} (${cand.via}): no ${PERF_JOB_NAME} report`);
    return null;
  };
  for (const cand of candidates) {
    const own = await tryReports(cand);
    if (own) return own;
    let head = null;
    try {
      head = await mergedHead(cand.sha);
    } catch (err) {
      log(`  ${cand.sha.slice(0, 12)}: merged MR head not resolved — ${err.message}`);
    }
    if (!head) continue;
    const viaHead = await tryReports({ sha: head.sha, via: head.via });
    if (viaHead) return viaHead;
  }
  return null;
}

const LABEL_EVENT_PAGES = 20;

/** The MR's resource label events, oldest first, read with
 * PERF_BUDGET_API_TOKEN (a read_api token, if the project sets one) or the job
 * token. Any failure is returned as an Error, which labelSignature treats as
 * unsigned — including a history longer than LABEL_EVENT_PAGES pages: a
 * truncated history could hide the event that is really the last one. */
async function gitlabLabelEvents(env, fetchImpl = fetch) {
  try {
    const base = `${env.CI_API_V4_URL}/projects/${env.CI_PROJECT_ID}/merge_requests/${env.CI_MERGE_REQUEST_IID}`;
    const headers = apiHeaders(env);
    const events = [];
    for (let page = 1; page <= LABEL_EVENT_PAGES; page++) {
      const res = await fetchImpl(`${base}/resource_label_events?per_page=100&page=${page}`, { headers });
      if (!res.ok) return new Error(`label events API: HTTP ${res.status}`);
      const chunk = await res.json();
      if (!Array.isArray(chunk)) return new Error("label events API: response is not a list");
      events.push(...chunk);
      if (chunk.length < 100) {
        events.sort((a, b) => (a.id ?? 0) - (b.id ?? 0));
        return events;
      }
    }
    return new Error(`label events API: more than ${LABEL_EVENT_PAGES * 100} events, history not read to the end`);
  } catch (err) {
    return new Error(`label events API: ${err.message}`);
  }
}

const SIGNERS_FILE = ".gitlab/perf-budget-signers.txt";

/** GitLab usernames whose `perf-budget-raise` counts, one per line, `#` comments. */
function parseSigners(text) {
  return text
    .split("\n")
    .map((l) => l.replace(/#.*/, "").trim().toLowerCase())
    .filter(Boolean);
}

/**
 * Signers as main has them at `ref` (the merge-base), so an MR cannot sign its
 * own raise by adding a name to the file. Missing file or unreadable ref -> [].
 */
function readSigners(git, ref) {
  try {
    return parseSigners(git(["show", `${ref}:${SIGNERS_FILE}`]));
  } catch {
    return [];
  }
}

/** API headers: PERF_BUDGET_API_TOKEN (read_api, set on the project) when
 * present, else the job token. */
function apiHeaders(env) {
  return env.PERF_BUDGET_API_TOKEN ? { "PRIVATE-TOKEN": env.PERF_BUDGET_API_TOKEN } : { "JOB-TOKEN": env.CI_JOB_TOKEN };
}

/**
 * The head of the MR merged into main as `sha` (its merge or squash commit),
 * when that head has exactly `sha`'s tree — the same evidence `reuse-check`
 * uses to skip main's pipeline. null when `sha` is not such a commit.
 */
function gitlabMergedMrHead(env, git) {
  const base = `${env.CI_API_V4_URL}/projects/${env.CI_PROJECT_ID}`;
  return async (sha) => {
    const res = await fetch(`${base}/repository/commits/${sha}/merge_requests`, { headers: apiHeaders(env) });
    if (!res.ok) throw new Error(`API commits/:sha/merge_requests: HTTP ${res.status}`);
    const mrs = (await res.json()).filter(
      (m) =>
        m.state === "merged" &&
        m.target_branch === (env.CI_DEFAULT_BRANCH || "main") &&
        [m.merge_commit_sha, m.squash_commit_sha].includes(sha)
    );
    if (mrs.length !== 1) return null;
    const { iid, sha: headSha } = mrs[0];
    git(["fetch", "--quiet", "--no-tags", "origin", `refs/merge-requests/${iid}/head`]);
    const fetched = git(["rev-parse", "--verify", "FETCH_HEAD"]).trim();
    if (fetched !== headSha) throw new Error(`MR !${iid} head ref is ${fetched.slice(0, 12)}, API says ${headSha.slice(0, 12)}`);
    if (git(["rev-parse", `${headSha}^{tree}`]).trim() !== git(["rev-parse", `${sha}^{tree}`]).trim()) return null;
    return { sha: headSha, via: `MR !${iid} head, same tree as main ${sha.slice(0, 12)} (main reused its pipeline)` };
  };
}

/** Reports kept by the `perf-bundle` jobs of every pipeline on `sha`, newest
 * pipeline first, read with the job token (the same API `reuse-check` reads). */
function gitlabReportFetcher(env) {
  const base = `${env.CI_API_V4_URL}/projects/${env.CI_PROJECT_ID}`;
  const headers = apiHeaders(env);
  const get = async (path, json = true) => {
    const res = await fetch(base + path, { headers });
    if (res.status === 404) return null;
    if (!res.ok) throw new Error(`API ${path.split("?")[0]}: HTTP ${res.status}`);
    return json ? res.json() : res.text();
  };
  return async (sha) => {
    const pipelines = (await get(`/pipelines?sha=${sha}&order_by=id&sort=desc&per_page=20`)) ?? [];
    const reports = [];
    for (const p of pipelines) {
      for (let page = 1; page <= 5; page++) {
        const jobs = (await get(`/pipelines/${p.id}/jobs?per_page=100&page=${page}`)) ?? [];
        for (const job of jobs.filter((j) => j.name === PERF_JOB_NAME)) {
          const text = await get(`/jobs/${job.id}/artifacts/${REPORT_ARTIFACT}`, false);
          if (text === null) continue;
          let report;
          try {
            report = JSON.parse(text);
          } catch {
            report = null;
          }
          reports.push({ report, source: `pipeline ${p.id} job ${job.id}` });
        }
        if (jobs.length < 100) break;
      }
    }
    return reports;
  };
}

/** The growth gate's verdict for one run: no baseline is a failure, never a pass. */
function deltaGate({ actual, baseline, signature, searched }) {
  if (!baseline) {
    return {
      failures: [
        `no baseline: none of ${searched} commit(s) from the merge-base back along main has a usable ` +
          `${PERF_JOB_NAME} report artifact — cannot measure this MR's growth, refusing to pass.`,
      ],
      rows: [],
      signed: false,
      signature: signature?.note ?? null,
    };
  }
  return evaluateDelta(
    actual,
    { commit: baseline.sha, metrics: baseline.report.metrics, newMetrics: newMetricsOf(baseline.report) },
    signature
  );
}

/** Index.html's entry chunk key in the manifest. Throws when there is none. */
function entryKeyOf(manifest) {
  const key = Object.keys(manifest).find((k) => manifest[k].isEntry && manifest[k].src === "index.html");
  if (!key) {
    throw new Error(`manifest.json has no isEntry chunk for index.html. Keys found: ${Object.keys(manifest).join(", ") || "(none)"}`);
  }
  return key;
}

/**
 * JS files of `startKey` plus everything it STATICALLY imports, walked
 * recursively. `dynamicImports` is deliberately not walked: those chunks are
 * fetched on demand (React.lazy, import()), not before the screen paints.
 * Throws on a reference to a chunk the manifest does not have.
 */
function staticClosure(manifest, startKey) {
  const seen = new Set();
  const files = new Set();
  const walk = (key) => {
    if (seen.has(key)) return;
    seen.add(key);
    const chunk = manifest[key];
    if (!chunk) throw new Error(`manifest.json references chunk "${key}" that has no entry of its own.`);
    if (chunk.file && chunk.file.endsWith(".js")) files.add(chunk.file);
    for (const imp of chunk.imports ?? []) walk(imp);
  };
  walk(startKey);
  return files;
}

/**
 * The bundle metrics of a vite manifest; `sizeKb(file)` is a file's gzip size.
 * - login.initial_js_kb_gz: what /login loads before first paint, the entry's
 *   static closure;
 * - dashboard.landing_js_kb_gz: what the page after sign-in (w/:ws) loads
 *   before it paints, the entry's closure plus the dashboard page chunk's
 *   static closure. Edit mode, the widget picker and the catalog sit behind
 *   its dynamic imports and stay out;
 * - total_js_kb_gz: every JS chunk vite emitted (from the manifest, not a
 *   directory listing, so a renamed chunk is still found).
 * Throws when the entry or the dashboard page chunk is missing: a metric that
 * cannot be measured is a refusal, never a skipped one.
 */
function bundleMetrics(manifest, sizeKb) {
  const entryFiles = [...staticClosure(manifest, entryKeyOf(manifest))];
  if (!manifest[DASHBOARD_PAGE_SRC]) {
    throw new Error(
      `manifest.json has no chunk for ${DASHBOARD_PAGE_SRC} (the w/:ws landing page) — cannot measure ` +
        `dashboard.landing_js_kb_gz. Renamed or moved the page? Update DASHBOARD_PAGE_SRC in this script.`
    );
  }
  const landingFiles = [...new Set([...entryFiles, ...staticClosure(manifest, DASHBOARD_PAGE_SRC)])];
  const allJsFiles = [...new Set(Object.values(manifest).map((c) => c.file).filter((f) => f && f.endsWith(".js")))];
  if (allJsFiles.length === 0) {
    throw new Error("manifest.json lists zero .js chunks — the build produced no JS, or the manifest is empty.");
  }
  const sum = (files) => round2(files.reduce((s, f) => s + sizeKb(f), 0));
  return {
    entryFiles,
    landingFiles,
    allJsFiles,
    metrics: {
      "login.initial_js_kb_gz": sum(entryFiles),
      total_js_kb_gz: sum(allJsFiles),
      "dashboard.landing_js_kb_gz": sum(landingFiles),
    },
  };
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
  // Growth over main: within the limits, over them, over them
  // with the lead's label, exactly on a limit, and shrinking.
  const MAIN = "c".repeat(40);
  const base = { commit: MAIN, metrics: { "login.initial_js_kb_gz": 174.29, total_js_kb_gz: 758.75, "dashboard.landing_js_kb_gz": 248.39 } };
  const m = (login, total, landing = 248.39) => ({ "login.initial_js_kb_gz": login, total_js_kb_gz: total, "dashboard.landing_js_kb_gz": landing });
  // The raise label counts only when a listed signer was the last to add it.
  const SIGNERS = ["gary"];
  const ev = (id, action, username, name = RAISE_LABEL) => ({ id, action, user: { username }, label: { name }, created_at: `t${id}` });
  const LBL = parseLabels("frontend, Perf-Budget-Raise");
  const none = labelSignature([], [], SIGNERS);
  const byLead = labelSignature(LBL, [ev(1, "add", "Gary")], SIGNERS);
  const sigCases = [
    ["signer adds the label -> signed", byLead.signed, true],
    ["developer adds the label -> not signed", labelSignature(LBL, [ev(1, "add", "vera")], SIGNERS).signed, false],
    ["lead removed, developer re-added -> not signed", labelSignature(LBL, [ev(1, "add", "gary"), ev(2, "remove", "gary"), ev(3, "add", "vera")], SIGNERS).signed, false],
    ["last event is remove -> not signed", labelSignature(LBL, [ev(1, "add", "gary"), ev(2, "remove", "vera")], SIGNERS).signed, false],
    ["only another label's events -> not signed", labelSignature(LBL, [ev(1, "add", "gary", "frontend")], SIGNERS).signed, false],
    ["label events unreadable -> not signed", labelSignature(LBL, new Error("label events API: HTTP 404"), SIGNERS).signed, false],
    ["no signers file -> not signed", labelSignature(LBL, [ev(1, "add", "gary")], []).signed, false],
    ["label absent -> not signed", none.signed, false],
  ];
  for (const [name, got, want] of sigCases) {
    const pass = got === want;
    if (!pass) bad++;
    console.log(`${pass ? "ok  " : "FAIL"} ${name}`);
  }
  {
    const parsed = parseSigners("# who signs\nGary  # lead\n\n");
    const fromMain = readSigners((a) => (a[1] === `B:${SIGNERS_FILE}` ? "gary\n" : (() => { throw new Error("no file"); })()), "B");
    const missing = readSigners(() => { throw new Error("fatal: path does not exist"); }, "B");
    const pass = parsed.join() === "gary" && fromMain.join() === "gary" && missing.length === 0;
    if (!pass) bad++;
    console.log(`${pass ? "ok  " : "FAIL"} signers read from main's file, missing file -> none`);
  }
  {
    // The reader itself, on a fake API: a short last page ends the history;
    // pages that never end are a truncated history, which signs nothing.
    const env = { CI_API_V4_URL: "x", CI_PROJECT_ID: "1", CI_MERGE_REQUEST_IID: "2", CI_JOB_TOKEN: "t" };
    const page = (n, from) => Array.from({ length: n }, (_, i) => ev(from + i, "add", "vera"));
    const pages = (lens) => async (url) => {
      const p = Number(new URL(url, "http://h/").searchParams.get("page"));
      return { ok: true, json: async () => page(lens[p - 1] ?? 0, (p - 1) * 100) };
    };
    const ended = await gitlabLabelEvents(env, async (url) => {
      const p = Number(new URL(url, "http://h/").searchParams.get("page"));
      return { ok: true, json: async () => (p === 3 ? [ev(1000, "add", "gary")] : page(100, (p - 1) * 100)) };
    });
    const endless = await gitlabLabelEvents(env, pages(Array(LABEL_EVENT_PAGES + 5).fill(100)));
    const denied = await gitlabLabelEvents(env, async () => ({ ok: false, status: 401 }));
    const cases = [
      ["label history read to its short last page", Array.isArray(ended) && ended.length === 201 && labelSignature(LBL, ended, SIGNERS).signed],
      ["label history longer than the page cap -> not signed", endless instanceof Error && !labelSignature(LBL, endless, SIGNERS).signed],
      ["label history HTTP 401 -> not signed", denied instanceof Error && /401/.test(denied.message)],
    ];
    for (const [name, pass] of cases) {
      if (!pass) bad++;
      console.log(`${pass ? "ok  " : "FAIL"} ${name}`);
    }
  }
  const forged = labelSignature(LBL, [ev(1, "add", "vera")], SIGNERS);
  const deltaCases = [
    ["growth within limits", evaluateDelta(m(174.59, 760.25), base, none), true, null],
    ["Δtotal +2.5", evaluateDelta(m(174.29, 761.25), base, none), false, /Δtotal \+2\.5 KB gz > \+2\.0/],
    ["Δtotal +2.5 with perf-budget-raise set by the lead", evaluateDelta(m(174.29, 761.25), base, byLead), true, null],
    ["Δtotal +2.5 with perf-budget-raise set by a developer", evaluateDelta(m(174.29, 761.25), base, forged), false, /Δtotal \+2\.5 .*not a signature: .*vera, who is not a signer/],
    ["Δtotal +2.5, label events unreadable", evaluateDelta(m(174.29, 761.25), base, labelSignature(LBL, new Error("label events API: HTTP 404"), SIGNERS)), false, /cannot verify who set/],
    ["Δlogin +0.51", evaluateDelta(m(174.8, 758.75), base, none), false, /Δlogin \+0\.51 KB gz > \+0\.5/],
    ["exactly +0.5 / +2.0", evaluateDelta(m(174.79, 760.75), base, none), true, null],
    ["bundle shrank", evaluateDelta(m(170, 700), base, none), true, null],
    ["other labels only", evaluateDelta(m(174.29, 761.25), base, labelSignature(parseLabels("perf-budget,raise"), [], SIGNERS)), false, /Δtotal/],
  ];
  for (const [name, got, wantOk, wantText] of deltaCases) {
    const ok = got.failures.length === 0;
    const pass = ok === wantOk && (!wantText || got.failures.some((f) => wantText.test(f)));
    if (!pass) bad++;
    console.log(`${pass ? "ok  " : "FAIL"} ${name}: ${ok ? "OK" : got.failures.join(" | ").slice(0, 120)}`);
  }

  // Baseline search: a fake history and a fake CI, no git and no network.
  //   m0 (merge-base) <- m1 <- m2 on main's first-parent line; m0 is a merge
  //   whose tree equals its second parent h0 (reused on main, no own report).
  const sha = (c) => c.repeat(40);
  const graph = {
    [sha("0")]: { parents: [sha("1"), sha("8")], tree: "T0" },
    [sha("8")]: { parents: [sha("9")], tree: "T0" },
    [sha("1")]: { parents: [sha("2")], tree: "T1" },
    [sha("2")]: { parents: [], tree: "T2" },
  };
  const fakeGit = (args) => {
    if (args[0] === "rev-list" && args[1] === "--first-parent") {
      const line = [];
      for (let c = args[3]; c; c = graph[c]?.parents[0]) line.push(c);
      return line.join("\n");
    }
    if (args[0] === "rev-list" && args[1] === "--parents") return [args[4], ...graph[args[4]].parents].join(" ");
    if (args[0] === "rev-parse") return graph[args[1].replace("^{tree}", "")]?.tree ?? "?";
    throw new Error(`fakeGit: ${args.join(" ")}`);
  };
  const cands = baselineCandidates(sha("0"), fakeGit);
  const candOrder = cands.map((c) => c.sha[0]).join("");
  const report = (c, extra = {}) => ({ report: { commit: sha(c), metrics: base.metrics, ...extra }, source: `job ${c}` });
  const store = (map) => async (s) => map[s] ?? [];
  const silent = () => {};
  const searchCases = [
    ["candidates: merge-base, its same-tree parent, then ancestors", candOrder === "0812", true],
    ["merge-base measured -> merge-base", (await findBaseline(cands, store({ [sha("0")]: [report("0")], [sha("1")]: [report("1")] }), silent))?.sha === sha("0"), true],
    ["merge-base reused -> its merged head", (await findBaseline(cands, store({ [sha("8")]: [report("8")], [sha("1")]: [report("1")] }), silent))?.sha === sha("8"), true],
    ["nearest measured ancestor", (await findBaseline(cands, store({ [sha("2")]: [report("2")] }), silent))?.sha === sha("2"), true],
    ["report of another commit is not a baseline", (await findBaseline(cands, store({ [sha("0")]: [report("1")] }), silent)) === null, true],
    ["report without metrics is not a baseline", (await findBaseline(cands, store({ [sha("0")]: [{ report: { commit: sha("0"), metrics: {} }, source: "x" }] }), silent)) === null, true],
    ["API error skips to the next commit", (await findBaseline(cands, async (s) => { if (s === sha("0")) throw new Error("HTTP 500"); return s === sha("1") ? [report("1")] : []; }, silent))?.sha === sha("1"), true],
    ["no base artifact anywhere -> FAIL", (await findBaseline(cands, store({}), silent)) === null, true],
    // main reused the merged MR's pipeline: no report on main, the MR head's report stands in
    ["reused merge-base -> its merged MR head's report", (await findBaseline(cands, store({ [sha("7")]: [report("7")], [sha("1")]: [report("1")] }), silent, async (s) => (s === sha("0") ? { sha: sha("7"), via: "MR !1 head" } : null)))?.sha === sha("7"), true],
    ["merged MR head report of another commit is not a baseline", (await findBaseline(cands, store({ [sha("7")]: [report("1")], [sha("1")]: [report("1")] }), silent, async (s) => (s === sha("0") ? { sha: sha("7"), via: "MR !1 head" } : null)))?.sha === sha("1"), true],
    ["merged MR lookup error skips to the next commit", (await findBaseline(cands, store({ [sha("1")]: [report("1")] }), silent, async () => { throw new Error("HTTP 403"); }))?.sha === sha("1"), true],
  ];
  for (const [name, got] of searchCases) {
    if (!got) bad++;
    console.log(`${got ? "ok  " : "FAIL"} ${name}`);
  }
  // dashboard.landing_js_kb_gz on a fake manifest: the entry,
  // the dashboard page with a static shared chunk, and the editor behind a
  // dynamic import of the page. Sizes in KB gz.
  {
    const manifestOf = (extra = {}) => ({
      "index.html": { file: "assets/index.js", src: "index.html", isEntry: true, imports: ["_vendor.js"], dynamicImports: [DASHBOARD_PAGE_SRC, "src/pages/board.tsx"] },
      "_vendor.js": { file: "assets/vendor.js" },
      [DASHBOARD_PAGE_SRC]: { file: "assets/dashboard.js", src: DASHBOARD_PAGE_SRC, isDynamicEntry: true, imports: ["index.html", "_vendor.js", "_dash-shared.js"], dynamicImports: ["src/core/dashboard/edit/dashboard-editor.tsx"] },
      "_dash-shared.js": { file: "assets/dash-shared.js", imports: ["_vendor.js"] },
      "src/core/dashboard/edit/dashboard-editor.tsx": { file: "assets/dashboard-editor.js", isDynamicEntry: true, imports: ["_dash-shared.js"] },
      "src/pages/board.tsx": { file: "assets/board.js", isDynamicEntry: true },
      ...extra,
    });
    const sizes = { "assets/index.js": 100, "assets/vendor.js": 70, "assets/dashboard.js": 20, "assets/dash-shared.js": 50, "assets/dashboard-editor.js": 30, "assets/board.js": 40 };
    const measure = (man, grow = {}) => bundleMetrics(man, (f) => sizes[f] + (grow[f] ?? 0)).metrics;
    const L = "dashboard.landing_js_kb_gz";
    const b0 = measure(manifestOf());
    const mainRep = { commit: MAIN, metrics: b0 };
    const staticGrow = measure(manifestOf(), { "assets/dash-shared.js": 1.2 });
    const staticSmall = measure(manifestOf(), { "assets/dashboard.js": 0.8 });
    const editorGrow = measure(manifestOf(), { "assets/dashboard-editor.js": 50 });
    // A new module the page imports statically (what an MR adding `import heavy from ...` to the page produces).
    const newStatic = (() => {
      const man = manifestOf({ "_heavy.js": { file: "assets/heavy.js" } });
      man[DASHBOARD_PAGE_SRC] = { ...man[DASHBOARD_PAGE_SRC], imports: [...man[DASHBOARD_PAGE_SRC].imports, "_heavy.js"] };
      return bundleMetrics(man, (f) => (f === "assets/heavy.js" ? 5 : sizes[f])).metrics;
    })();
    let missingPage = null;
    try {
      const man = manifestOf();
      delete man[DASHBOARD_PAGE_SRC];
      man["index.html"].dynamicImports = ["src/pages/board.tsx"];
      bundleMetrics(man, (f) => sizes[f]);
    } catch (err) {
      missingPage = err.message;
    }
    // A base report from before this metric: its budget has no landing key.
    const oldRep = { commit: MAIN, metrics: { "login.initial_js_kb_gz": 170, total_js_kb_gz: 310 }, budget: { "login.initial_js_kb_gz": 183.7, total_js_kb_gz: 885.7 } };
    const oldGate = deltaGate({ actual: b0, baseline: { sha: MAIN, report: oldRep }, signature: none, searched: 1 });
    const brokenRep = { ...oldRep, budget: { ...oldRep.budget, [L]: 261 } };
    const cases = [
      ["landing = entry closure + page static closure (100+70+20+50)", b0[L] === 240 && b0["login.initial_js_kb_gz"] === 170 && b0.total_js_kb_gz === 310],
      ["page static import grows +1.2 -> Δlanding red", evaluateDelta(staticGrow, mainRep, none).failures.some((f) => /Δlanding \+1\.2 KB gz > \+1\.0/.test(f))],
      ["page static import of a new 5 KB module -> Δlanding red", evaluateDelta(newStatic, mainRep, none).failures.some((f) => /Δlanding \+5 KB gz > \+1\.0/.test(f))],
      ["page own chunk grows +0.8 -> within Δlanding", evaluateDelta(staticSmall, mainRep, none).failures.length === 0],
      ["editor (dynamic import) grows +50 -> landing unchanged, only total moves", editorGrow[L] === b0[L] && editorGrow["login.initial_js_kb_gz"] === b0["login.initial_js_kb_gz"] && editorGrow.total_js_kb_gz === 360],
      ["editor growth -> no Δlanding failure (Δtotal red)", (() => { const f = evaluateDelta(editorGrow, mainRep, none).failures; return !f.some((x) => /Δlanding/.test(x)) && f.some((x) => /Δtotal/.test(x)); })()],
      ["dashboard page chunk missing from manifest -> refusal", missingPage !== null && /no chunk for src\/pages\/dashboard\.tsx/.test(missingPage)],
      ["base report from before the metric is a baseline, Δlanding marked new", usableReport(oldRep, MAIN) === null && oldGate.failures.length === 0 && oldGate.rows.find((r) => r.metric === L)?.introduced === true],
      ["  ... and Δlogin / Δtotal still gated against it", oldGate.rows.filter((r) => !r.introduced).length === 2 && deltaGate({ actual: { ...b0, total_js_kb_gz: 313 }, baseline: { sha: MAIN, report: oldRep }, signature: none, searched: 1 }).failures.some((f) => /Δtotal \+3/.test(f))],
      ["base report whose budget has landing but no number -> not a baseline", /no numeric dashboard\.landing_js_kb_gz/.test(usableReport(brokenRep, MAIN) ?? "")],
      ["base report without a budget object -> landing required", /no numeric dashboard\.landing_js_kb_gz/.test(usableReport({ commit: MAIN, metrics: oldRep.metrics }, MAIN) ?? "")],
      ["login / total never excused, even with a budget lacking them", /no numeric login/.test(usableReport({ commit: MAIN, metrics: {}, budget: {} }, MAIN) ?? "")],
    ];
    for (const [name, pass] of cases) {
      if (!pass) bad++;
      console.log(`${pass ? "ok  " : "FAIL"} ${name}`);
    }
  }

  // The real run turns "no baseline" into a red job, not a skip.
  const noBase = deltaGate({ actual: base.metrics, baseline: null, signature: none, searched: cands.length });
  if (noBase.failures.length === 0 || !/no baseline/.test(noBase.failures[0])) bad++;
  console.log(`${noBase.failures.length ? "ok  " : "FAIL"} no base artifact -> FAIL: ${noBase.failures[0] ?? "passed"}`);

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

const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));

let measured;
try {
  measured = bundleMetrics(manifest, (f) => gzipKb(join(distDir, f)));
} catch (err) {
  fail(err.message);
}
// landingFiles stays out of the report: its top-level keys are a schema other
// tools read (README, "perf-bundle-report.json"); the metric is the new field.
const { entryFiles, allJsFiles, metrics: actual } = measured;

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

// Growth over main. A merge request pipeline always runs it (fail-closed on a
// missing baseline); locally `--base-report <file>` runs it against a report
// measured on main.
let delta = null;
const baseReportArg = process.argv.indexOf("--base-report");
const isMrPipeline = process.env.CI_PIPELINE_SOURCE === "merge_request_event";
if (baseReportArg !== -1) {
  const file = process.argv[baseReportArg + 1];
  if (!file || !existsSync(file)) fail(`--base-report needs an existing report file, got ${file ?? "(nothing)"}`);
  const report = JSON.parse(readFileSync(file, "utf8"));
  const why = usableReport(report, report.commit);
  if (why || !report.commit) fail(`--base-report ${file}: ${why ?? "report has no commit"}`);
  const baseline = { sha: report.commit, via: `--base-report ${file}`, report, source: file };
  // A local run has no MR label events, so a label never signs here.
  const signature = labelSignature(parseLabels(process.env.CI_MERGE_REQUEST_LABELS), new Error("local run, no MR label events"), []);
  delta = { baseline, ...deltaGate({ actual, baseline, signature, searched: 1 }) };
} else if (isMrPipeline) {
  const baseSha = process.env.CI_MERGE_REQUEST_DIFF_BASE_SHA;
  if (!baseSha) fail("merge request pipeline without CI_MERGE_REQUEST_DIFF_BASE_SHA — cannot find main's measurement");
  const git = (args) =>
    execSync(`git ${args.map((a) => `'${a}'`).join(" ")}`, { cwd: webRoot, stdio: ["ignore", "pipe", "pipe"] }).toString();
  let candidates;
  try {
    candidates = baselineCandidates(baseSha, git);
  } catch (err) {
    fail(`cannot walk main's history from the merge-base ${baseSha}: ${err.message.split("\n")[0]}`);
  }
  console.log(`[check-bundle-budget] looking for main's measurement from the merge-base ${baseSha}:`);
  const baseline = await findBaseline(candidates, gitlabReportFetcher(process.env), (l) => console.log(l), gitlabMergedMrHead(process.env, git));
  // Who set the raise label is read on every MR pipeline, label or not, so a
  // broken API path shows in the log before anyone needs a signed raise.
  const events = await gitlabLabelEvents(process.env);
  console.log(
    `[check-bundle-budget] MR label events: ${events instanceof Error ? `UNREADABLE (${events.message}) — a raise label cannot count` : `${events.length} read`}`
  );
  const signers = readSigners(git, baseSha);
  console.log(`[check-bundle-budget] raise signers on main ${baseSha.slice(0, 12)}: ${signers.length ? signers.join(", ") : `none (${SIGNERS_FILE} missing there)`}`);
  const signature = labelSignature(parseLabels(process.env.CI_MERGE_REQUEST_LABELS), events, signers);
  console.log(`[check-bundle-budget] raise signature: ${signature.signed ? "SIGNED" : "none"} (${signature.note})`);
  delta = { baseline, ...deltaGate({ actual, baseline, signature, searched: candidates.length }) };
}

if (delta) {
  if (delta.baseline) {
    const b = delta.baseline;
    console.log(`[check-bundle-budget] baseline: ${b.sha} (${b.via}; ${b.source})`);
    if (b.via !== "merge-base" && isMrPipeline) {
      console.log(`[check-bundle-budget] NOTE: the merge-base itself has no report; ${b.sha} is taken instead.`);
    }
    for (const r of delta.rows) {
      if (r.introduced) {
        console.log(
          `  ${r.name.padEnd(7)} ${r.metric}: base (not measured by ${b.sha.slice(0, 12)}, its budget has no such key) -> ${r.actual}` +
            `  NEW metric: no growth to gate in this MR, its ceiling in budget.json holds it`
        );
        continue;
      }
      const sign = r.delta >= 0 ? "+" : "";
      console.log(
        `  ${r.name.padEnd(7)} ${r.metric}: base ${r.base} -> ${r.actual}  ${sign}${r.delta} KB gz (limit +${r.max.toFixed(1)})` +
          `${r.over ? (delta.signed ? `  OVER, signed: ${delta.signature}` : "  OVER") : ""}`
      );
    }
  }
}

// perf-bundle-report.json is the measurement against the absolute ceilings,
// in the schema it always had: later MRs read its `metrics` as their baseline,
// and review tooling recomputes `passed` from `metrics` vs `budget`. The
// growth verdict goes to a file of its own.
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

if (delta) {
  const deltaReport = {
    commit: report.commit,
    base_commit: delta.baseline?.sha ?? null,
    base_via: delta.baseline?.via ?? null,
    base_source: delta.baseline?.source ?? null,
    base_metrics: delta.baseline?.report.metrics ?? null,
    metrics: actual,
    rows: delta.rows,
    limits: Object.fromEntries(Object.entries(DELTA_LIMITS).map(([k, v]) => [k, v.max])),
    raise_label: RAISE_LABEL,
    signed: delta.signed,
    signature: delta.signature,
    passed: delta.failures.length === 0,
    failures: delta.failures,
  };
  writeFileSync(deltaReportPath, JSON.stringify(deltaReport, null, 2) + "\n");
}

const allFailures = [...failures, ...(delta?.failures ?? [])];
if (allFailures.length > 0) {
  console.error("\n[check-bundle-budget] FAILED:");
  for (const f of allFailures) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(`\n[check-bundle-budget] OK — within the ceilings${delta ? " and the growth limits" : ""}.`);
