// The SDK's contract suites (packages/mesh-theme-sdk/testing) run on `mesh-default`: every slot v1, dashboard.layout v2 (v1 stays for themes that declared ^1), zero differences.
import { expect } from "vitest";
import { registerContractSuites, setHarnessHost } from "@entire-vc/mesh-theme-sdk/testing";
import { registerMeshDefaultTheme } from "@/themes/mesh-default";
import { meshHost } from "@/test-utils/slot-host";
import type { ComponentType } from "react";
import { MeshDefaultLayout, WIDGETS } from "@/core/dashboard";
import { resolveSlotView } from "../registry";
import "../mesh-default";

registerMeshDefaultTheme();
setHarnessHost(meshHost);

const SLOTS = ["board.layout", "board.column", "board.card", "task.row", "task.view", "doc.tree", "doc.view", "nav.sidebar", "nav.topbar", "auth.screen"] as const;
// The dashboard draws its mesh-default views itself (core/dashboard/slots.tsx), outside the slot registry: every
// registered widget with a data hook (widgets/*/index.ts) is a dashboard.widget slot and runs its suites. A widget
// without one (event-feed) is drawn as a v2 component that loads its own data, outside the slot contract.
// The views are read eagerly here, every group's views.tsx (the app loads most of them lazily, see registry.ts).
const SLOT_WIDGETS = new Set(WIDGETS.filter((d) => d.dataHook).map((d) => d.id));
const WIDGET_VIEWS: Record<string, ComponentType<never>> = Object.assign(
  {},
  ...Object.values(import.meta.glob<{ views?: Record<string, ComponentType<never>> }>("../../dashboard/widgets/*/views.tsx", { eager: true })).map((m) => m.views ?? {}),
);
const DASHBOARD = {
  "dashboard.layout": { view: MeshDefaultLayout, major: 2 },
  ...Object.fromEntries(Object.entries(WIDGET_VIEWS).filter(([id]) => SLOT_WIDGETS.has(id)).map(([id, v]) => [`dashboard.widget.${id}`, v])),
};

registerContractSuites({ ...Object.fromEntries(SLOTS.map((s) => [s, resolveSlotView(s)])), ...DASHBOARD } as never, (report) => {
  expect(Object.keys(report.failed)).toEqual([]);
  expect(Object.keys(report.passed).sort()).toEqual([...SLOTS, ...Object.keys(DASHBOARD)].sort());
});
