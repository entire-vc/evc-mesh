import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";

// Regression guard for the class audited in Mesh #acf58d59 (and its sibling
// #8816431f): a number formatted for display with no locale argument takes the
// grouping separator from whoever is LOOKING at it, not from the UI it lives
// in. Mesh's interface is English-only, so the same code renders "50,000" for
// one reader, "50 000" for another and "50.000" for a third. That is
// unpredictable visible text, not a style preference.
//
// The class survived in four call sites precisely because no test looked for
// it; it only ever surfaced in artifact-preview-dialog, where an unrelated
// test happened to assert on the rendered string. This guard is the thing that
// was missing.
//
// WHAT THIS GUARD SERVES (stated as an allow-list, not an exclusion list —
// Mesh #f70347b5: a guard phrased as "everything except X" silently adopts
// every case added after it):
//
//   flagged   value.toLocaleString()          <- locale never considered
//   flagged   new Intl.NumberFormat()         <- same defect, other spelling
//   allowed   value.toLocaleString("en-US")   <- pinned, the convention here
//   allowed   d.toLocaleString(undefined, {}) <- runtime locale ON PURPOSE, said out loud
//   allowed   d.toLocaleDateString(...)       <- a different method entirely
//
// The trigger is an EMPTY argument list, which is the structural signature of
// "nobody thought about locale". It is deliberately not a check for the word
// "Date" or for particular variable names: the sibling guard in this directory
// shipped with an identifier-matching regex and was blind to `catch (e)` by
// construction (Mesh #e6077830). Matching shape rather than naming survives
// the next person's choice of words.
//
// ESCAPE HATCH, so this never needs a file-exclusion list: if you genuinely
// want the reader's own locale, pass `undefined` explicitly. That is a
// sentence about intent rather than an omission, and project-settings.tsx
// already formats a date that way.
//
// WHAT KEEPS THIS FILE FROM FLAGGING ITSELF: the `__tests__` directory
// exclusion in walk(), NOT the comment stripping. Stripping handles the prose
// above, but the fixture strings and it() descriptions below contain the bare
// pattern as literal text and would match. If this guard is ever moved out of
// a `__tests__` directory, it will flag its own fixtures — fix that by moving
// the fixtures behind string concatenation, not by adding a self-exclusion.
//
// KNOWN LIMITS (measured, accepted — this is a regex scanner, not an AST pass,
// the same tradeoff the sibling guard documents):
//   - missed: x["toLocaleString"](), Number.prototype.toLocaleString.call(n),
//     `const f = n.toLocaleString; f()`, and whitespace around the dot itself.
//     None are shapes anyone writes for ordinary UI number formatting.
//   - false positive: a STRING or template literal that merely mentions the
//     text ".toLocaleString()" (a URL, a doc link) is flagged, since stripping
//     knows about comments but not about string contents. No such text exists
//     in web/src today; if one appears, split the literal rather than widening
//     the guard.
// Caught by design: multiline argument lists, a comment inside the parens, and
// optional chaining (`v?.toLocaleString()`).

const SRC_ROOT = path.resolve(__dirname, "../../");

const BARE_TO_LOCALE_STRING = /\.toLocaleString\(\s*\)/g;
const BARE_INTL_NUMBER_FORMAT = /\bIntl\.NumberFormat\(\s*\)/g;
const PATTERNS = [
  { name: ".toLocaleString() with no locale", re: BARE_TO_LOCALE_STRING },
  { name: "Intl.NumberFormat() with no locale", re: BARE_INTL_NUMBER_FORMAT },
];

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

/** Returns "<relative path> (<which pattern>)" for every offending call site. */
function scanTree(): string[] {
  const files: string[] = [];
  walk(SRC_ROOT, files);
  // Sanity: a walk that silently found nothing would report a clean tree.
  expect(files.length).toBeGreaterThan(50);

  const offenders: string[] = [];
  for (const file of files) {
    const stripped = stripComments(fs.readFileSync(file, "utf8"));
    for (const { name, re } of PATTERNS) {
      re.lastIndex = 0; // stateful `g` regex — reset before every reuse
      if (re.test(stripped)) {
        offenders.push(`${path.relative(SRC_ROOT, file)} (${name})`);
      }
    }
  }
  return offenders;
}

it("no source file formats a number without pinning the locale", () => {
  expect(scanTree()).toEqual([]);
});

describe("mutation control: the guard actually detects what it claims to", () => {
  // The red control writes a real file into the real tree and runs the real
  // scan, rather than testing the regex against a string. A guard can match a
  // pattern perfectly and still report a clean tree if its walk, its comment
  // stripping or its reporting is broken — only driving the whole pipeline
  // rules that out. Cleanup is in `finally` so a failure cannot leave the file
  // behind and redden every later run.
  function withTempSource<T>(basename: string, body: string, run: (rel: string) => T): T {
    const full = path.join(SRC_ROOT, "lib", basename);
    fs.writeFileSync(full, body, "utf8");
    try {
      return run(path.relative(SRC_ROOT, full));
    } finally {
      fs.unlinkSync(full);
    }
  }

  it("reddens on a bare .toLocaleString() and names the offending file", () => {
    withTempSource(
      "__locale_guard_red_probe.ts",
      "export const shown = (50000).toLocaleString();\n",
      (rel) => {
        const offenders = scanTree();
        expect(offenders).toContainEqual(`${rel} (.toLocaleString() with no locale)`);
      },
    );
  });

  it("reddens on a bare Intl.NumberFormat()", () => {
    withTempSource(
      "__locale_guard_red_probe_intl.ts",
      "export const shown = new Intl.NumberFormat().format(50000);\n",
      (rel) => {
        const offenders = scanTree();
        expect(offenders).toContainEqual(`${rel} (Intl.NumberFormat() with no locale)`);
      },
    );
  });

  it("stays green on the forms it must never flag", () => {
    withTempSource(
      "__locale_guard_green_probe.ts",
      [
        'export const pinned = (50000).toLocaleString("en-US");',
        "export const asDate = new Date().toLocaleDateString();",
        "export const asTime = new Date().toLocaleTimeString();",
        'export const deliberate = new Date().toLocaleString(undefined, { dateStyle: "short" });',
        "// a remark mentioning value.toLocaleString() must not count",
        "/* nor a block one: new Intl.NumberFormat() */",
      ].join("\n") + "\n",
      (rel) => {
        // Scoped to THIS probe on purpose. Asserting the whole tree is clean
        // would just restate the main test and would fail for reasons that
        // have nothing to do with the forms under test — as it did on the
        // first red-control run, reporting two failures for one planted
        // offender and pointing at the wrong assertion.
        expect(scanTree().filter((o) => o.startsWith(rel))).toEqual([]);
      },
    );
  });

  it("the tree is only clean because nothing offends, not because the scan is blind", () => {
    // Positive control on the scanner itself: the walk must reach real files
    // and the patterns must be capable of matching. Without this, the green
    // assertion above is satisfied equally well by a scanner that reads nothing.
    const files: string[] = [];
    walk(SRC_ROOT, files);
    const pinnedSites = files.filter((f) =>
      /\.toLocaleString\("en-US"\)/.test(fs.readFileSync(f, "utf8")),
    );
    expect(pinnedSites.length).toBeGreaterThan(0);
  });
});
