#!/usr/bin/env node
/**
 * One copy of React (themes T5, ADR D10: a second copy breaks hooks).
 *
 *   node scripts/assert-single-react.mjs            the built app (web/dist):
 *       exactly one React core and one react-dom defined across all chunks, one
 *       react/react-dom version in pnpm-lock.yaml, theme shims current.
 *   node scripts/assert-single-react.mjs --bundle <views.js> [...]
 *       a theme views bundle: no React core/react-dom inside, and every bare
 *       import is one of the import map's specifiers.
 *   node scripts/assert-single-react.mjs --selftest
 *       feeds known-bad inputs (a bundle that embeds React, a dist with two copies) and requires RED.
 *
 * A React core is recognised by the assignment that defines its internals
 * (`x.__CLIENT_INTERNALS_… = y`); react-dom by `x.__DOM_INTERNALS_… = y`. A
 * mere read (`n.__CLIENT_INTERNALS_…,`) in react-dom or the app does not count.
 */
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.join(import.meta.dirname, "..");
const ALLOWED = new Set(["react", "react-dom", "react/jsx-runtime", "react-i18next", "@entire-vc/mesh-theme-sdk"]);

const CORE = /\.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE\s*=[^=]/g;
const DOM = /\.__DOM_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE\s*=[^=]/g;
const count = (src, re) => (src.match(re) ?? []).length;

export function checkBundle(src) {
  const bad = [];
  if (count(src, CORE) || /Symbol\.for\(["'`]react\.transitional\.element["'`]\)/.test(src)) bad.push("embeds a copy of React");
  if (count(src, DOM)) bad.push("embeds a copy of react-dom");
  const specs = new Set();
  for (const m of src.matchAll(/(?:\bfrom\s*|\bimport\s*\(?\s*)["']([^"']+)["']/g)) specs.add(m[1]);
  for (const s of specs) {
    if (s.startsWith(".") || s.startsWith("/") || /^https?:/.test(s)) continue; // own chunks / assets
    if (!ALLOWED.has(s)) bad.push(`imports "${s}", not in the import map`);
  }
  return bad;
}

export function checkChunks(sources) {
  const bad = [];
  const core = sources.reduce((n, s) => n + count(s, CORE), 0);
  const dom = sources.reduce((n, s) => n + count(s, DOM), 0);
  if (core !== 1) bad.push(`${core} React cores in dist, expected 1`);
  if (dom !== 1) bad.push(`${dom} react-dom copies in dist, expected 1`);
  return bad;
}

function lockVersions() {
  const lock = readFileSync(path.join(WEB, "pnpm-lock.yaml"), "utf8");
  const out = {};
  for (const name of ["react", "react-dom"]) {
    out[name] = [...new Set([...lock.matchAll(new RegExp(`^  ${name}@(\\d[^\\s(:]*)`, "gm"))].map((m) => m[1]))];
  }
  return out;
}

function main() {
  const args = process.argv.slice(2);
  const fails = [];
  if (args[0] === "--selftest") {
    const clean = `import{jsx as a}from"react/jsx-runtime";import x from"./chunk.js";export default{views:{}}`;
    const embedded = `var e={};e.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE=w;export default{}`;
    const foreign = `import l from"lodash";export default{}`;
    const reactCore = `e.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE=w;`;
    const domCore = `r.__DOM_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE={};`;
    const readOnly = `var M=n.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE,N=1;`;
    const cases = [
      ["clean bundle passes", checkBundle(clean).length === 0],
      ["bundle with its own React is RED", checkBundle(embedded).length > 0],
      ["bundle importing an unmapped package is RED", checkBundle(foreign).length > 0],
      ["dist with one core + one react-dom passes", checkChunks([reactCore, domCore, readOnly]).length === 0],
      ["dist with two React cores is RED", checkChunks([reactCore, reactCore, domCore]).length > 0],
      ["dist with no React is RED", checkChunks([readOnly]).length > 0],
    ];
    for (const [what, pass] of cases) {
      console.log(`${pass ? "✅" : "❌"} ${what}`);
      if (!pass) fails.push(what);
    }
  } else if (args[0] === "--bundle") {
    if (args.length < 2) fails.push("no bundle given");
    for (const f of args.slice(1)) for (const b of checkBundle(readFileSync(f, "utf8"))) fails.push(`${f}: ${b}`);
  } else {
    const dir = path.join(WEB, "dist", "assets");
    if (!existsSync(dir)) {
      console.error(`No build at ${dir}; run pnpm build first (a missing build is a failure, not a skip).`);
      process.exit(1);
    }
    const srcs = readdirSync(dir).filter((f) => f.endsWith(".js")).map((f) => readFileSync(path.join(dir, f), "utf8"));
    fails.push(...checkChunks(srcs));
    for (const [name, vs] of Object.entries(lockVersions())) {
      if (vs.length !== 1) fails.push(`pnpm-lock.yaml has ${vs.length} versions of ${name}: ${vs.join(", ") || "none"}`);
    }
    try {
      execFileSync("node", [path.join(import.meta.dirname, "gen-theme-shims.mjs"), "--check"], { stdio: "inherit" });
    } catch {
      fails.push("theme shims or the import map are stale (run: node scripts/gen-theme-shims.mjs)");
    }
    if (!fails.length) console.log(`✅ one React core, one react-dom (${srcs.length} chunks), one version each in the lockfile`);
  }
  if (fails.length) {
    for (const f of fails) console.error(`❌ ${f}`);
    process.exit(1);
  }
}
if (process.argv[1] === fileURLToPath(import.meta.url)) main();
