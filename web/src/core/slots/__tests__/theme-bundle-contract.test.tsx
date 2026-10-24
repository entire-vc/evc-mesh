// The contract suites over a BUILT theme bundle: `mesh-theme build` (packages/mesh-theme-sdk/cli/vitest-runner.mjs) runs
// this file with MESH_THEME_BUNDLE (path to views.js) and MESH_THEME_REPORT (where the per-slot report goes). Without
// them there is nothing to test, so the file stays a no-op in the ordinary unit run.
import { writeFileSync } from "node:fs";
import { it } from "vitest";
import { registerContractSuites, setHarnessHost } from "@entire-vc/mesh-theme-sdk/testing";
import * as mod from "@theme-bundle";
import { meshHost } from "@/test-utils/slot-host";

const bundle = process.env.MESH_THEME_BUNDLE;
const reportPath = process.env.MESH_THEME_REPORT;

if (!bundle || !reportPath) {
  it.skip("theme bundle contract (MESH_THEME_BUNDLE not set)", () => {});
} else {
  const views = ((mod as { default?: { views?: unknown } }).default?.views ?? {}) as Record<string, { contract?: string; view: unknown }>;
  setHarnessHost(meshHost);
  // The bundle declares `contract: "^1"`; the suites take the major.
  const slots = Object.fromEntries(
    Object.entries(views).map(([slot, v]) => [slot, { view: v.view, major: Number(/\d+/.exec(v.contract ?? "^1")?.[0] ?? 1) }]),
  );
  registerContractSuites(slots as never, (report) => writeFileSync(reportPath, JSON.stringify(report)));
  if (!Object.keys(slots).length) it("views.js declares no slot views", () => {});
}
