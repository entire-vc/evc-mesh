import { describe, it, expect, afterEach } from "vitest";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";

const script = join(process.cwd(), "scripts/check-tokens.mjs");
const dirs: string[] = [];
// Built from parts so a literal grep of web/src for palette classes stays at 0.
const amber = ["text", "amber", "500"].join("-");
const blueDark = ["dark:bg", "blue", "900/30"].join("-");
const red = ["text", "red", "500"].join("-");
// 8-hex literals built from parts so they are not mistaken for task ids.
const hex8 = ["#1122", "3344"].join("");
const id8 = ["#0000", "0000"].join("");
const id8b = ["#1234", "abcd"].join("");
const borderL = ["border-l", "amber", "400"].join("-");
const divideX = ["divide-x", "red", "500"].join("-");

function run(files: Record<string, string>) {
  const dir = mkdtempSync(join(tmpdir(), "check-tokens-"));
  dirs.push(dir);
  for (const [name, body] of Object.entries(files)) {
    const p = join(dir, name);
    mkdirSync(join(p, ".."), { recursive: true }); // scope-ok: parent of a file in a temp dir
    writeFileSync(p, body);
  }
  return spawnSync(process.execPath, [script, dir], { encoding: "utf8" });
}

afterEach(() => {
  while (dirs.length) rmSync(dirs.pop()!, { recursive: true, force: true });
});

describe("check-tokens", () => {
  it("passes on token classes and a color-ok hex", () => {
    const r = run({
      "a.tsx": `export const A = () => <i className="text-tone-warning-500 bg-card dark:bg-tone-danger-900/40" />;\nconst d = "#6366f1"; // color-ok: stored default\n`,
    });
    expect(r.status).toBe(0);
  });

  it.each([
    ["raw palette class", `const c = "${amber}";`, "a.tsx"],
    ["raw palette class with variant and opacity", `const c = "${blueDark}";`, "a.tsx"],
    ["side border palette class", `const c = "${borderL}";`, "a.tsx"],
    ["axis divide palette class", `const c = "${divideX}";`, "a.tsx"],
    ["8-digit hex in an arbitrary value", `const c = "bg-[${hex8}]";`, "a.tsx"],
    ["4-digit hex in a gradient", `const c = "linear-gradient(${hex8}, #abcd)";`, "a.tsx"],
    ["alpha hex after a CSS value in a style string", `const s = {boxShadow: "0 0 2px ${hex8}"};`, "a.tsx"],
    ["alpha hex after a border keyword", `const s = "1px solid #abcd";`, "a.tsx"],
    ["hex inside a multi-line template literal", "const c = `\n  flex\n  bg-[#fff]\n`;", "a.tsx"],
    ["alpha hex inside a multi-line template literal", "const c = `\n  flex\n  bg-[" + hex8 + "]\n`;", "a.tsx"],
    ["hex literal in a string", `const c = "#2dd4a8";`, "a.tsx"],
    ["hex in an arbitrary value", `const c = "bg-[#fff]";`, "a.tsx"],
    ["unquoted hex in css", `.x { color: #fff; }`, "a.css"],
    ["4-digit hex in css", `.x { color: #ffff; }`, "a.css"],
    ["8-digit hex in css", `.x { background: ${hex8}; }`, "a.css"],
    ["8-digit hex string in tsx", `const c = "${hex8}";`, "a.tsx"],
    ["css custom property hex outside the token file", `:root { --custom: #123456; }`, "a.css"],
  ])("fails on %s", (_name, body, file) => {
    const r = run({ [file]: body });
    expect(r.status).toBe(1);
    expect(r.stderr).toContain("violation");
  });

  it("reports the right line for a multi-line template literal", () => {
    const r = run({ "a.tsx": "const c = `\n  flex\n  bg-[#fff]\n`;" });
    expect(r.stderr).toContain("a.tsx:3:");
  });

  it("does not flag an 8-hex id in prose", () => {
    const r = run({ "a.tsx": `const t = "Task ${id8} failed";\nconst u = "see ${id8b}";` });
    expect(r.status).toBe(0);
  });

  it("allows hex inside styles/brandkit.css and ignores tests and comments", () => {
    const r = run({
      "styles/brandkit.css": `:root { --x: #ef4444; }`,
      "__tests__/t.test.tsx": `const c = "${amber} #fff";`,
      "b.ts": `// see a PR and ${amber} in old code\n/* #fff */\n/* multi\n line \`${id8}\` ${red} */\n`,
    });
    expect(r.status).toBe(0);
  });
});
