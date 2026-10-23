// Acceptance (c): a different layout (columns stacked vertically) drawn through the real BoardPage
// container; dragging a card from one column to the next changes its status.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

const apiCalls: { path: string; opts?: { method?: string; body?: unknown } }[] = [];
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string, opts?: { method?: string; body?: unknown }) => {
      apiCalls.push({ path, opts });
      if (/\/projects\/p1\/tasks$/.test(path)) return { items: TASKS, total_count: TASKS.length, page: 1, per_page: 200, has_more: false };
      if (/\/projects\/p1\/statuses$/.test(path)) return STATUSES;
      if (/\/move$/.test(path)) return { ...TASKS[0], status_id: "s2" };
      if (/custom-fields/.test(path)) return [];
      return { items: [] };
    }),
  };
});

import { BoardPage } from "@/pages/board";
import { registerSlotViewOverride, setThemeSettings } from "../registry";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import type { BoardLayoutView } from "../types";
import type { Task, TaskStatus } from "@/types";

const mkStatus = (id: string, position: number): TaskStatus => ({
  id, project_id: "p1", name: id, slug: id, color: "#000", position, category: "todo", is_default: position === 0, auto_transition: {},
});
const STATUSES = [mkStatus("s1", 0), mkStatus("s2", 1)];
const mkTask = (id: string, status = "s1"): Task =>
  ({ id, project_id: "p1", title: `T ${id}`, status_id: status, position: 1, priority: "medium", labels: [] }) as unknown as Task;
const TASKS = [{ ...mkTask("t1"), labels: ["x-label"] }, mkTask("t2", "s2")];

// Columns one under another: the second column sits below the first, so "next" is ArrowDown.
const VerticalLayout: BoardLayoutView = ({ data, actions, ctx, themeSettings }) => {
  const { Column } = data.components;
  const cols = data.columns.state === "ready" || data.columns.state === "refreshing" ? data.columns.value : [];
  return (
    <div data-testid="vertical-layout">
      {cols.map((column, index) => (
        <Column
          key={column.id}
          data={{ column, index, total: cols.length, dnd: data.dnd, fields: data.availableFields.filter((f) => data.cardFields.includes(f.key)), components: { Card: data.components.Card } }}
          actions={{ loadMore: () => actions.loadMore(column.id), openTask: actions.openTask, quickEdit: actions.quickEdit, moveTask: (id, pos) => actions.moveTask(id, column.id, pos) }}
          themeSettings={themeSettings}
          ctx={ctx}
        />
      ))}
    </div>
  );
};

// jsdom has no layout: columns stacked 400px apart, cards 60px apart inside a column.
function stubVerticalLayout() {
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const el = this as HTMLElement;
    const zone = el.closest("[data-column-id]") as HTMLElement | null;
    const ci = zone ? Array.from(document.querySelectorAll("[data-column-id]")).indexOf(zone) : 0;
    const isZone = el.hasAttribute("data-column-id");
    const row = isZone ? 0 : Array.from(zone?.querySelectorAll("[data-task-id]") ?? []).indexOf(el.closest("[data-task-id]") as Element);
    const y = ci * 400 + (isZone ? 0 : 20 + Math.max(row, 0) * 60);
    return { x: 0, y, left: 0, top: y, width: 200, height: isZone ? 300 : 50, right: 200, bottom: y + (isZone ? 300 : 50), toJSON() {} } as DOMRect;
  });
}

let unregister: () => void;
beforeEach(() => {
  apiCalls.length = 0;
  stubVerticalLayout();
  unregister = registerSlotViewOverride("board.layout", VerticalLayout);
  useProjectStore.setState({ currentProject: { id: "p1", slug: "proj", name: "P" } as never, statuses: STATUSES });
  useTaskStore.setState({ tasks: [], tasksByStatus: {} });
});
afterEach(() => { setThemeSettings({}); unregister(); cleanup(); vi.restoreAllMocks(); });

describe("stub layout through the BoardPage container", () => {
  it("draws through the stub and a keyboard drag to the next column posts the new status", async () => {
    render(
      <MemoryRouter initialEntries={["/ws/proj"]}>
        <Routes><Route path="/:wsSlug/:projectSlug" element={<BoardPage />} /></Routes>
      </MemoryRouter>,
    );
    await screen.findByText("T t1");
    expect(screen.getByTestId("vertical-layout")).toBeTruthy();

    const handle = document.querySelector('[data-testid="task-card"][data-task-id="t1"]') as HTMLElement;
    handle.focus();
    const tick = () => act(async () => { await new Promise((r) => setTimeout(r, 30)); });
    fireEvent.keyDown(handle, { code: "Space", key: " " });
    await tick();
    fireEvent.keyDown(document, { code: "ArrowDown", key: "ArrowDown" });
    await tick();
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await tick();

    await waitFor(() => expect(apiCalls.some((c) => /\/tasks\/t1\/move$/.test(c.path))).toBe(true));
    const move = apiCalls.find((c) => /\/tasks\/t1\/move$/.test(c.path))!;
    expect(move.opts?.method).toBe("POST");
    expect(JSON.stringify(move.opts?.body)).toContain("s2");
  });

  it("the card_fields theme setting changes what the live board draws on its cards", async () => {
    render(
      <MemoryRouter initialEntries={["/ws/proj"]}>
        <Routes><Route path="/:wsSlug/:projectSlug" element={<BoardPage />} /></Routes>
      </MemoryRouter>,
    );
    await screen.findByText("T t1");
    expect(screen.getByText("x-label")).toBeTruthy(); // default fields include the labels
    act(() => setThemeSettings({ card_fields: ["priority"] }));
    await waitFor(() => expect(screen.queryByText("x-label")).toBeNull());
    expect(screen.getByText("T t1")).toBeTruthy();
    act(() => setThemeSettings({}));
    await waitFor(() => expect(screen.getByText("x-label")).toBeTruthy());
  });

  it("a board.card override registered after the board mounted is picked up, and dropped again", async () => {
    render(
      <MemoryRouter initialEntries={["/ws/proj"]}>
        <Routes><Route path="/:wsSlug/:projectSlug" element={<BoardPage />} /></Routes>
      </MemoryRouter>,
    );
    await screen.findByText("T t1");
    expect(screen.queryByTestId("late-card")).toBeNull();
    let off = () => {};
    act(() => { off = registerSlotViewOverride("board.card", () => <div data-testid="late-card" />); });
    await waitFor(() => expect(screen.queryAllByTestId("late-card").length).toBeGreaterThan(0));
    act(() => off());
    await waitFor(() => expect(screen.queryByTestId("late-card")).toBeNull());
    expect(screen.getByText("T t1")).toBeTruthy();
  });

  it("a theme layout that throws falls back to the mesh-default layout instead of taking the board down", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const off = registerSlotViewOverride("board.layout", () => { throw new Error("broken theme"); });
    try {
      render(
        <MemoryRouter initialEntries={["/ws/proj"]}>
          <Routes><Route path="/:wsSlug/:projectSlug" element={<BoardPage />} /></Routes>
        </MemoryRouter>,
      );
      await screen.findByText("T t1");
      expect(screen.queryByTestId("vertical-layout")).toBeNull();
    } finally {
      off();
    }
  });
});
