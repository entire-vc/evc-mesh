#!/usr/bin/env node
// A fresh copy of packages/mesh-theme-template does what its README says (T6.4 AC): `pnpm install && pnpm build &&
// pnpm test` are green and the manifest is valid; replacing Draggable with a div turns the board.card contract red,
// the report negative and leaves no install-ready manifest. Needs the web workspace installed (CI test:web) and
// the registry; the SDK is linked from this checkout because the template asks for the published version.
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { schemaErrors } from "../cli/manifest.mjs";

const pkg = path.join(import.meta.dirname, "..");
const template = path.join(pkg, "..", "mesh-theme-template");
const json = (f) => JSON.parse(readFileSync(f, "utf8"));
const pnpm = (cwd, ...args) => spawnSync("pnpm", args, { cwd, encoding: "utf8", env: { ...process.env, CI: "1" } });
const tail = (r) => (r.stdout + r.stderr).split("\n").slice(-25).join("\n");

const dir = mkdtempSync(path.join(tmpdir(), "mesh-theme-template-"));
cpSync(template, dir, { recursive: true, filter: (p) => !/node_modules|\/dist|\.contract-dist/.test(p) });
const pj = json(path.join(dir, "package.json"));
pj.dependencies["@entire-vc/mesh-theme-sdk"] = `link:${pkg}`;
writeFileSync(path.join(dir, "package.json"), JSON.stringify(pj, null, 2));

const install = pnpm(dir, "install", "--no-frozen-lockfile");
assert.equal(install.status, 0, tail(install));

// Green.
const built = pnpm(dir, "build");
assert.equal(built.status, 0, tail(built));
const manifestPath = path.join(dir, "dist", "mesh-extension.json");
const manifest = json(manifestPath);
assert.deepEqual(schemaErrors(manifest), [], "the built manifest is valid against the manifest schema");
assert.deepEqual(manifest.theme.contract_report.passed, { "board.card": "1.0.0" });
assert.equal(manifest.theme.contract_report.bundle_sha256, manifest.theme.views.sha256);
const test = pnpm(dir, "test");
assert.equal(test.status, 0, tail(test));
console.log("template green: build + test pass, manifest valid, board.card in the report");

// Red: a card that cannot be dragged.
const cardFile = path.join(dir, "src", "card.tsx");
const card = readFileSync(cardFile, "utf8");
const broken = card.replace(/<Draggable [^>]*>\{body\}<\/Draggable>/, "<div>{body}</div>");
assert.notEqual(broken, card, "the red control must change the card");
writeFileSync(cardFile, broken);
const red = pnpm(dir, "build");
assert.notEqual(red.status, 0, "a card without Draggable must fail the build");
assert.match(red.stdout + red.stderr, /board\.card: CT-board-\d+/);
assert.equal(existsSync(manifestPath), false, "a red build leaves no install-ready manifest");
assert.equal("board.card" in json(path.join(dir, "dist", "contract_report.json")).passed, false);
assert.notEqual(pnpm(dir, "test").status, 0, "pnpm test is red too");
console.log("template red: no Draggable -> CT-board-* fail, board.card not in passed, no manifest");
