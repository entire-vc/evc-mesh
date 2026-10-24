#!/usr/bin/env node
// The control theme (packages/mesh-theme-board-studio) is what Mesh ships as the builtin `board-studio`
// (internal/themes/builtin/board-studio): a fresh copy builds green on the real contract suites for all five of its
// slots, the manifest is valid, and the assets the Go binary embeds are byte-for-byte what that build produces. A card
// without Draggable turns the build red. Needs the web workspace installed (CI test:web) and the registry; the SDK is
// linked from this checkout.
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { schemaErrors } from "../cli/manifest.mjs";

const pkg = path.join(import.meta.dirname, "..");
const theme = path.join(pkg, "..", "mesh-theme-board-studio");
const embedded = path.join(pkg, "..", "..", "internal", "themes", "builtin", "board-studio");
const json = (f) => JSON.parse(readFileSync(f, "utf8"));
const pnpm = (cwd, ...args) => spawnSync("pnpm", args, { cwd, encoding: "utf8", env: { ...process.env, CI: "1" } });
const tail = (r) => (r.stdout + r.stderr).split("\n").slice(-25).join("\n");

const dir = mkdtempSync(path.join(tmpdir(), "mesh-theme-board-studio-"));
cpSync(theme, dir, { recursive: true, filter: (p) => !/node_modules|\/dist|\.contract-dist/.test(p) });
const pj = json(path.join(dir, "package.json"));
pj.dependencies["@entire-vc/mesh-theme-sdk"] = `link:${pkg}`;
writeFileSync(path.join(dir, "package.json"), JSON.stringify(pj, null, 2));
const install = pnpm(dir, "install", "--no-frozen-lockfile");
assert.equal(install.status, 0, tail(install));

// Green: five slots, valid manifest.
const built = pnpm(dir, "build");
assert.equal(built.status, 0, tail(built));
const manifestPath = path.join(dir, "dist", "mesh-extension.json");
const manifest = json(manifestPath);
assert.deepEqual(schemaErrors(manifest), [], "the built manifest is valid against the manifest schema");
assert.deepEqual(Object.keys(manifest.theme.contract_report.passed).sort(), [
  "board.card", "board.column", "board.layout", "dashboard.layout", "dashboard.widget.focus",
]);
const unit = pnpm(dir, "test");
assert.equal(unit.status, 0, tail(unit));
console.log("board-studio green: five slots pass, manifest valid, unit tests pass");

// The embedded copy is this build.
for (const f of ["mesh-extension.json", "views.js", "theme.css"]) {
  assert.equal(
    readFileSync(path.join(dir, "dist", f), "utf8"),
    readFileSync(path.join(embedded, f), "utf8"),
    `internal/themes/builtin/board-studio/${f} is stale: rebuild the theme and copy dist/{mesh-extension.json,views.js,theme.css} there`,
  );
}
console.log("board-studio embedded copy equals the fresh build");

// Red: a card that cannot be dragged.
const cardFile = path.join(dir, "src", "card.tsx");
const card = readFileSync(cardFile, "utf8");
const broken = card.replace(/<Draggable /, "<div ").replace(/<\/Draggable>/, "</div>");
assert.notEqual(broken, card, "the red control must change the card");
writeFileSync(cardFile, broken);
const red = pnpm(dir, "build");
assert.notEqual(red.status, 0, "a card without Draggable must fail the build");
assert.match(red.stdout + red.stderr, /board\.card: CT-board-\d+/);
assert.equal(existsSync(manifestPath), false, "a red build leaves no install-ready manifest");
console.log("board-studio red: no Draggable -> CT-board-* fail, no manifest");
