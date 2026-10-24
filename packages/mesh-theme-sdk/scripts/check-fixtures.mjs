#!/usr/bin/env node
// Contract check: a theme using only what the SDK declares compiles; a theme importing a missing slot
// type or primitive is rejected by tsc (the red control, run on every call).
import { spawnSync } from "node:child_process";
import path from "node:path";

const root = path.join(import.meta.dirname, "..");
const tsc = (project) =>
  spawnSync("pnpm", ["exec", "tsc", "-p", project, "--noEmit"], { cwd: root, encoding: "utf8" });

const good = tsc("tsconfig.fixtures.json");
if (good.status !== 0) {
  console.error("FAIL: fixtures/good-theme.tsx must typecheck\n" + good.stdout + good.stderr);
  process.exit(1);
}
const bad = tsc("tsconfig.fixtures-bad.json");
const out = bad.stdout + bad.stderr;
// One "no exported member" diagnostic (TS2305) per missing name: a name merely echoed in another error does not count.
const rejected = (name) => new RegExp(`error TS2305: .*'${name}'`).test(out);
if (bad.status === 0 || !rejected("NoSuchSlotView") || !rejected("NoSuchPrimitive")) {
  console.error("FAIL: fixtures/bad-theme.tsx must be rejected for both missing names\n" + out);
  process.exit(1);
}
console.log("ok: good theme compiles; theme with a missing slot type and primitive is rejected");
