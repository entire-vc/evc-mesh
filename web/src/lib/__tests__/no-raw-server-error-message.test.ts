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
//
// The pattern matches ANY identifier via a capture group + backreference
// (Mesh #e6077830): a first version hardcoded `(err|error)` on both sides of
// the ternary and missed the idiomatic `catch (e)` spelling entirely — the
// guard was blind to its own defect class by construction, not by accident.
// `\1` forces the left- and right-hand identifiers to be the SAME name,
// which is also what keeps this from flagging two unrelated identifiers
// that happen to sit on either side of an unrelated ternary.
const SRC_ROOT = path.resolve(__dirname, "../../");
const EXCLUDED_FILES = new Set([path.resolve(__dirname, "../api-error.ts")]);

const RAW_PATTERN =
  /([A-Za-z_$][\w$]*)\s+instanceof\s+Error\s*\n?\s*\?\s*\1\.message\s*\n?\s*:/g;

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
  it("flags a synthetic reintroduction of the leak, spelled with any variable name", () => {
    for (const name of ["err", "error", "e", "ex", "caughtError"]) {
      const synthetic = `toast.error(${name} instanceof Error ? ${name}.message : "Failed to save");`;
      RAW_PATTERN.lastIndex = 0;
      expect(
        RAW_PATTERN.test(stripComments(synthetic)),
        `expected a flag for variable name "${name}"`,
      ).toBe(true);
    }
  });

  it("flags the idiomatic `catch (e)` spelling that the old name-hardcoded regex missed (Mesh #e6077830)", () => {
    const synthetic = [
      "try {",
      "  await save();",
      "} catch (e) {",
      '  toast.error(e instanceof Error ? e.message : "Failed to save");',
      "}",
    ].join("\n");
    RAW_PATTERN.lastIndex = 0;
    expect(RAW_PATTERN.test(stripComments(synthetic))).toBe(true);
  });

  it("does not flag the pattern when it appears only in a comment", () => {
    const synthetic = [
      "// old code used to do: e instanceof Error ? e.message : fallback",
      "/* multi-line remark: err instanceof Error ? err.message : fallback */",
    ].join("\n");
    RAW_PATTERN.lastIndex = 0;
    expect(RAW_PATTERN.test(stripComments(synthetic))).toBe(false);
  });

  it("does not flag two different identifiers either side of the ternary", () => {
    const synthetic =
      'toast.error(err instanceof Error ? fallbackMessage.message : "Failed to save");';
    RAW_PATTERN.lastIndex = 0;
    expect(RAW_PATTERN.test(stripComments(synthetic))).toBe(false);
  });
});
