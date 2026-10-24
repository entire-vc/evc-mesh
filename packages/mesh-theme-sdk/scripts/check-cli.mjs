#!/usr/bin/env node
// Tests of `mesh-theme` (T6.3): a green build reports the real sha256 and a schema-valid manifest, a red
// slot fails the build and leaves no manifest, a bundle with its own React is refused, `dev` rebuilds.
// The contract suites themselves are the SDK's (T6.2); here a stand-in runner (scripts/fake-runner.mjs)
// decides which slots pass, so these tests prove the CLI's handling, not the suites.
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import assert from "node:assert/strict";
import { dev } from "../cli/dev.mjs";
import { checkBundleText } from "../cli/externals.mjs";
import { schemaErrors } from "../cli/manifest.mjs";

const pkg = path.join(import.meta.dirname, "..");
const cli = path.join(pkg, "cli", "mesh-theme.mjs");
const runner = path.join(pkg, "scripts", "fake-runner.mjs");
const sha = (b) => createHash("sha256").update(b).digest("hex");

const copyTheme = (name) => {
  const dir = mkdtempSync(path.join(tmpdir(), `mesh-theme-${name}-`));
  cpSync(path.join(pkg, "fixtures", `theme-${name}`), dir, { recursive: true });
  return dir;
};
const run = (cwd, args, env = {}) => spawnSync("node", [cli, ...args], { cwd, encoding: "utf8", env: { ...process.env, ...env } });
const json = (f) => JSON.parse(readFileSync(f, "utf8"));
// The system's own tool, not node's crypto: the hash in the report must match what an author gets from the shell.
const shasum = (f) => {
  const r = spawnSync("sha256sum", [f], { encoding: "utf8" });
  return (r.error ? spawnSync("shasum", ["-a", "256", f], { encoding: "utf8" }) : r).stdout.split(" ")[0];
};
// What the CLI wrote for fixtures/theme-good, committed so the Go side (internal/themes/cli_output_test.go)
// checks the server against real CLI output. UPDATE_GOLDEN=1 rewrites it; otherwise a drift fails here.
const golden = path.join(pkg, "..", "..", "internal", "themes", "testdata", "cli-dist");

// The CLI validates against its own copy of the manifest schema; it must be the server's.
const serverSchema = path.join(pkg, "..", "..", "internal", "extensions", "schema", "manifest.v1.json");
if (existsSync(serverSchema)) {
  assert.ok(readFileSync(serverSchema).equals(readFileSync(path.join(pkg, "schema", "manifest.v1.json"))), "schema/manifest.v1.json drifted from internal/extensions/schema/manifest.v1.json");
}

// -- the CLI reads `import.meta.dirname` (Node 20.11+): the declared engine must not promise older Node 20 ---------
const engine = json(path.join(pkg, "package.json")).engines.node;
assert.match(engine, /^>=(2[1-9]|20\.(1[1-9]|[2-9]\d))/, `engines.node ${engine} admits Node without import.meta.dirname`);

// -- green: hash equals sha256sum of the file, manifest is schema-valid, no second React ----------
const good = copyTheme("good");
let r = run(good, ["build", "--contract-runner", runner]);
assert.equal(r.status, 0, r.stdout + r.stderr);
const m = json(path.join(good, "dist/mesh-extension.json"));
const views = readFileSync(path.join(good, "dist/views.js"));
assert.equal(m.theme.views.sha256, shasum(path.join(good, "dist/views.js")), "views sha256 must equal shasum of the file");
assert.equal(m.theme.contract_report.bundle_sha256, m.theme.views.sha256);
assert.equal(m.theme.css.sha256, shasum(path.join(good, "dist/theme.css")));
assert.deepEqual(m.theme.contract_report.passed, { "board.card": "1.0.0" });
assert.deepEqual(m.theme.slots, { "board.card": "^1" });
assert.deepEqual(schemaErrors(m), []);
assert.deepEqual(checkBundleText(views.toString("utf8")), [], "no React of its own, only import-map imports");
for (const f of ["mesh-extension.json", "views.js", "theme.css"]) {
  const fresh = readFileSync(path.join(good, "dist", f));
  if (process.env.UPDATE_GOLDEN) {
    mkdirSync(golden, { recursive: true });
    writeFileSync(path.join(golden, f), fresh);
  } else if (existsSync(golden)) {
    assert.ok(fresh.equals(readFileSync(path.join(golden, f))), `${f} differs from internal/themes/testdata/cli-dist; rerun with UPDATE_GOLDEN=1`);
  }
}
const first = m.theme.views.sha256;
r = run(good, ["build", "--contract-runner", runner]);
assert.equal(json(path.join(good, "dist/mesh-extension.json")).theme.views.sha256, first, "the build is deterministic");

// -- red: a failing slot ends the build with !=0, is not in `passed`, and no manifest stays ------
r = run(good, ["build", "--contract-runner", runner], { FAKE_FAIL: "board.card" });
assert.notEqual(r.status, 0, "a red slot must fail the build");
assert.match(r.stderr, /board\.card: CT-fake/);
assert.equal(existsSync(path.join(good, "dist/mesh-extension.json")), false, "no manifest after a red build (the green one was removed)");
const rep = json(path.join(good, "dist/contract_report.json"));
assert.equal("board.card" in rep.passed, false);
assert.deepEqual(Object.keys(rep.failed), ["board.card"]);

// -- no runner = no report -----------------------------------------------------------------------
r = run(good, ["build", "--contract-runner", "nope.mjs"]);
assert.equal(r.status, 2, r.stdout + r.stderr);
assert.equal(existsSync(path.join(good, "dist/mesh-extension.json")), false);

// -- guards the build relies on, each fed a known-bad input --------------------------------------
assert.ok(checkBundleText('var a=Symbol.for("react.transitional.element");').some((p) => /copy of React/.test(p)));
assert.ok(checkBundleText('import x from "lodash";').some((p) => /lodash/.test(p)));
const noReport = structuredClone(m);
delete noReport.theme.contract_report;
assert.notEqual(schemaErrors(noReport).length, 0, "views without contract_report must not validate");

// -- dev: serves the bundle, rebuilds on change, only the instance origin may read it ------------
const instance = createServer((_, res) => res.end("ok"));
await new Promise((ok) => instance.listen(0, "127.0.0.1", ok));
const logs = [];
const d = await dev({ cwd: good, instance: `http://127.0.0.1:${instance.address().port}`, port: 0, log: (l) => logs.push(l) });
const get = (p, init) => fetch(d.url.replace("/views.js", p), init);
const waitFor = async (cond) => { for (let i = 0; i < 100 && !(await cond()); i++) await new Promise((ok) => setTimeout(ok, 50)); };
await waitFor(() => d.files["views.js"]);
let res = await get("/views.js");
assert.equal(res.status, 200);
assert.equal(res.headers.get("access-control-allow-origin"), `http://127.0.0.1:${instance.address().port}`);
const before = await res.text();
assert.ok(before.includes("board.card"));
const src = path.join(good, "src/index.tsx");
writeFileSync(src, readFileSync(src, "utf8").replace("TaskLink taskId", "TaskLink data-dev=\"rebuilt\" taskId"));
await waitFor(async () => (await (await get("/views.js")).text()) !== before);
assert.ok((await (await get("/views.js")).text()).includes("rebuilt"), "dev serves the rebuilt bundle");
writeFileSync(src, "this is not valid {{{");
await waitFor(() => logs.some((l) => /build failed/.test(l)));
assert.ok((await (await get("/views.js")).text()).includes("rebuilt"), "a broken edit keeps the last good bundle");
assert.ok(logs.some((l) => /localStorage\.setItem\("mesh-theme-dev-bundle"/.test(l)));
assert.equal(sha(Buffer.from(before)).length, 64);
await d.close();
instance.close();

console.log("ok: mesh-theme build (green, red, no runner) and dev (serve, rebuild, keep last good) behave");
