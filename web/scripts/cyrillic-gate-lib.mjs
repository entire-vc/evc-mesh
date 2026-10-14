// Cyrillic gate: web UI strings live in src/locales/<lang>/*.json, never in
// .ts/.tsx. Finds Cyrillic in code outside locales/, tests and comments.
//
// Strict: every hit fails (the baseline mode ended with L2.3, baseline = 0).
import fs from "node:fs";
import path from "node:path";

const CYRILLIC = /[Ѐ-ӿ]/;

// Lines where Cyrillic is data, not UI text. Keep this list tiny and explained.
// Per-file LINE patterns, not whole files: a UI string added elsewhere in the
// same file must still be caught.
export const ALLOWLIST = {
  // Rows of the Cyrillic->Latin table for slugify(): `а: "a", б: "b", ...`
  "lib/utils.ts": /^\s*(?:[а-яё]:\s*"[a-z]*",?\s*)+$/,
};

// String-aware: `//` or `/*` inside a '...', "..." or `...` literal is code, not
// a comment. Comment characters are blanked (newlines kept) so line numbers hold.
// An unterminated ' or " ends at the line break (JSX text apostrophes), which can
// only produce extra hits, never hide one.
export function stripComments(src) {
  let out = "";
  let i = 0;
  const n = src.length;
  while (i < n) {
    const c = src[i];
    const next = src[i + 1];
    if (c === "/" && next === "/") {
      while (i < n && src[i] !== "\n") (out += " "), i++;
    } else if (c === "/" && next === "*") {
      const end = src.indexOf("*/", i + 2);
      const stop = end === -1 ? n : end + 2;
      out += src.slice(i, stop).replace(/[^\n]/g, " ");
      i = stop;
    } else if (c === '"' || c === "'" || c === "`") {
      out += c;
      i++;
      while (i < n && src[i] !== c && (c === "`" || src[i] !== "\n")) {
        if (src[i] === "\\" && i + 1 < n) out += src[i++];
        out += src[i++];
      }
      if (i < n && src[i] === c) out += src[i++];
    } else {
      out += c;
      i++;
    }
  }
  return out;
}

function walk(dir, out) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (["node_modules", "__tests__", "locales"].includes(entry.name)) continue;
      walk(full, out);
    } else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.(ts|tsx)$/.test(entry.name)) {
      out.push(full);
    }
  }
}

/** Returns { "<rel path>": [{ line, text }] } for every file with Cyrillic in code. */
export function scan(srcRoot) {
  const files = [];
  walk(srcRoot, files);
  const hits = {};
  for (const file of files) {
    const rel = path.relative(srcRoot, file).split(path.sep).join("/");
    const allowedLine = ALLOWLIST[rel];
    const lines = stripComments(fs.readFileSync(file, "utf8")).split("\n");
    lines.forEach((text, i) => {
      if (CYRILLIC.test(text) && !allowedLine?.test(text)) (hits[rel] ??= []).push({ line: i + 1, text: text.trim() });
    });
  }
  return { filesScanned: files.length, hits };
}

/** Violations to report: every hit fails. */
export function violations(hits) {
  return Object.entries(hits).map(([file, list]) => ({ file, found: list.length, lines: list }));
}
