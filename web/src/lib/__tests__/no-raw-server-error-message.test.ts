import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";

// Regression guard for the class audited in Mesh #363ef885: a server-supplied
// `.message` (built in api.ts from whatever the backend put in the response
// body) rendered straight to the user bypasses §1r.A copy approval, and the
// caller's own approved fallback string sits there unreachable. The fix
// funnels every "show the user why the request failed" call through
// `apiErrorMessage()`, which suppresses a bare API-error message in favor of
// the fallback (see api-error.ts). This test fails if the raw ternary comes
// back in a new call site instead of reusing the shared helper.
//
// Source is stripped of comments before matching so this test doesn't match
// its own prose above (the self-referential-grep trap: a naive scan would
// find this comment's mention of the pattern and "pass" for the wrong
// reason).
const SRC_ROOT = path.resolve(__dirname, "../../");
const EXCLUDED_FILES = new Set([path.resolve(__dirname, "../api-error.ts")]);

const RAW_PATTERN =
  /(err|error)\s+instanceof\s+Error\s*\n?\s*\?\s*(err|error)\.message\s*\n?\s*:/g;

function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|[^:])\/\/.*$/gm, "$1");
}

function walk(dir: string, out: string[]): void {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules" || entry.name === "__tests__") continue;
      walk(full, out);
    } else if (/\.(ts|tsx)$/.test(entry.name)) {
      out.push(full);
    }
  }
}

it("no source file re-introduces the raw 'instanceof Error ? .message' leak", () => {
  const files: string[] = [];
  walk(SRC_ROOT, files);
  expect(files.length).toBeGreaterThan(50); // sanity: the walk actually found the tree

  const offenders: string[] = [];
  for (const file of files) {
    if (EXCLUDED_FILES.has(path.resolve(file))) continue;
    const stripped = stripComments(fs.readFileSync(file, "utf8"));
    RAW_PATTERN.lastIndex = 0; // stateful `g` regex — reset before every reuse
    if (RAW_PATTERN.test(stripped)) offenders.push(path.relative(SRC_ROOT, file));
  }

  expect(offenders).toEqual([]);
});

describe("mutation control: the guard above actually detects the pattern", () => {
  it("flags a synthetic reintroduction of the leak", () => {
    const synthetic = 'toast.error(err instanceof Error ? err.message : "Failed to save");';
    RAW_PATTERN.lastIndex = 0;
    expect(RAW_PATTERN.test(stripComments(synthetic))).toBe(true);
  });
});
