// Fails the build when web/src bypasses the theme tokens:
//   1. a raw Tailwind palette class (bg-amber-500, text-blue-100/50, dark:border-red-900 …)
//   2. a hex colour literal (#fff, #2dd4a8)
// Use `bg-tone-warning-500` / semantic tokens instead (src/styles/brandkit.css).
// A hex that is user data (stored default for <input type=color>, contrast maths)
// carries `color-ok` in a comment on the same line.
// Tests are skipped. Run: pnpm check:tokens
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const SRC = process.argv[2] ?? fileURLToPath(new URL("../src", import.meta.url));
const HUES = "slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose";
const PFX = "bg|text|border|ring|fill|stroke|from|to|via|divide|outline|decoration|shadow|accent|caret|placeholder";
// Optional side/axis/offset segment: border-l-amber-400, divide-x-red-500, ring-offset-blue-200.
const PALETTE = new RegExp(`(?<![\\w-])(${PFX})(?:-(?:[lrtbxyse]|offset|inset))?-(${HUES})-\\d+`, "g");
// `#639` in a comment or `href="#x"` is not a colour: only flag hex inside a string literal.
// In stylesheets any 3/4/6/8-digit hex after `:` or `(` or a space is a colour.
const HEX_IN_CSS = /[:(,\s](#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3}))(?![\w-])/g;
// In TS/TSX strings (may span lines, e.g. a template literal): 3/6-digit anywhere; 4/8-digit (alpha)
// only when the string is exactly the colour, sits right after `[` `(` `,`, or follows a CSS value
// (`0 0 2px #11223344`, `1px solid #abcd`), so an 8-hex task id inside a prose string is not a
// false positive.
const HEX_IN_STRING = new RegExp(
  "([\"'`])[^\"'`]*?(#(?:[0-9a-fA-F]{6}|[0-9a-fA-F]{3}))(?![\\w-])" +
    "|([\"'`])(#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{4}))\\3" +
    "|(?<=[\\[(,]\\s*|(?:\\d|px|rem|em|%|\\)|solid|dashed|dotted|inset)\\s+)(#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{4}))(?![\\w-])",
  "g",
);

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name === "__tests__" || name === "test-utils") continue;
      yield* walk(p);
    } else if (/\.(ts|tsx|css)$/.test(name) && !/\.test\./.test(name)) yield p;
  }
}

const bad = [];
for (const file of walk(SRC)) {
  const rel = relative(SRC, file);
  const isTokenFile = rel === join("styles", "brandkit.css");
  const text = readFileSync(file, "utf8");
  // Block comments (incl. multi-line) are blanked but keep their newlines so line numbers stay right.
  const stripped = text.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
  const original = text.split("\n");
  const code = stripped.split("\n").map((l) => l.replace(/(^|\s)\/\/.*$/, "$1"));
  // Line number of a match: lines are joined with "\n", so count newlines before its index.
  const joined = code.join("\n");
  const lineOf = (idx) => joined.slice(0, idx).split("\n").length;
  const flag = (idx, what) => bad.push(`${rel}:${lineOf(idx)}: ${what}`);
  for (const m of joined.matchAll(PALETTE)) flag(m.index, `raw palette class ${m[0]}`);
  if (!isTokenFile) {
    const re = file.endsWith(".css") ? HEX_IN_CSS : HEX_IN_STRING;
    for (const m of joined.matchAll(re)) {
      const h = file.endsWith(".css") ? m[1] : (m[2] ?? m[4] ?? m[5]);
      const at = m.index + m[0].lastIndexOf(h);
      if (!original[lineOf(at) - 1].includes("color-ok")) flag(at, `hex colour ${h}`);
    }
  }
}
if (bad.length) {
  console.error(`check:tokens — ${bad.length} violation(s):\n` + bad.join("\n"));
  process.exit(1);
}
console.log("check:tokens — ok");
