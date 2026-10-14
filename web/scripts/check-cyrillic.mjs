// Usage: node scripts/check-cyrillic.mjs [--src <dir>]
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { scan, violations } from "./cyrillic-gate-lib.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const opt = (name, def) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : def;
};
const srcRoot = path.resolve(opt("--src", path.join(here, "../src")));

if (!fs.existsSync(srcRoot)) {
  console.error(`check-cyrillic: ${srcRoot} does not exist`);
  process.exit(2);
}
const { filesScanned, hits } = scan(srcRoot);
// A scan that read nothing would report a clean tree.
if (filesScanned < 1) {
  console.error(`check-cyrillic: no .ts/.tsx files under ${srcRoot}`);
  process.exit(2);
}

const bad = violations(hits);
if (bad.length === 0) {
  const total = Object.values(hits).reduce((n, l) => n + l.length, 0);
  console.log(`check-cyrillic OK: ${filesScanned} files, ${total} known line(s)`);
  process.exit(0);
}
console.error(`check-cyrillic FAILED: Cyrillic in code outside src/locales/ru`);
for (const v of bad) {
  console.error(`  ${v.file}: ${v.found} line(s)`);
  for (const l of v.lines) console.error(`    ${l.line}: ${l.text.slice(0, 120)}`);
}
console.error("Move the text to src/locales/<lang>/<ns>.json and use t(). See docs/web-i18n.md");
process.exit(1);
