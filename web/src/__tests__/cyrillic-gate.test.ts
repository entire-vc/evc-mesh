import { describe, expect, it } from "vitest";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

// Drives the real CLI (scripts/check-cyrillic.mjs) against a throwaway source
// tree: red on a planted Russian string, green on a clean tree, and the
// baseline / strict / allow-list rules.
const CLI = path.resolve(__dirname, "../../scripts/check-cyrillic.mjs");

function run(files: Record<string, string>, extra: string[] = [], baseline?: Record<string, number>) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "cyr-gate-"));
  try {
    for (const [rel, body] of Object.entries(files)) {
      const full = path.join(dir, "src", rel);
      fs.mkdirSync(path.dirname(full), { recursive: true });
      fs.writeFileSync(full, body);
    }
    const baselineFile = path.join(dir, "baseline.json");
    fs.writeFileSync(baselineFile, JSON.stringify(baseline ?? {}));
    const r = spawnSync(process.execPath, [CLI, "--src", path.join(dir, "src"), "--baseline", baselineFile, ...extra], {
      encoding: "utf8",
    });
    return { code: r.status, out: (r.stdout ?? "") + (r.stderr ?? "") };
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

const RU = "Добро"; // a Russian word, kept escaped so this file stays clean itself

describe("check-cyrillic gate", () => {
  it("is green on a clean tree", () => {
    expect(run({ "a.tsx": 'export const A = () => <b>Hello</b>;\n' }).code).toBe(0);
  });

  it("is RED on a planted Russian string and names file and line", () => {
    const r = run({ "components/x.tsx": `export const X = () => <b>${RU}</b>;\n` });
    expect(r.code).toBe(1);
    expect(r.out).toContain("components/x.tsx");
  });

  it("ignores comments, tests, locales/ and the transliteration allow-list", () => {
    const r = run({
      "a.ts": `// ${RU}\n/* ${RU} */\nexport const a = 1;\n`,
      "__tests__/a.test.ts": `export const t = "${RU}";\n`,
      "locales/ru/common.ts": `export const t = "${RU}";\n`,
      "lib/utils.ts": `const m = {\n  \u0434: "d", \u043e: "o",\n};\n`,
    });
    expect(r.code).toBe(0);
  });

  it("is RED on Cyrillic after // or /* inside a string literal", () => {
    expect(run({ "a.ts": `export const a = "// ${RU}";\n` }).code).toBe(1);
    expect(run({ "a.ts": `export const a = '/* ${RU} */';\n` }).code).toBe(1);
    expect(run({ "a.ts": "export const a = `x // " + RU + "`;\n" }).code).toBe(1);
  });

  it("is RED on a UI string in lib/utils.ts: only the table rows are exempt", () => {
    const r = run({ "lib/utils.ts": `export const label = "${RU}";\n` });
    expect(r.code).toBe(1);
    expect(r.out).toContain("lib/utils.ts");
  });

  it("baseline tolerates known lines but fails on one more; --strict fails on all", () => {
    const files = { "p.tsx": `export const a = "${RU}";\n` };
    expect(run(files, [], { "p.tsx": 1 }).code).toBe(0);
    expect(run({ "p.tsx": `export const a = "${RU}";\nexport const b = "${RU}";\n` }, [], { "p.tsx": 1 }).code).toBe(1);
    expect(run(files, ["--strict"], { "p.tsx": 1 }).code).toBe(1);
  });

  it("fails loudly when it finds no source files (a blind scan must not look green)", () => {
    expect(run({}).code).toBe(2);
  });
});
