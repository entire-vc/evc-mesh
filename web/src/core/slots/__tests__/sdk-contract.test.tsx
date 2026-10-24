// The SDK's contract suites (packages/mesh-theme-sdk/testing) run on `mesh-default`: every slot v1, zero differences.
import { expect } from "vitest";
import { registerContractSuites, setHarnessHost } from "@entire-vc/mesh-theme-sdk/testing";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { meshHost } from "@/test-utils/slot-host";
import { MeshDefaultLayout } from "@/core/dashboard";
import { MESH_DEFAULT_WIDGETS } from "@/core/dashboard/views/mesh-default";
import { resolveSlotView } from "../registry";
import "../mesh-default";

registerMeshDefaultTheme();
setHarnessHost(meshHost);

const SLOTS = ["board.layout", "board.column", "board.card", "task.row", "task.view", "doc.tree", "doc.view", "nav.sidebar", "nav.topbar", "auth.screen"] as const;
// The dashboard draws its mesh-default views itself (core/dashboard/slots.tsx), outside the slot registry.
const DASHBOARD = { "dashboard.layout": MeshDefaultLayout, ...Object.fromEntries(Object.entries(MESH_DEFAULT_WIDGETS).map(([id, v]) => [`dashboard.widget.${id}`, v])) };

registerContractSuites({ ...Object.fromEntries(SLOTS.map((s) => [s, resolveSlotView(s)])), ...DASHBOARD } as never, (report) => {
  expect(Object.keys(report.failed)).toEqual([]);
  expect(Object.keys(report.passed).sort()).toEqual([...SLOTS, ...Object.keys(DASHBOARD)].sort());
});
