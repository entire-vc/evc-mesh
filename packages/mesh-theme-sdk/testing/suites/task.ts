// CT-row-01..08 and CT-view-01..09 (Mesh Doc "slots-task-nav-doc-auth" §1, §2).
// task.row: a row is `<tr>` or `[role=row]`, its cells `td` / `[role=cell]` / `[role=gridcell]`, in `columns` order
// after any leading cells of the row's own (the selection box).
// task.view: blocks are the core's stand-ins (`data-ct-standin="<id>"`, carrying `data-mesh-block="<id>"`). A view
// that draws several layouts in one DOM (phone and desktop, switched by CSS) marks each with `data-mesh-layout`;
// "exactly once" is then counted per layout.
import { act, fireEvent, waitFor, within } from "@testing-library/react";
import { expect } from "vitest";
import { harnessHost } from "../host";
import { button, calls, markup, tick, visibleText } from "../helpers";
import { fixtures } from "../fixtures";
import { defineTests } from "../suite";
import type { Mounted } from "../types";
import type { TaskViewBlockId } from "../../src/index";

const ROW = ["task.row"];
const VIEW = ["task.view"];

// -- task.row helpers ----------------------------------------------------------------------------------

function rowOf(m: Mounted): HTMLElement {
  const el = m.container.querySelector<HTMLElement>("tr,[role=row]");
  if (!el) throw new Error("task.row must draw a <tr> or an element with role=row");
  return el;
}

const cellsOf = (m: Mounted) =>
  Array.from(rowOf(m).querySelectorAll<HTMLElement>(":scope > td, :scope > th, :scope > [role=cell], :scope > [role=gridcell]"));

const cellWith = (m: Mounted, text: string) => cellsOf(m).findIndex((c) => visibleText(c).includes(text));

/** The cell of column `key` (cells follow `columns`, after the row's own leading cells). */
function columnCell(m: Mounted, key: string): HTMLElement {
  const cols = (m.data() as { columns: { key: string }[] }).columns;
  const cells = cellsOf(m);
  const i = cols.findIndex((c) => c.key === key);
  const cell = cells[cells.length - cols.length + i];
  if (i < 0 || !cell) throw new Error(`no cell for column ${key}`);
  return cell;
}

const EDITOR = "select, textarea, input:not([type=checkbox]):not([type=radio]), [contenteditable=true], [role=combobox], [role=listbox], [role=textbox]";

const editorIn = (root: Element) => root.querySelector<HTMLElement>(EDITOR);

const checkedOf = (box: HTMLElement) => box.getAttribute("aria-checked") ?? String((box as HTMLInputElement).checked);

const isPriorityCall = (c: unknown[], value?: string) =>
  c.some((a) => typeof a === "object" && a !== null && (a as { field?: string }).field === "priority" && (value === undefined || (a as { value?: string }).value === value));

// -- task.view helpers ---------------------------------------------------------------------------------

/** The layouts the view drew (one scope when it draws a single one). */
function scopes(m: Mounted): HTMLElement[] {
  const marked = Array.from(m.container.querySelectorAll<HTMLElement>("[data-mesh-layout]"));
  return marked.length > 0 ? marked : [m.container];
}

const standins = (root: ParentNode, id: string) => root.querySelectorAll(`[data-ct-standin="${id}"]`).length;

const TAB_NAME: Partial<Record<TaskViewBlockId, RegExp>> = { properties: /propert|details/i };

/** The control that brings block `id` forward (a tab), not one inside a block. */
function tabFor(scope: HTMLElement, id: TaskViewBlockId): HTMLElement | null {
  const name = TAB_NAME[id] ?? new RegExp(id, "i");
  return (
    Array.from(scope.querySelectorAll<HTMLElement>("button,[role=tab]")).find((b) => !b.closest("[data-ct-standin]") && name.test(visibleText(b))) ?? null
  );
}

/** How many times block `id` is on the screen of this layout, opening its tab first if it is behind one. */
async function reach(m: Mounted, scope: HTMLElement, id: TaskViewBlockId): Promise<number> {
  if (standins(scope, id) === 0) {
    const tab = tabFor(scope, id);
    if (tab) await m.user.click(tab);
  }
  return standins(scope, id);
}

const viewBlocks = (m: Mounted) => (m.data() as { blocks: TaskViewBlockId[] }).blocks;

defineTests([
  // ------------------------------------------------------------------ task.row
  {
    id: "CT-row-01",
    title: "cells follow `columns` order; empty values do not break the row",
    slots: ROW,
    run({ mount }) {
      const t = fixtures.task("t1", { dueDate: "2030-05-01T00:00:00Z", customFields: { sprint: 4242 } });
      const a = mount({ data: { task: t, columns: fixtures.rowColumns(["title", "status", "due_date", "cf:sprint"]) } });
      const [title, st, sprint] = [cellWith(a, "Task t1"), cellWith(a, "Todo"), cellWith(a, "4242")];
      expect([title, st, sprint].every((i) => i >= 0), "title, status and sprint values must be drawn").toBe(true);
      expect(title).toBeLessThan(st);
      expect(sprint - st, "due_date sits between status and cf:sprint").toBe(2);
      const width = cellsOf(a).length;
      a.unmount();
      const b = mount({ data: { task: t, columns: fixtures.rowColumns(["title", "cf:sprint", "due_date", "status"]) } });
      expect(cellWith(b, "Task t1")).toBeLessThan(cellWith(b, "4242"));
      expect(cellWith(b, "Todo") - cellWith(b, "4242")).toBe(2);
      b.unmount();
      const empty = mount({ data: { columns: fixtures.rowColumns(["title", "status", "due_date", "cf:sprint"]) } });
      expect(cellsOf(empty)).toHaveLength(width);
      expect(cellWith(empty, "Task t1")).toBeGreaterThanOrEqual(0);
    },
  },
  {
    id: "CT-row-02",
    title: "the title is a real link: a click opens the task, a ctrl-click opens a new tab",
    slots: ROW,
    run({ mount }) {
      const m = mount();
      const link = within(rowOf(m)).getByRole("link", { name: /Task t1/ });
      expect(link.getAttribute("href") ?? "").toContain("t1");
      fireEvent.click(link);
      expect(calls(m, "openTask")).toHaveLength(1);
      // A new tab: either the browser opens it from the href (the click is not cancelled, no in-app open) or the view
      // asks for it with openTask({ newTab: true }).
      const notCancelled = fireEvent.click(link, { ctrlKey: true });
      const added = calls(m, "openTask").slice(1);
      expect(added.every((c) => c.some((x) => (x as { newTab?: boolean } | undefined)?.newTab === true))).toBe(true);
      expect(notCancelled || added.length === 1).toBe(true);
    },
  },
  {
    id: "CT-row-03",
    title: "the selection checkbox toggles by click and Space; aria-checked follows `selected`",
    slots: ROW,
    async run({ mount }) {
      const m = mount();
      const box = within(rowOf(m)).getByRole("checkbox");
      expect(checkedOf(box)).toBe("false");
      await m.user.click(box);
      expect(calls(m, "toggleSelected")).toHaveLength(1);
      expect(checkedOf(within(rowOf(m)).getByRole("checkbox"))).toBe("true");
      const again = within(rowOf(m)).getByRole("checkbox");
      again.focus();
      expect(document.activeElement).toBe(again);
      await m.user.keyboard(" ");
      expect(calls(m, "toggleSelected")).toHaveLength(2);
      expect(checkedOf(within(rowOf(m)).getByRole("checkbox"))).toBe("false");
    },
  },
  {
    id: "CT-row-04",
    title: "viewer: no quickEdit / changeStatus in actions, no editor opens in any cell",
    slots: ROW,
    async run({ mount }) {
      const m = mount({ role: "viewer", data: { columns: fixtures.rowColumns(["title", "status", "priority", "due_date", "cf:sprint"]) } });
      expect(m.spies.quickEdit).toBeUndefined();
      expect(m.spies.changeStatus).toBeUndefined();
      for (let i = 0; i < cellsOf(m).length; i++) {
        const cell = cellsOf(m)[i]!;
        fireEvent.click(cell);
        fireEvent.doubleClick(cell);
        fireEvent.keyDown(cell, { key: "Enter" });
        await tick();
        expect(editorIn(rowOf(m)), `cell ${i} opened an editor for a viewer`).toBeNull();
      }
      expect(rowOf(m).querySelector("[data-inline-edit]")).toBeNull();
    },
  },
  {
    id: "CT-row-05",
    title: "the priority editor commits through quickEdit, by change and by Enter",
    slots: ROW,
    async run({ mount }) {
      const m = mount();
      const open = async (how: "click" | "enter") => {
        const cell = columnCell(m, "priority");
        if (how === "click") fireEvent.click(cell);
        else {
          cell.focus();
          fireEvent.keyDown(cell, { key: "Enter" });
        }
        await tick();
        const ed = editorIn(rowOf(m));
        expect(ed, `the priority cell opens an editor (${how})`).not.toBeNull();
        return ed!;
      };
      const ed = await open("click");
      if (ed instanceof HTMLSelectElement) fireEvent.change(ed, { target: { value: "high" } });
      else {
        fireEvent.click(ed);
        await m.user.click(await within(document.body).findByRole("option", { name: /high/i }));
      }
      await tick();
      expect(calls(m, "quickEdit").some((c) => isPriorityCall(c, "high"))).toBe(true);
      const before = calls(m, "quickEdit").length;
      const ed2 = await open("enter");
      fireEvent.keyDown(ed2, { key: "Enter" });
      await tick();
      expect(calls(m, "quickEdit").length).toBe(before + 1);
      const all = calls(m, "quickEdit");
      expect(isPriorityCall(all[all.length - 1]!)).toBe(true);
    },
  },
  {
    id: "CT-row-06",
    title: "the expand button reports aria-expanded and calls toggleExpanded; a nested row is indented",
    slots: ROW,
    async run({ mount }) {
      const m = mount({ data: { expandable: true, expanded: false } });
      const btn = () => rowOf(m).querySelector<HTMLElement>("[aria-expanded]");
      expect(btn(), "an expandable row needs a control with aria-expanded").not.toBeNull();
      expect(btn()!.getAttribute("aria-expanded")).toBe("false");
      await m.user.click(btn()!);
      expect(calls(m, "toggleExpanded")).toHaveLength(1);
      expect(btn()!.getAttribute("aria-expanded")).toBe("true");
      expect(calls(m, "openTask"), "the toggle is not a click on the row").toHaveLength(0);
      m.unmount();
      const draw = (depth: number) => {
        const r = mount({ data: { depth } });
        const out = { html: markup(rowOf(r)), title: cellWith(r, "Task t1") };
        r.unmount();
        return out;
      };
      const root = draw(0);
      const nested = draw(2);
      expect(nested.title).toBeGreaterThanOrEqual(0);
      expect(nested.html, "depth must change how the row is drawn (indent)").not.toBe(root.html);
    },
  },
  {
    id: "CT-row-07",
    title: "a realtime title change updates the row in place and keeps the selection",
    slots: ROW,
    async run({ mount }) {
      const m = mount();
      const row = rowOf(m);
      await m.user.click(within(row).getByRole("checkbox"));
      await m.emitRealtime({ type: "task.updated", data: { id: "t1", title: "Новый" }, at: "1" });
      await waitFor(() => expect(visibleText(rowOf(m))).toContain("Новый"));
      expect(rowOf(m)).toBe(row);
      expect(checkedOf(within(rowOf(m)).getByRole("checkbox"))).toBe("true");
    },
  },
  {
    id: "CT-row-08",
    title: "on a phone the row keeps its title and status, and opens the task",
    slots: ROW,
    run({ mount }) {
      const m = mount({ isMobile: true });
      expect(visibleText(rowOf(m))).toContain("Task t1");
      expect(visibleText(rowOf(m))).toContain("Todo");
      fireEvent.click(within(rowOf(m)).getByRole("link", { name: /Task t1/ }));
      expect(calls(m, "openTask")).toHaveLength(1);
    },
  },

  // ------------------------------------------------------------------ task.view
  {
    id: "CT-view-01",
    title: "every offered block is on the screen exactly once (per layout), reached through its tab if tabbed",
    slots: VIEW,
    async run({ mount }) {
      const m = mount();
      const offered = viewBlocks(m);
      for (const scope of scopes(m)) {
        for (const id of offered) {
          expect(await reach(m, scope, id), `block ${id} in ${scope.getAttribute("data-mesh-layout") ?? "the view"}`).toBe(1);
          for (const other of offered) expect(standins(scope, other), `block ${other} drawn twice`).toBeLessThanOrEqual(1);
        }
      }
      // A block the core did not offer is not drawn.
      expect(standins(m.container, "vcs")).toBe(0);
    },
  },
  {
    id: "CT-view-02",
    title: "hidden_blocks naming header and description does not hide them",
    slots: VIEW,
    async run({ mount }) {
      const m = mount({ themeSettings: { hidden_blocks: ["header", "description"] } });
      for (const scope of scopes(m)) {
        expect(await reach(m, scope, "header")).toBe(1);
        expect(await reach(m, scope, "description")).toBe(1);
      }
    },
  },
  {
    id: "CT-view-03",
    title: "viewer: no edit actions, no editors of the view's own; the comments block stays",
    slots: VIEW,
    async run({ mount }) {
      const m = mount({ role: "viewer" });
      for (const name of ["quickEdit", "changeStatus", "assign"]) expect(m.spies[name], name).toBeUndefined();
      const own = Array.from(m.container.querySelectorAll(EDITOR + ", [data-inline-edit]")).filter((e) => !e.closest("[data-ct-standin]"));
      expect(own.map((e) => e.outerHTML.slice(0, 80))).toEqual([]);
      for (const scope of scopes(m)) expect(await reach(m, scope, "comments")).toBe(1);
      expect(within(m.container).getAllByLabelText("Comment draft").length).toBeGreaterThan(0);
    },
  },
  {
    id: "CT-view-04",
    title: "with task.panel and task.actions extensions both insertion points are in the DOM",
    slots: VIEW,
    async run({ mount }) {
      const m = mount({ extensions: ["task.panel", "task.actions"] });
      const host = harnessHost();
      await waitFor(() => expect(m.container.querySelector(host.extensionSelector("task.actions")), "task.actions (top bar)").not.toBeNull());
      await waitFor(() => expect(m.container.querySelector(host.extensionSelector("task.panel")), "task.panel (extension panel)").not.toBeNull());
      m.unmount();
      // Without extensions nothing of theirs is drawn.
      const bare = mount();
      await tick();
      expect(bare.container.querySelector(host.extensionSelector("task.panel"))).toBeNull();
      expect(bare.container.querySelector(host.extensionSelector("task.actions"))).toBeNull();
    },
  },
  {
    id: "CT-view-05",
    title: "loading is a placeholder, error is a message with a working retry, refreshing keeps the content",
    slots: VIEW,
    async run({ mount }) {
      const loading = mount({ options: { state: "loading" } });
      expect(loading.container.children.length).toBeGreaterThan(0);
      expect(standins(loading.container, "description"), "no task content while loading").toBe(0);
      loading.unmount();
      const error = mount({ options: { state: "error" } });
      expect(visibleText(error.container)).toMatch(/could not load the task/i);
      const retry = button(error.container, /retry|повтор|try again/i);
      expect(retry, "the error state needs a retry").not.toBeNull();
      await error.user.click(retry!);
      expect(calls(error, "retry")).toHaveLength(1);
      error.unmount();
      const refreshing = mount({ options: { state: "refreshing" } });
      for (const scope of scopes(refreshing)) expect(await reach(refreshing, scope, "description")).toBe(1);
    },
  },
  {
    id: "CT-view-06",
    title: "slide-over: close() is reachable by a button; Escape is left to the core",
    slots: VIEW,
    async run({ mount }) {
      const m = mount({ data: { presentation: "slide-over" } });
      const seen: Array<{ prevented: boolean }> = [];
      const listen = (e: KeyboardEvent) => {
        if (e.key === "Escape") seen.push({ prevented: e.defaultPrevented });
      };
      document.addEventListener("keydown", listen);
      try {
        fireEvent.keyDown(document.body, { key: "Escape" });
        for (const scope of scopes(m)) await reach(m, scope, "comments");
        fireEvent.keyDown(within(m.container).getAllByLabelText("Comment draft")[0]!, { key: "Escape" });
        expect(seen, "Escape must reach the core, not be swallowed by the view").toHaveLength(2);
        expect(seen.every((s) => !s.prevented)).toBe(true);
        expect(calls(m, "close"), "the view does not close the panel on Escape itself").toHaveLength(0);
      } finally {
        document.removeEventListener("keydown", listen);
      }
      await m.user.click(within(m.container).getAllByRole("button", { name: /close/i })[0]!);
      expect(calls(m, "close")).toHaveLength(1);
    },
  },
  {
    id: "CT-view-07",
    title: "a realtime status change keeps the comment being typed",
    slots: VIEW,
    async run({ mount }) {
      const m = mount();
      for (const scope of scopes(m)) await reach(m, scope, "comments");
      const drafts = within(m.container).getAllByLabelText("Comment draft") as HTMLTextAreaElement[];
      for (const d of drafts) fireEvent.change(d, { target: { value: "half a thought" } });
      await m.emitRealtime({ type: "task.updated", data: { id: "t1", statusId: "s2", title: "Task t1 (moved)" }, at: "1" });
      expect((m.data() as { status: { id: string } }).status.id).toBe("s2");
      const after = within(m.container).getAllByLabelText("Comment draft") as HTMLTextAreaElement[];
      expect(after).toHaveLength(drafts.length);
      after.forEach((d, i) => {
        expect(d, "the comment block was remounted").toBe(drafts[i]);
        expect(d.value).toBe("half a thought");
      });
    },
  },
  {
    id: "CT-view-08",
    title: "a click on a subtask opens it through openTask(subtaskId)",
    slots: VIEW,
    async run({ mount }) {
      const m = mount();
      const scope = scopes(m)[0]!;
      expect(await reach(m, scope, "subtasks")).toBe(1);
      await m.user.click(within(scope).getByRole("button", { name: "Subtask sub-1" }));
      expect(calls(m, "openTask").map((c) => c[0])).toContain("sub-1");
    },
  },
  {
    id: "CT-view-09",
    title: "tabs: switched from the keyboard; a block the core asks for comes to the front",
    slots: VIEW,
    async run({ mount }) {
      const m = mount({ themeSettings: { layout_variant: "tabs" } });
      // A layout that tabs blocks: the one where artifacts is behind a tab.
      const scope = scopes(m).find((s) => standins(s, "artifacts") === 0 && tabFor(s, "artifacts")) ?? null;
      if (!scope) {
        // Every block is on the screen at once: nothing to switch.
        for (const s of scopes(m)) expect(standins(s, "artifacts")).toBe(1);
        return;
      }
      const tab = tabFor(scope, "artifacts")!;
      if (tab.closest("[role=tablist]")) {
        // ARIA tabs: arrows move the selection.
        const list = tab.closest("[role=tablist]")!;
        const current = list.querySelector<HTMLElement>("[aria-selected=true]") ?? list.querySelector<HTMLElement>("[role=tab]")!;
        current.focus();
        for (let i = 0; i < 10 && standins(scope, "artifacts") === 0; i++) await m.user.keyboard("{ArrowRight}");
      } else {
        // Tabs as buttons: focus and activate from the keyboard.
        tab.focus();
        expect(document.activeElement).toBe(tab);
        await m.user.keyboard("{Enter}");
      }
      expect(standins(scope, "artifacts")).toBe(1);
      await m.update({ requestedBlock: { block: "comments", nonce: 1 } } as never);
      await act(async () => {});
      expect(standins(scope, "comments")).toBe(1);
    },
  },
]);
