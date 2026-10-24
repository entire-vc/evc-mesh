// CT-board-01..18 (Mesh Doc slots-board §6). A card is found by the SDK Draggable around it
// (`data-mesh-draggable="task:<id>"`): a card that is not a Draggable has no way to be moved and fails these tests.
import { fireEvent, waitFor, within } from "@testing-library/react";
import { expect } from "vitest";
import { button, calls, keyboardMove, markup, pointerDrag, tick, visibleText } from "../helpers";
import { fixtures } from "../fixtures";
import { defineTests, type TestCtx } from "../suite";
import type { Mounted } from "../types";

const LCC = ["board.layout", "board.column", "board.card"];

const card = (m: Mounted, id: string): HTMLElement => {
  const el = m.container.querySelector<HTMLElement>(`[data-mesh-draggable="task:${id}"]`);
  if (!el) throw new Error(`no SDK Draggable for task ${id}: a board card must render inside <Draggable item={{kind:"task", id}}>`);
  return el;
};

const zone = (m: Mounted, id: string) => m.container.querySelector<HTMLElement>(`[data-mesh-dropzone="status:${id}"]`);

const cols = (ids: string[], tasks: ReturnType<typeof fixtures.task>[][], over: Array<Parameters<typeof fixtures.column>[2]> = []) =>
  ids.map((id, i) => fixtures.column(id, tasks[i] ?? [], over[i]));

const withColumns = (columns: ReturnType<typeof cols>) => ({ columns: { state: "ready" as const, value: columns } });

const cardOf = (t: TestCtx, tasks: ReturnType<typeof fixtures.task>[], extra: Record<string, unknown> = {}) =>
  t.mount({ data: { ...withColumns(cols(["backlog"], [tasks])), ...extra } as never });

defineTests([
  {
    id: "CT-board-01",
    title: "dragging a card to another column calls moveTask once with the right task, column and index",
    slots: LCC,
    async run({ mount }) {
      const m = mount();
      await pointerDrag(card(m, "t1"), 330);
      const c = calls(m, "moveTask");
      expect(c).toHaveLength(1);
      expect(c[0]![0]).toBe("t1");
      expect(c[0]![1]).toBe("progress");
      expect(typeof (c[0]![2] as { index: number }).index).toBe("number");
    },
  },
  {
    id: "CT-board-02",
    title: "without task.move the card has no handle, no tabindex, and cannot be moved",
    slots: ["board.column", "board.card"],
    async run({ mount }) {
      const m = mount({ role: "viewer" });
      const el = card(m, "t1");
      expect(el.hasAttribute("aria-roledescription")).toBe(false);
      expect(el.hasAttribute("tabindex")).toBe(false);
      await pointerDrag(el, 330);
      expect(calls(m, "moveTask")).toHaveLength(0);
    },
  },
  {
    id: "CT-board-03",
    title: "a realtime update changes the card in place; the other cards keep order and DOM nodes",
    slots: LCC,
    async run({ mount }) {
      const m = mount();
      const t1 = card(m, "t1");
      const t2 = card(m, "t2");
      const order = () => Array.from(m.container.querySelectorAll("[data-mesh-draggable]")).map((e) => e.getAttribute("data-mesh-draggable"));
      const before = order();
      await m.emitRealtime({ type: "task.updated", data: { id: "t1", title: "Новый" }, at: "1" });
      expect(within(m.container).getByText("Новый")).toBeTruthy();
      expect(card(m, "t1")).toBe(t1);
      expect(card(m, "t2")).toBe(t2);
      expect(order()).toEqual(before);
    },
  },
  {
    id: "CT-board-04",
    title: "keyboard: pick up, ArrowRight, drop moves the card and announces it; Escape cancels",
    slots: LCC,
    async run({ mount }) {
      const m = mount();
      await keyboardMove(card(m, "t1"), "ArrowRight");
      expect(calls(m, "moveTask")).toHaveLength(1);
      expect(calls(m, "moveTask")[0]!.slice(0, 2)).toEqual(["t1", "progress"]);
      expect(document.querySelector("[aria-live]")?.textContent?.trim().length ?? 0).toBeGreaterThan(0);
      await keyboardMove(card(m, "t3"), "ArrowRight", "Escape");
      expect(calls(m, "moveTask")).toHaveLength(1);
    },
  },
  {
    id: "CT-board-05",
    title: "grouped by priority nothing is draggable, by pointer or keyboard",
    slots: ["board.layout"],
    async run({ mount }) {
      const m = mount({ options: { groupBy: "priority" } });
      const el = card(m, "t1");
      expect(el.hasAttribute("tabindex")).toBe(false);
      expect(el.hasAttribute("aria-roledescription")).toBe(false);
      await pointerDrag(el, 330);
      await keyboardMove(el, "ArrowRight");
      expect(calls(m, "moveTask")).toHaveLength(0);
    },
  },
  {
    id: "CT-board-06",
    title: "a gated card is drawn once, in Triage, with its real status; nothing drops into Triage",
    slots: ["board.layout", "board.column"],
    async run({ mount }) {
      const m = mount({ options: { gated: true } });
      expect(m.container.querySelectorAll('[data-mesh-draggable="task:gated1"]')).toHaveLength(1);
      // Not in the zone of its status column (Triage has no zone at all).
      expect(card(m, "gated1").closest("[data-mesh-dropzone]")).toBeNull();
      expect(zone(m, "backlog")!.querySelector('[data-mesh-draggable="task:gated1"]')).toBeNull();
      expect(visibleText(card(m, "gated1"))).toContain("Backlog");
      // Triage is the first column (x = 0); t1 sits in the second.
      await pointerDrag(card(m, "t1"), -290);
      expect(calls(m, "moveTask")).toHaveLength(0);
    },
  },
  {
    id: "CT-board-07",
    title: "a closed column with more cards loads on request; the button is off while loading",
    slots: ["board.layout", "board.column"],
    async run({ mount }) {
      const more = (loadingMore: boolean) =>
        withColumns(cols(["backlog", "done"], [[fixtures.task("t1")], [fixtures.task("d1")]], [{}, { isClosed: true, hasMore: true, totalCount: 5, countLabel: "5", loadingMore, closedSlice: { loaded: 1, total: 5, filtersActive: false } }]));
      const m = mount({ data: more(false) as never });
      const btn = button(m.container, /more|ещё|еще|load/i);
      expect(btn, "a column with hasMore needs a way to load more").not.toBeNull();
      await m.user.click(btn!);
      expect(calls(m, "loadMore")).toEqual([["done"]]);
      await m.update(more(true) as never);
      const busy = button(m.container, /more|ещё|еще|load/i);
      expect(busy === null || busy.hasAttribute("disabled") || busy.getAttribute("aria-disabled") === "true").toBe(true);
    },
  },
  {
    id: "CT-board-08",
    title: "error: message and working retry; loading: a placeholder; refreshing: the old cards stay",
    slots: ["board.layout"],
    async run({ mount }) {
      const loading = mount({ options: { state: "loading" } });
      expect(loading.container.textContent?.trim().length || loading.container.querySelector("[role=status],[aria-busy]")).toBeTruthy();
      expect(loading.container.querySelector("[data-mesh-draggable]")).toBeNull();
      loading.unmount();
      const error = mount({ options: { state: "error" } });
      expect(visibleText(error.container)).toMatch(/could not load/i);
      await error.user.click(button(error.container, /retry|повтор/i)!);
      expect(calls(error, "retry")).toHaveLength(1);
      error.unmount();
      const refreshing = mount({ options: { state: "refreshing" } });
      expect(refreshing.container.querySelectorAll("[data-mesh-draggable]")).toHaveLength(3);
    },
  },
  {
    id: "CT-board-09",
    title: "a status column has a DropZone carrying its id; Triage has none",
    slots: ["board.column"],
    run({ mount }) {
      const m = mount({ options: { gated: true } });
      expect(m.container.querySelector('[data-mesh-dropzone="status:backlog"]')).not.toBeNull();
      expect(m.container.querySelector('[data-mesh-dropzone="status:progress"]')).not.toBeNull();
      expect(m.container.querySelector('[data-mesh-dropzone="status:triage"]')).toBeNull();
    },
  },
  {
    id: "CT-board-10",
    title: "only the chosen fields are drawn, in the chosen order; an empty custom field does not break",
    slots: ["board.card"],
    async run({ mount }) {
      const tasks = [fixtures.task("t1", { priority: "high", labels: ["LBL-SENTINEL"], customFields: { sprint: "SPRINT-SENTINEL" } })];
      const fieldsOf = (keys: string[]) => ({ cardFields: keys });
      const text = (m: Mounted) => visibleText(card(m, "t1"));
      const a = mount({ data: { ...withColumns(cols(["backlog"], [tasks])), ...fieldsOf(["cf:sprint", "labels"]) } as never });
      expect(text(a).indexOf("SPRINT-SENTINEL")).toBeGreaterThanOrEqual(0);
      expect(text(a).indexOf("SPRINT-SENTINEL")).toBeLessThan(text(a).indexOf("LBL-SENTINEL"));
      a.unmount();
      const b = mount({ data: { ...withColumns(cols(["backlog"], [tasks])), ...fieldsOf(["labels", "cf:sprint"]) } as never });
      expect(text(b).indexOf("LBL-SENTINEL")).toBeGreaterThanOrEqual(0);
      expect(text(b).indexOf("LBL-SENTINEL")).toBeLessThan(text(b).indexOf("SPRINT-SENTINEL"));
      b.unmount();
      const c = mount({ data: { ...withColumns(cols(["backlog"], [tasks])), ...fieldsOf(["priority"]) } as never });
      expect(text(c)).not.toContain("SPRINT-SENTINEL");
      expect(text(c)).not.toContain("LBL-SENTINEL");
      c.unmount();
      const empty = mount({ data: { ...withColumns(cols(["backlog"], [[fixtures.task("t1", { customFields: {} })]])), ...fieldsOf(["priority", "due_date", "cf:sprint"]) } as never });
      expect(card(empty, "t1")).toBeTruthy();
    },
  },
  {
    id: "CT-board-11",
    title: "overdue comes from the data: the indicator follows task.overdue, not dueDate",
    slots: ["board.card"],
    run(t) {
      const html = (overdue: boolean, dueDate: string) => {
        const m = cardOf(t, [fixtures.task("t1", { dueDate, overdue })]);
        const out = markup(card(m, "t1"));
        m.unmount();
        return out;
      };
      expect(html(true, "2000-01-01T00:00:00Z"), "overdue must be visible").not.toBe(html(false, "2000-01-01T00:00:00Z"));
      expect(html(true, "2999-01-01T00:00:00Z"), "the view must not recompute overdue from dueDate").not.toBe(html(false, "2999-01-01T00:00:00Z"));
    },
  },
  {
    id: "CT-board-12",
    title: "the title is a real link; a plain click opens the task once, a ctrl-click is left to the browser",
    slots: ["board.card"],
    run(t) {
      const m = cardOf(t, [fixtures.task("t1")]);
      const link = within(card(m, "t1")).getByRole("link", { name: /Task t1/ });
      expect(link.getAttribute("href") ?? "").toContain("t1");
      fireEvent.click(link);
      expect(calls(m, "openTask")).toHaveLength(1);
      expect(calls(m, "openTask")[0]![0]).toBe("t1");
      // The browser opens the tab: the click is not cancelled and the in-app handler does not run.
      expect(fireEvent.click(link, { ctrlKey: true })).toBe(true);
      expect(calls(m, "openTask")).toHaveLength(1);
    },
  },
  {
    id: "CT-board-13",
    title: "Enter on the title opens the editor, Enter commits through quickEdit; a viewer gets no editor",
    slots: ["board.card"],
    async run(t) {
      const m = cardOf(t, [fixtures.task("t1")]);
      const trigger = card(m, "t1").querySelector<HTMLElement>("[data-inline-edit=title]");
      expect(trigger, "the title must be wrapped in <InlineEdit field=\"title\">").not.toBeNull();
      fireEvent.keyDown(trigger!, { key: "Enter" });
      const input = within(m.container).getByLabelText(/title/i) as HTMLInputElement;
      fireEvent.change(input, { target: { value: "Renamed" } });
      fireEvent.keyDown(input, { key: "Enter" });
      await tick();
      expect(calls(m, "quickEdit")).toContainEqual(["t1", { field: "title", value: "Renamed" }]);
      m.unmount();
      const viewer = t.mount({ role: "viewer" });
      expect(card(viewer, "t1").querySelector("[data-inline-edit]")).toBeNull();
    },
  },
  {
    id: "CT-board-14",
    title: "a task.badge extension in the fixture draws its badge on the card",
    slots: ["board.card"],
    async run({ mount }) {
      const m = mount({ extensions: ["task.badge"] });
      const { harnessHost } = await import("../host");
      await waitFor(() => expect(m.container.querySelector(harnessHost().extensionSelector("task.badge"))).not.toBeNull());
    },
  },
  {
    id: "CT-board-15",
    title: "the drag preview (isOverlay) has no Draggable and no handlers",
    slots: ["board.card"],
    run(t) {
      const overlay = t.mount({ options: { overlay: true } });
      expect(overlay.container.querySelector("[data-mesh-draggable]")).toBeNull();
      expect(overlay.container.querySelector("[tabindex],[aria-roledescription]")).toBeNull();
    },
  },
  {
    id: "CT-board-16",
    title: "a refused move does not break the view; the card stays",
    slots: ["board.layout"],
    async run({ mount }) {
      const m = mount({ actions: { moveTask: () => Promise.reject(new Error("refused")) } });
      await pointerDrag(card(m, "t1"), 330);
      expect(calls(m, "moveTask")).toHaveLength(1);
      expect(card(m, "t1")).toBeTruthy();
      expect(zone(m, "backlog")!.querySelector('[data-mesh-draggable="task:t1"]')).not.toBeNull();
    },
  },
  {
    id: "CT-board-17",
    title: "the column count is totalCount, not the number of loaded cards",
    slots: ["board.column"],
    run({ mount }) {
      const m = mount({ data: withColumns(cols(["backlog"], [[fixtures.task("t1")]], [{ totalCount: 42, countLabel: "42" }])) as never });
      expect(visibleText(m.container)).toContain("42");
    },
  },
  {
    id: "CT-board-18",
    title: "on a phone the board is readable; with dnd.reason mobile_readonly there are no handles",
    slots: ["board.layout"],
    run({ mount }) {
      const m = mount({ isMobile: true, options: { dndReason: "mobile_readonly" } });
      expect(m.container.querySelectorAll("[data-mesh-draggable]")).toHaveLength(3);
      for (const el of Array.from(m.container.querySelectorAll("[data-mesh-draggable]"))) {
        expect(el.hasAttribute("aria-roledescription")).toBe(false);
        expect(el.hasAttribute("tabindex")).toBe(false);
      }
    },
  },
]);
