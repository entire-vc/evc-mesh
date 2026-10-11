import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

const taskListGets = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (/\/projects\/p1\/tasks$/.test(path)) {
        taskListGets();
        return { items: TASKS, total_count: TASKS.length, page: 1, per_page: 200, has_more: false };
      }
      if (/\/projects\/p1\/statuses$/.test(path)) return STATUSES;
      if (/custom-fields/.test(path)) return [];
      return { items: [] };
    }),
  };
});

import { BoardPage } from "@/pages/board";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useWebSocketStore } from "@/stores/websocket";
import type { Task, TaskStatus, WSMessage } from "@/types";

const mkStatus = (id: string, position: number): TaskStatus => ({
  id, project_id: "p1", name: id, slug: id, color: "#000", position,
  category: "todo", is_default: position === 0, auto_transition: {},
});
const STATUSES = [mkStatus("s1", 0), mkStatus("s2", 1)];
const mkTask = (id: string): Task =>
  ({ id, project_id: "p1", title: `T ${id}`, status_id: "s1", position: 1, priority: "medium", labels: [] }) as unknown as Task;
const TASKS = [mkTask("t1"), mkTask("t2")];

const wsEvent = (n: number): WSMessage => ({
  type: "status_change", channel: "project:p1", data: { n }, timestamp: String(n),
});

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  taskListGets.mockClear();
  useProjectStore.setState({
    currentProject: { id: "p1", slug: "proj", name: "P" } as never,
    statuses: STATUSES,
  });
  useTaskStore.setState({ tasks: [], tasksByStatus: {} });
});
afterEach(() => vi.useRealTimers());

describe("board: WS refetch is held while a card is being dragged", () => {
  it("0 task-list GETs during drag, exactly 1 after drop", async () => {
    render(
      <MemoryRouter initialEntries={["/ws/proj"]}>
        <Routes><Route path="/:wsSlug/:projectSlug" element={<BoardPage />} /></Routes>
      </MemoryRouter>,
    );
    const card = await screen.findByText("T t1");
    const handle = card.closest("[data-testid=task-card]") as HTMLElement;
    await act(async () => { await vi.advanceTimersByTimeAsync(50); });
    taskListGets.mockClear();

    fireEvent.pointerDown(handle, { button: 0, isPrimary: true, pointerId: 1, clientX: 10, clientY: 10 });
    for (let i = 1; i <= 4; i++) {
      fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: 10 + i * 20, clientY: 10 });
    }
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    expect(document.querySelector("[aria-roledescription=sortable][aria-pressed=true], [aria-pressed=true]")).not.toBeNull();

    for (const n of [1, 2]) {
      act(() => { useWebSocketStore.setState({ lastEvent: wsEvent(n) }); });
      await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    }
    expect(taskListGets).toHaveBeenCalledTimes(0);

    fireEvent.keyDown(document, { key: "Escape", code: "Escape" });
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(taskListGets).toHaveBeenCalledTimes(1);
  });

  it("pointer held on a card before dnd-kit activates: 0 GETs, exactly 1 after release", async () => {
    render(
      <MemoryRouter initialEntries={["/ws/proj"]}>
        <Routes><Route path="/:wsSlug/:projectSlug" element={<BoardPage />} /></Routes>
      </MemoryRouter>,
    );
    const handle = (await screen.findByText("T t1")).closest("[data-testid=task-card]") as HTMLElement;
    await act(async () => { await vi.advanceTimersByTimeAsync(50); });
    taskListGets.mockClear();

    fireEvent.pointerDown(handle, { button: 0, isPrimary: true, pointerId: 1, clientX: 10, clientY: 10 });
    for (const n of [1, 2]) {
      act(() => { useWebSocketStore.setState({ lastEvent: wsEvent(n) }); });
      await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    }
    expect(taskListGets).toHaveBeenCalledTimes(0);

    fireEvent.pointerUp(document, { isPrimary: true, pointerId: 1, clientX: 10, clientY: 10 });
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(taskListGets).toHaveBeenCalledTimes(1);
  });
});
