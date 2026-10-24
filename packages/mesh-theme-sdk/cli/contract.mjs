import { pathToFileURL } from "node:url";
import path from "node:path";

/**
 * Loads the contract test runner. By default it runs the SDK's CT-* suites in the Mesh web workspace
 * (vitest-runner.mjs); `--contract-runner <module>` swaps it, which the CLI's own tests use.
 *
 * A runner exports `runContractTests({ bundlePath })` and answers, for every slot the
 * bundle's `views` declares: `{ passed: boolean, contract: "x.y.z", failures: string[] }`.
 */
export async function loadRunner(spec, cwd) {
  let mod;
  try {
    mod = await import(spec ? pathToFileURL(path.resolve(cwd, spec)).href : "./vitest-runner.mjs");
  } catch (e) {
    throw new Error(
      `contract tests are not available (${spec ?? "vitest-runner"}): ${e.message}. ` +
        "A report without them would be a claim without proof, so no manifest is written.",
    );
  }
  if (typeof mod.runContractTests !== "function") throw new Error("contract runner has no runContractTests export");
  return mod.runContractTests;
}

/** The contract_report `passed` map and the failed slots of a runner answer. */
export function summarize(answer) {
  const passed = {};
  const failed = {};
  for (const [slot, r] of Object.entries(answer)) {
    if (r.passed) passed[slot] = r.contract;
    else failed[slot] = r.failures?.length ? r.failures : ["contract tests failed"];
  }
  return { passed, failed };
}
