// CT-common-01..06 (Mesh Doc theme-slot-catalog-v1 §6). CT-common-07 (a crashing view falls back to mesh-default) is
// a core test: the host's CI runs it, a theme author cannot fail it from here.
import axe from "axe-core";
import { waitFor } from "@testing-library/react";
import { expect } from "vitest";
import { harnessHost } from "../host";
import { button, calls, tick } from "../helpers";
import { defineTests } from "../suite";
import type { ExtensionPoint } from "../types";

/** Slots whose data carries an Async state (loading / error) the view must draw. */
const ASYNC = ["board.layout", "task.view", "doc.tree", "doc.view", "dashboard.widget.*"];
const ALL = [
  "board.layout", "board.column", "board.card", "task.row", "task.view", "nav.sidebar", "nav.topbar",
  "doc.tree", "doc.view", "auth.screen", "dashboard.layout", "dashboard.widget.*",
];
/** Where a view must carry an extension insertion point, and which one (catalog §4 rule 6). */
export const EXTENSION_POINTS: Record<string, ExtensionPoint[]> = {
  "board.card": ["task.badge"],
  "task.view": ["task.panel", "task.actions"],
  "nav.sidebar": ["sidebar.item"],
};

async function axeViolations(root: HTMLElement) {
  // jsdom has no layout or paint: colour contrast is checked by the design-token gate, not here.
  const r = await axe.run(root, { rules: { "color-contrast": { enabled: false }, region: { enabled: false } } });
  return r.violations.filter((v) => v.impact === "critical").map((v) => `${v.id}: ${v.nodes[0]?.html.slice(0, 120)}`);
}

defineTests([
  {
    id: "CT-common-01",
    title: "loading and error are drawn, not empty; error has a working retry",
    slots: ASYNC,
    async run({ mount, slot }) {
      const loading = mount({ options: { state: "loading" } });
      expect(loading.container.textContent?.trim() || loading.container.querySelector("[role=status],[aria-busy],svg,img,[class]")).toBeTruthy();
      expect(loading.container.children.length).toBeGreaterThan(0);
      loading.unmount();
      const error = mount({ options: { state: "error" } });
      expect(error.container.textContent?.trim().length ?? 0).toBeGreaterThan(0);
      // A theme's own widget reads SDK hooks: the core turns a failed read into an empty result and gives the view
      // no `retry`, so there is nothing to click. What stays binding is that the state is drawn.
      if ((error.data() as { result?: unknown }).result === undefined && slot.startsWith("dashboard.widget.")) return;
      const retry = button(error.container, /retry|повтор|try again/i);
      expect(retry, "the error state needs a retry button").not.toBeNull();
      await error.user.click(retry!);
      expect(calls(error, "retry").length).toBeGreaterThan(0);
    },
  },
  {
    id: "CT-common-02",
    title: "the view does not throw on empty fields (null assignee, no labels, no due date, no custom fields)",
    slots: ALL,
    run({ mount }) {
      // The default fixture is the empty baseline; a viewer's actions are missing too (every optional method absent).
      for (const role of ["member", "viewer"] as const) {
        const m = mount({ role });
        expect(m.container.innerHTML.length).toBeGreaterThan(0);
        m.unmount();
      }
    },
  },
  {
    id: "CT-common-03",
    title: "no network calls past the harness while rendering and interacting",
    slots: ALL,
    async run({ mount }) {
      const m = mount();
      await tick();
      for (const el of Array.from(m.container.querySelectorAll<HTMLElement>("button:not([disabled])")).slice(0, 3)) await m.user.click(el);
      await m.user.tab();
      await m.emitRealtime({ type: "task.updated", data: { id: "t1", title: "Changed" }, at: "1" });
      await tick();
      expect(m.unexpectedRequests, "a view reaches data only through props and actions").toEqual([]);
    },
  },
  {
    id: "CT-common-04",
    title: "the extension insertion point is in the DOM when an extension fills it",
    slots: Object.keys(EXTENSION_POINTS),
    async run({ mount, slot }) {
      const points = EXTENSION_POINTS[slot] ?? [];
      const m = mount({ extensions: points });
      const host = harnessHost();
      for (const p of points) {
        await waitFor(() => expect(m.container.ownerDocument.querySelector(host.extensionSelector(p)), `extension point ${p} missing`).not.toBeNull());
      }
    },
  },
  {
    id: "CT-common-05",
    title: "axe: no critical a11y violations in ready, loading and error states",
    slots: ALL,
    async run({ mount, slot }) {
      const states = ASYNC.includes(slot) || slot.startsWith("dashboard.widget.") ? (["ready", "loading", "error"] as const) : (["ready"] as const);
      for (const state of states) {
        const m = mount({ options: { state } });
        await tick();
        expect(await axeViolations(m.container), `${state}`).toEqual([]);
        m.unmount();
      }
    },
  },
  {
    id: "CT-common-06",
    title: "renders in dark mode and on a phone without throwing",
    slots: ALL,
    run({ mount }) {
      const m = mount({ mode: "dark", isMobile: true });
      expect(m.container.innerHTML.length).toBeGreaterThan(0);
    },
  },
]);
