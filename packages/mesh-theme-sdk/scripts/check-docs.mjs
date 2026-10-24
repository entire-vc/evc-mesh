#!/usr/bin/env node
// Every ts/tsx block of docs/themes/*.md is compiled against the SDK, unless its info string says `fragment`
// (a piece that is not a whole module). A block that does not compile fails here, so the guide cannot drift
// from the types. Control: a deliberately wrong block must fail the same compile.
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import assert from "node:assert/strict";

const pkg = path.join(import.meta.dirname, "..");
const docsDir = path.join(pkg, "..", "..", "docs", "themes");
const tsc = path.join(pkg, "node_modules", ".bin", "tsc");

/** [{file, line, info, code}] for every fenced block. */
export function blocks(md, file) {
  const out = [];
  const lines = md.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const m = /^```(\S*)\s*(.*)$/.exec(lines[i]);
    if (!m) continue;
    const start = i + 1;
    const body = [];
    for (i++; i < lines.length && !lines[i].startsWith("```"); i++) body.push(lines[i]);
    out.push({ file, line: start, lang: m[1], info: m[2], code: body.join("\n") + "\n" });
  }
  return out;
}

function compile(sources) {
  const dir = mkdtempSync(path.join(tmpdir(), "mesh-theme-docs-"));
  try {
    sources.forEach((s, i) => writeFileSync(path.join(dir, `block-${i}.tsx`), s.code));
    writeFileSync(
      path.join(dir, "tsconfig.json"),
      JSON.stringify({
        extends: path.join(pkg, "tsconfig.json"),
        compilerOptions: {
          paths: {
            "@entire-vc/mesh-theme-sdk": [path.join(pkg, "src", "index.ts")],
            "*": [path.join(pkg, "node_modules", "@types", "*"), path.join(pkg, "node_modules", "*")],
          },
          types: [],
        },
        include: ["./*.tsx"],
      }),
    );
    const r = spawnSync(tsc, ["-p", path.join(dir, "tsconfig.json"), "--noEmit"], { encoding: "utf8" });
    return { status: r.status, output: r.stdout + r.stderr };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const all = readdirSync(docsDir)
  .filter((f) => f.endsWith(".md"))
  .flatMap((f) => blocks(readFileSync(path.join(docsDir, f), "utf8"), f));
const ts = all.filter((b) => /^tsx?$/.test(b.lang));
const checked = ts.filter((b) => !/\bfragment\b/.test(b.info));
assert.ok(checked.length > 0, "no ts/tsx block is checked: the extractor found nothing");

const res = compile(checked);
assert.equal(res.status, 0, `docs examples do not compile:\n${res.output}`);

// Red control: the same compile must reject a block that uses a primitive that does not exist.
const red = compile([{ code: 'import { NotAPrimitive } from "@entire-vc/mesh-theme-sdk";\nexport const x = NotAPrimitive;\n' }]);
assert.notEqual(red.status, 0, "the compile accepted a block importing a missing primitive: the check proves nothing");

console.log(`docs ok: ${checked.length} example(s) compile, ${ts.length - checked.length} marked fragment; red control fails as it should`);
