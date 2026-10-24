#!/usr/bin/env node
// `mesh-theme build` on the SDK's real contract suites (no stand-in runner): needs the web workspace installed
// (CI test:web). Green: the example theme passes every board.card test. Red (T6.3 AC): a board card without Draggable fails
// the build, board.card is not in `passed`, and no install-ready manifest is written.
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import assert from "node:assert/strict";

const pkg = path.join(import.meta.dirname, "..");
const cli = path.join(pkg, "cli", "mesh-theme.mjs");
const build = (name) => {
  const dir = mkdtempSync(path.join(tmpdir(), `mesh-theme-real-${name}-`));
  cpSync(path.join(pkg, "fixtures", `theme-${name}`), dir, { recursive: true });
  return { dir, r: spawnSync("node", [cli, "build"], { cwd: dir, encoding: "utf8" }) };
};
const report = (dir) => JSON.parse(readFileSync(path.join(dir, "dist/contract_report.json"), "utf8"));

const bad = build("no-draggable");
assert.equal(bad.r.status, 1, bad.r.stdout + bad.r.stderr);
assert.match(bad.r.stderr, /board\.card: CT-board-\d+/);
assert.equal("board.card" in report(bad.dir).passed, false, "board.card must not be in passed");
assert.equal(existsSync(path.join(bad.dir, "dist/mesh-extension.json")), false, "no manifest after a red build");
console.log("red ok:", bad.r.stderr.split("\n").find((l) => /board\.card/.test(l)));

// Green: the example theme is a complete card (Draggable, chosen fields, overdue, editor, overlay, task.badge point),
// so every contract test of board.card passes on the real suites and the build is install-ready.
const good = build("good");
assert.equal(good.r.status, 0, good.r.stdout + good.r.stderr);
assert.deepEqual(report(good.dir).passed, { "board.card": "1.0.0" });
assert.ok(existsSync(path.join(good.dir, "dist/mesh-extension.json")), "a green build writes the manifest");
const manifest = JSON.parse(readFileSync(path.join(good.dir, "dist/mesh-extension.json"), "utf8"));
const sha = createHash("sha256").update(readFileSync(path.join(good.dir, "dist/views.js"))).digest("hex");
assert.equal(manifest.theme.views.sha256, sha, "the reported views hash is the file's hash");
assert.equal(manifest.theme.contract_report.bundle_sha256, sha);
console.log("green ok: board.card passes every CT-* on the real suites");
