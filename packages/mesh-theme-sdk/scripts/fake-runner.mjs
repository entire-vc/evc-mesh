// A stand-in for the SDK's contract tests, for the CLI's own tests: it never runs the suites. Slots
// named in FAKE_FAIL (comma separated) fail, every other slot of the bundle passes at 1.0.0.
import { readFileSync } from "node:fs";

export async function runContractTests({ bundlePath }) {
  const src = readFileSync(bundlePath, "utf8");
  const slots = [...src.matchAll(/"((?:board|task|dashboard|nav|doc|auth)\.[a-z.-]+)":\s*\{/g)].map((m) => m[1]);
  const fail = new Set((process.env.FAKE_FAIL ?? "").split(",").filter(Boolean));
  return Object.fromEntries(
    slots.map((s) => [s, fail.has(s) ? { passed: false, contract: "1.0.0", failures: ["CT-fake: forced to fail"] } : { passed: true, contract: "1.0.0", failures: [] }]),
  );
}
