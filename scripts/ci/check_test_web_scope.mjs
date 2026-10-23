#!/usr/bin/env node
// Keeps the `rules:changes` list of the `test:web` job honest.
//
// On a merge request `test:web` is created only when the diff touches one of the
// paths between the `# BEGIN test-web-scope` / `# END test-web-scope` markers in
// .gitlab-ci.yml. Everything under web/ is in that list; the risk is a file
// OUTSIDE web/ that the frontend unit suite reads (a JSON contract fixture, a
// schema), because a merge request touching only that file would get no
// `test:web` job and a green pipeline without the tests that read it.
//
// This script scans the inputs of the suite (web/src, web/scripts and the config
// files at the web/ root) for relative path literals ("../../docs/...") that
// resolve outside web/, and fails when one of them is not matched by a glob in
// the list. It runs as the first step of `test:web`: the commit that adds such a
// reference changes web/, so it always starts the job that catches it.
//
// Usage, from the repository root or from web/:
//   node scripts/ci/check_test_web_scope.mjs            check the real tree
//   node scripts/ci/check_test_web_scope.mjs --selftest prove the matcher and the
//                                                        red control

import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const SELF = "scripts/ci/check_test_web_scope.mjs";
const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const webRoot = join(repoRoot, "web");

// Directories under web/ that hold the unit suite's inputs, and the root-level
// config extensions that vitest/vite read. e2e/, board-e2e/ and perf/ belong to
// the Playwright jobs, not to `test:web` (vitest.config.ts excludes them).
const SCAN_DIRS = ["src", "scripts"];
const SCAN_EXT = /\.(?:[cm]?[jt]sx?|json)$/;
const SKIP_DIRS = new Set(["node_modules", "dist", "dist-perf", "coverage"]);
// Paths every version of the list must keep: the suite itself, the job
// definition, and this checker.
const ALWAYS = ["web/**/*", ".gitlab-ci.yml", SELF];

function fail(message) {
  console.error(`[check_test_web_scope] ${message}`);
  process.exit(1);
}

/** GitLab `rules:changes` glob -> RegExp (`**` crosses directories, `*` does not). */
export function globToRegExp(glob) {
  let out = "";
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i];
    if (c === "*" && glob[i + 1] === "*") {
      if (glob[i + 2] === "/") {
        out += "(?:.*/)?";
        i += 2;
      } else {
        out += ".*";
        i += 1;
      }
    } else if (c === "*") {
      out += "[^/]*";
    } else if (c === "?") {
      out += "[^/]";
    } else {
      out += c.replace(/[.+^${}()|[\]\\]/g, "\\$&");
    }
  }
  return new RegExp(`^${out}$`);
}

/** The globs between the markers, which must sit inside the `test:web` job. */
export function scopeGlobs(ciText) {
  const job = ciText.match(/\ntest:web:\n([\s\S]*?)(?=\n[^\s#][^\n]*:\n)/);
  if (!job) fail("job `test:web` not found in .gitlab-ci.yml");
  const block = job[1].match(/# BEGIN test-web-scope\n([\s\S]*?)\n\s*# END test-web-scope/);
  if (!block) fail("`# BEGIN test-web-scope` / `# END test-web-scope` block not found inside `test:web`");
  const globs = [];
  for (const line of block[1].split("\n")) {
    const t = line.trim();
    if (!t || t.startsWith("#")) continue;
    const m = t.match(/^-\s+"([^"]+)"$/);
    if (!m) fail(`cannot parse scope line: ${JSON.stringify(line)} (expected: - "glob")`);
    globs.push(m[1]);
  }
  return globs;
}

function* walk(dir) {
  let names;
  try {
    names = readdirSync(dir);
  } catch {
    return;
  }
  for (const name of names) {
    if (SKIP_DIRS.has(name)) continue;
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) yield* walk(p);
    else if (SCAN_EXT.test(name)) yield p;
  }
}

/** Repo-relative paths outside web/ that the literals of `text` (a file in `fileDir`) point at. */
export function escapingRefs(text, fileDir, root = repoRoot, web = webRoot) {
  const refs = [];
  // Any quoted relative path with a ".." segment, wherever it sits: "../x", "./../x",
  // "fixtures/../../../x". Absolute paths and URLs are not repository-relative.
  for (const m of text.matchAll(/(["'`])([^"'`\s]*)\1/g)) {
    const literal = m[2];
    if (literal.includes("${") || literal.startsWith("/") || /^[a-z][a-z0-9+.-]*:/i.test(literal)) continue;
    if (!literal.split("/").includes("..")) continue;
    const target = resolve(fileDir, literal);
    const fromWeb = relative(web, target);
    if (fromWeb === "" || !(fromWeb === ".." || fromWeb.startsWith(`..${sep}`))) continue;
    const fromRoot = relative(root, target);
    if (fromRoot.startsWith("..")) continue; // outside the repository: not a path in a diff
    refs.push(fromRoot.split(sep).join("/"));
  }
  return refs;
}

const BASES = new Set(["__dirname", "import.meta.dirname"]);
const UNRESOLVED = "<unresolved parent path>";

/** Top-level comma-separated arguments of the call whose "(" is at `open`, or null. */
function callArgs(text, open) {
  const args = [];
  let depth = 0, quote = null, cur = "";
  for (let i = open + 1; i < text.length; i++) {
    const c = text[i];
    if (quote) {
      cur += c;
      if (c === "\\") { cur += text[++i] ?? ""; continue; }
      if (c === quote) quote = null;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") { quote = c; cur += c; continue; }
    if (c === "(" || c === "[" || c === "{") depth++;
    if (c === ")" || c === "]" || c === "}") {
      if (depth === 0) { if (cur.trim()) args.push(cur.trim()); return args; }
      depth--;
    }
    if (c === "," && depth === 0) { args.push(cur.trim()); cur = ""; continue; }
    cur += c;
  }
  return null;
}

/**
 * Paths outside web/ assembled by join()/resolve() from segments, e.g.
 * resolve(__dirname, "..", "..", "docs", "x.json"). A call that has a ".." segment but
 * an argument this check cannot evaluate fails closed as UNRESOLVED, unless its line
 * carries a `scope-ok: <reason>` comment.
 */
export function segmentRefs(text, fileDir, root = repoRoot, web = webRoot) {
  const refs = [];
  for (const m of text.matchAll(/\b(?:path\.)?(?:join|resolve)\s*\(/g)) {
    const args = callArgs(text, m.index + m[0].length - 1);
    if (!args || !args.length) continue;
    const lineEnd = text.indexOf("\n", m.index + m[0].length);
    const lineStart = text.lastIndexOf("\n", m.index) + 1;
    const line = text.slice(lineStart, lineEnd === -1 ? undefined : lineEnd);
    const parts = args.map((a) => {
      if (BASES.has(a)) return { base: true };
      const lit = /^(["'`])([^"'`]*)\1$/.exec(a);
      if (lit && !lit[2].includes("${")) return { seg: lit[2] };
      return { unknown: true };
    });
    const climbs = parts.some((x) => x.seg !== undefined && x.seg.split("/").includes(".."));
    if (!climbs) continue;
    if (!parts[0].base || parts.slice(1).some((x) => x.unknown || x.base)) {
      if (!/scope-ok:\s*\S/.test(line)) refs.push(UNRESOLVED);
      continue;
    }
    const target = resolve(fileDir, ...parts.slice(1).map((x) => x.seg));
    const fromWeb = relative(web, target);
    if (fromWeb === "" || !(fromWeb === ".." || fromWeb.startsWith(`..${sep}`))) continue;
    const fromRoot = relative(root, target);
    if (fromRoot.startsWith("..")) continue;
    refs.push(fromRoot.split(sep).join("/"));
  }
  return refs;
}

function covered(path, regexps) {
  return regexps.some((re) => re.test(path) || re.test(`${path}/x`));
}

function selftest() {
  let bad = 0;
  const check = (name, got, want) => {
    const ok = got === want;
    if (!ok) bad++;
    console.log(`${ok ? "ok  " : "FAIL"} ${name}: ${got}`);
  };
  const re = (g) => globToRegExp(g);
  check("web/**/* matches a nested file", re("web/**/*").test("web/src/a/b.ts"), true);
  check("web/**/* matches a top-level file", re("web/**/*").test("web/package.json"), true);
  check("web/**/* does not match webby/x", re("web/**/*").test("webby/x"), false);
  check("docs/api/**/* matches the example", re("docs/api/**/*").test("docs/api/agent-runtime.example.json"), true);
  check("* does not cross a directory", re("docs/*.json").test("docs/api/x.json"), false);
  check("a literal dot is not a wildcard", re(".gitlab-ci.yml").test("xgitlab-ci.yml"), false);

  const fakeRoot = "/repo";
  const fakeWeb = "/repo/web";
  const dir = "/repo/web/src/lib/__tests__";
  const src = [
    `readFileSync(resolve(__dirname, "../../../../docs/api/agent-runtime.example.json"))`,
    `import schema from "../../../../internal/extensions/schema/manifest.v1.json";`,
    `const red = "../../../../migrations/20260101001_x.sql";`,
    `const inside = path.resolve(__dirname, "../../..", f);`,
    "const tpl = `../../../../${name}`;",
    `const dot = "./../../../../docs/api/dot.json";`,
    `const mid = "fixtures/../../../../../docs/api/mid.json";`,
    `const url = "https://example.invalid/../x";`,
  ].join("\n");
  const refs = escapingRefs(src, dir, fakeRoot, fakeWeb);
  check("finds the five escaping literals", refs.length, 5);
  check("finds a ./-prefixed climb", refs.includes("docs/api/dot.json"), true);
  check("finds a climb in the middle", refs.includes("docs/api/mid.json"), true);
  check("does not report a path inside web/", refs.some((r) => r.startsWith("web/")), false);
  const scope = ["web/**/*", "docs/api/**/*", "internal/extensions/schema/**/*"].map(re);
  const uncovered = refs.filter((r) => !covered(r, scope));
  // Red control: a reference the list does not cover must be reported.
  check("red control: uncovered migration reference is reported", uncovered.join(","), "migrations/20260101001_x.sql");
  const seg = [
    `readFileSync(resolve(__dirname, "..", "..", "..", "..", "docs", "api", "agent-runtime.example.json"))`,
    `const migr = join(\n  __dirname,\n  "../../../..",\n  "migrations",\n  "x.sql",\n);`,
    `const inside = join(__dirname, "..", "..");`,
    `mkdirSync(join(tmp, ".."), { recursive: true });`,
    `mkdirSync(join(tmp, "..")); // scope-ok: temp directory, not the repository`,
  ].join("\n");
  const segRefs = segmentRefs(seg, dir, fakeRoot, fakeWeb);
  check("segments: resolves the assembled docs path", segRefs.includes("docs/api/agent-runtime.example.json"), true);
  check("segments: resolves a multi-line join", segRefs.includes("migrations/x.sql"), true);
  check("segments: a path inside web/ is not reported", segRefs.some((r) => r.startsWith("web/")), false);
  check("segments: an unknown base climbing up fails closed once", segRefs.filter((r) => r === UNRESOLVED).length, 1);

  if (bad) fail(`selftest: ${bad} case(s) behaved wrongly`);
  console.log("[check_test_web_scope] selftest OK");
}

function main() {
  const ciText = readFileSync(join(repoRoot, ".gitlab-ci.yml"), "utf8");
  const globs = scopeGlobs(ciText);
  const missing = ALWAYS.filter((g) => !globs.includes(g));
  if (missing.length) fail(`the test-web-scope list must keep: ${missing.join(", ")}`);
  const regexps = globs.map(globToRegExp);

  const files = [
    ...SCAN_DIRS.flatMap((d) => [...walk(join(webRoot, d))]),
    ...readdirSync(webRoot)
      .filter((n) => SCAN_EXT.test(n))
      .map((n) => join(webRoot, n)),
  ];
  if (files.length < 50) fail(`scanned only ${files.length} files under web/ — the walk is broken, refusing to pass`);

  const found = new Map();
  for (const f of files) {
    const text = readFileSync(f, "utf8");
    for (const ref of [...escapingRefs(text, dirname(f)), ...segmentRefs(text, dirname(f))]) {
      const users = found.get(ref) ?? [];
      users.push(relative(repoRoot, f).split(sep).join("/"));
      found.set(ref, users);
    }
  }
  // Positive control: the suite reads at least one file outside web/ today
  // (docs/api/agent-runtime.example.json). Zero means the scan went blind.
  if (found.size === 0) fail("found no reference outside web/ at all — the scan is blind, refusing to pass");

  if (found.has(UNRESOLVED)) {
    fail(`join()/resolve() climbs out of its directory from a base this check cannot evaluate in: ${found.get(UNRESOLVED).join(", ")}. ` +
      "Use __dirname or import.meta.dirname as the base, or mark the line `// scope-ok: <reason>` when it does not read the repository.");
  }
  const uncovered = [...found.keys()].filter((ref) => !covered(ref, regexps));
  console.log(`[check_test_web_scope] ${files.length} files scanned, ${found.size} path(s) outside web/ referenced:`);
  for (const [ref, users] of found) {
    console.log(`  ${covered(ref, regexps) ? "covered  " : "UNCOVERED"} ${ref}  (${users.length} file(s), e.g. ${users[0]})`);
  }
  if (uncovered.length) {
    fail(
      `${uncovered.length} path(s) read by the web unit suite are not in the test:web rules:changes list: ` +
        `${uncovered.join(", ")}. Add them between the test-web-scope markers in .gitlab-ci.yml, ` +
        "otherwise a merge request touching only them gets no test:web job.",
    );
  }
  console.log("[check_test_web_scope] OK — every path outside web/ the suite reads starts test:web");
}

if (process.argv.includes("--selftest")) selftest();
else main();
