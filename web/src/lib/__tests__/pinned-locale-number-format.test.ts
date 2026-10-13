import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";

// Regression guard for the class audited in Mesh #acf58d59 / #8816431f and
// extended by the i18n framework (L1.2): a date or number formatted with the
// locale of whoever is LOOKING at it (or a hard-coded "en-US") is
// unpredictable visible text. Since the interface has a language setting, all
// display formatting goes through `@/i18n/format`, which formats for the
// current UI language.
//
// Allow-list phrasing (Mesh #f70347b5): the only code allowed to call the
// locale-sensitive formatting APIs is src/i18n/. Everywhere else:
//
//   flagged   x.toLocaleString(...) / toLocaleDateString(...) / toLocaleTimeString(...)
//   flagged   new Intl.NumberFormat(...) / DateTimeFormat / RelativeTimeFormat / ListFormat
//   allowed   i18nFmt.formatNumber(x), formatDate(x), formatDateTime(x), formatTime(x)
//   allowed   Intl.Segmenter (text segmentation, not display formatting)
//
// WHAT KEEPS THIS FILE FROM FLAGGING ITSELF: the `__tests__` directory
// exclusion in walk(), NOT the comment stripping. If this guard is moved out of
// a `__tests__` directory, move the fixtures behind string concatenation.
//
// KNOWN LIMITS (regex scanner, not AST): x["toLocaleString"](), `.call`
// forms, and a STRING literal that merely contains the text are not handled
// the way an AST pass would.

const SRC_ROOT = path.resolve(__dirname, "../../");

const TO_LOCALE = /\.toLocale(?:Date|Time)?String\(/g;
const INTL_FORMATTER = /\bIntl\.(?:NumberFormat|DateTimeFormat|RelativeTimeFormat|ListFormat|PluralRules)\b/g;
const PATTERNS = [
  { name: "toLocale*String() outside src/i18n", re: TO_LOCALE },
  { name: "Intl formatter outside src/i18n", re: INTL_FORMATTER },
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
      if (full === path.join(SRC_ROOT, "i18n")) continue;
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
      re.lastIndex = 0; // stateful `g` regex - reset before every reuse
      if (re.test(stripped)) {
        offenders.push(`${path.relative(SRC_ROOT, file)} (${name})`);
      }
    }
  }
  return offenders;
}

it("no source file formats a date or number outside the i18n formatters", () => {
  expect(scanTree()).toEqual([]);
});

describe("mutation control: the guard actually detects what it claims to", () => {
  // Writes a real file into the real tree and runs the real scan; cleanup is
  // in `finally` so a failure cannot leave the file behind.
  function withTempSource<T>(basename: string, body: string, run: (rel: string) => T): T {
    const full = path.join(SRC_ROOT, "lib", basename);
    fs.writeFileSync(full, body, "utf8");
    try {
      return run(path.relative(SRC_ROOT, full));
    } finally {
      fs.unlinkSync(full);
    }
  }

  it("reddens on a pinned toLocaleString and names the offending file", () => {
    withTempSource(
      "__locale_guard_red_probe.ts",
      'export const shown = (50000).toLocaleString("en-US");\n',
      (rel) => {
        expect(scanTree()).toContainEqual(`${rel} (toLocale*String() outside src/i18n)`);
      },
    );
  });

  it("reddens on toLocaleDateString with no arguments", () => {
    withTempSource(
      "__locale_guard_red_probe_date.ts",
      "export const shown = new Date().toLocaleDateString();\n",
      (rel) => {
        expect(scanTree()).toContainEqual(`${rel} (toLocale*String() outside src/i18n)`);
      },
    );
  });

  it("reddens on a direct Intl.NumberFormat", () => {
    withTempSource(
      "__locale_guard_red_probe_intl.ts",
      'export const shown = new Intl.NumberFormat("en-US").format(50000);\n',
      (rel) => {
        expect(scanTree()).toContainEqual(`${rel} (Intl formatter outside src/i18n)`);
      },
    );
  });

  it("stays green on the forms it must never flag", () => {
    withTempSource(
      "__locale_guard_green_probe.ts",
      [
        'import * as fmt from "@/i18n/format";',
        "export const n = fmt.formatNumber(50000);",
        "export const d = fmt.formatDate(new Date());",
        "// a remark mentioning value.toLocaleString() must not count",
        "/* nor a block one: new Intl.NumberFormat() */",
      ].join("\n") + "\n",
      (rel) => {
        // Scoped to THIS probe on purpose: see the history of this guard.
        expect(scanTree().filter((o) => o.startsWith(rel))).toEqual([]);
      },
    );
  });

  it("the formatters really live in src/i18n (positive control: the scan skips them on purpose)", () => {
    const src = fs.readFileSync(path.join(SRC_ROOT, "i18n", "format.ts"), "utf8");
    expect(src).toMatch(/Intl\.NumberFormat/);
    expect(src).toMatch(/Intl\.DateTimeFormat/);
  });
});
