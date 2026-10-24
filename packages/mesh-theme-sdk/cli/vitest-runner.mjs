import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

const SPEC = "src/core/slots/__tests__/theme-bundle-contract.test.tsx";

/**
 * The SDK's contract suites (`@entire-vc/mesh-theme-sdk/testing`) need a host that supplies the real primitives, so
 * they run in the Mesh web workspace under vitest: this runner starts that one test file against the built bundle
 * and reads the per-slot report it writes. `MESH_WEB_DIR` (or `--web-dir`) points at the workspace; inside the
 * monorepo it is found on its own.
 */
export async function runContractTests({ bundlePath, webDir }) {
  const web = path.resolve(webDir ?? process.env.MESH_WEB_DIR ?? path.join(import.meta.dirname, "..", "..", "..", "web"));
  if (!existsSync(path.join(web, SPEC))) throw new Error(`Mesh web workspace with the contract host not found at ${web}; set MESH_WEB_DIR`);
  const tmp = mkdtempSync(path.join(tmpdir(), "mesh-theme-ct-"));
  const reportPath = path.join(tmp, "report.json");
  try {
    const r = spawnSync("pnpm", ["exec", "vitest", "run", SPEC], {
      cwd: web,
      encoding: "utf8",
      env: { ...process.env, MESH_THEME_BUNDLE: path.resolve(bundlePath), MESH_THEME_REPORT: reportPath },
    });
    if (!existsSync(reportPath)) throw new Error(`contract run produced no report (vitest exit ${r.status}):\n${(r.stdout + r.stderr).split("\n").slice(-25).join("\n")}`);
    const report = JSON.parse(readFileSync(reportPath, "utf8"));
    const answer = {};
    for (const [slot, contract] of Object.entries(report.passed)) answer[slot] = { passed: true, contract, failures: [] };
    for (const [slot, ids] of Object.entries(report.failed)) answer[slot] = { passed: false, contract: report.contract, failures: ids.map((id) => `${id} failed`) };
    return answer;
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}
