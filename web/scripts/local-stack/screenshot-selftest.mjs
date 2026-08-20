#!/usr/bin/env node
// Negative control for screenshot.mjs — required by scripts/local-stack.sh's
// acceptance criteria (§ "Негативный контроль обязателен"). Runs standalone,
// no docker/API/vite needed, so it can be run on its own and in CI.
//
// Proves two things about the detector in screenshot.mjs:
//   1. a page whose scroller tracks the viewport (fixtures/ok.html) is captured
//      correctly — both markers land inside the frame (POSITIVE control: if this
//      fails, the tool is too strict and would reject good pages).
//   2. a page whose scroller does NOT track the viewport — a hardcoded pixel
//      height, exactly the D7 trap (fixtures/broken.html) — is caught and
//      refused rather than silently screenshotted wrong (NEGATIVE control: if
//      this fails to fail, the tool is exactly as blind as the bug it fixes).
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";

const __dirname = dirname(fileURLToPath(import.meta.url));
const screenshotScript = join(__dirname, "screenshot.mjs");

function run(fixture, outDir) {
  const url = "file://" + join(__dirname, "fixtures", fixture);
  const res = spawnSync(
    process.execPath,
    [
      screenshotScript,
      "--no-login",
      "--url",
      url,
      "--widths",
      "800",
      "--out-dir",
      outDir,
      "--prefix",
      fixture.replace(".html", ""),
      "--assert-visible",
      "#top,#bottom",
    ],
    { encoding: "utf8" }
  );
  return res;
}

function main() {
  const outDir = mkdtempSync(join(tmpdir(), "local-stack-selftest-"));
  let failures = [];

  console.log("--- positive control: fixtures/ok.html (must PASS) ---");
  const ok = run("ok.html", outDir);
  console.log(ok.stdout);
  if (ok.status !== 0) {
    console.error(ok.stderr);
    failures.push(
      "POSITIVE control failed: a capturable page (scroller tracks the viewport) was rejected. " +
        "The detector is too strict and would block every real screenshot."
    );
  } else {
    console.log("OK: ok.html captured cleanly, both markers in frame.");
  }

  console.log("\n--- negative control: fixtures/broken.html (must FAIL) ---");
  const broken = run("broken.html", outDir);
  console.log(broken.stdout);
  if (broken.status === 0) {
    failures.push(
      "NEGATIVE control failed to fail: broken.html has a marker permanently below the fold " +
        "(fixed-pixel-height inner scroller, does not track viewport growth) and the tool reported " +
        "success anyway. This is exactly the fullPage/inner-scroll trap the tool exists to catch — " +
        "if this control passes, the detector is blind to it."
    );
  } else {
    console.log("OK: broken.html correctly refused —", broken.stderr.trim().split("\n").pop());
  }

  rmSync(outDir, { recursive: true, force: true });

  if (failures.length > 0) {
    console.error("\nSELFTEST FAILED:");
    for (const f of failures) console.error(" - " + f);
    process.exit(1);
  }
  console.log("\nSELFTEST PASSED: detector accepts good layouts and refuses the D7 inner-scroll trap.");
}

main();
