import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { holderMapsEqual, tasksArrayEqual } from "@/pages/board";
import { cardPropsEqual, columnPropsEqual } from "@/core/slots/board-host";
import { toTaskData, stabilizeColumns } from "@/core/slots/board-data";
import type { BoardCardView, BoardColumnData } from "@/core/slots/types";
import type { Task } from "@/types";

// A static top-level import (not per-test `await import(...)`) — board.tsx is
// a big module with a lot of its own dependencies, and re-resolving that
// import from inside every `it` was slow enough under a loaded CI runner to
// blow past vitest's 5s default test timeout even though nothing was wrong;
// importing it once here costs that time exactly once, at file load.
//
// This does mean board.tsx's module-scope `void loadRuntimeFlags()` fires
// with whatever `fetch` exists at that moment (real or already mocked by an
// earlier file) — harmless either way, since fetchConfigFlags() in
// runtime-flags.ts already swallows any failure and both comparators below
// read window.__MESH_FLAGS__ fresh on every call, never that resolved value.
beforeEach(() => {
  delete (window as unknown as { __MESH_FLAGS__?: unknown }).__MESH_FLAGS__;
});

afterEach(() => {
  vi.restoreAllMocks();
});

function task(overrides: Partial<Task> = {}): Task {
  return {
    id: "t-1",
    project_id: "p-1",
    title: "A task",
    status_id: "s-1",
    priority: "medium",
    position: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  } as Task;
}

const ctx = { slot: "board.layout", contractVersion: "1.0.0", locale: "en", mode: "light" as const, isMobile: false, workspace: null };
const settings = {};
const fields: never[] = [];
const Card: BoardCardView = () => null;

function cardProps(t: Task, over: Record<string, unknown> = {}) {
  return {
    data: {
      task: toTaskData(t, "todo"),
      column: { id: "c-1", kind: "status" as const, statusCategory: "todo" as const },
      fields,
      selected: false,
      isOverlay: false,
      ...over,
    },
    actions: { openTask: () => {} },
    themeSettings: settings,
    ctx,
  };
}

describe("board slot memo comparators (perf·Б3 board.drag)", () => {
  it("toTaskData keeps its result's identity for the same task object, replaces it for a new one", () => {
    const t = task();
    expect(toTaskData(t, "todo")).toBe(toTaskData(t, "todo"));
    expect(toTaskData(task(), "todo")).not.toBe(toTaskData(t, "todo"));
  });

  it("cardPropsEqual: fresh data/actions objects around the same task are equal", () => {
    const t = task();
    expect(cardPropsEqual(cardProps(t), cardProps(t))).toBe(true);
  });

  it("cardPropsEqual: false when the task object was replaced (the moved card itself)", () => {
    expect(cardPropsEqual(cardProps(task()), cardProps(task({ status_id: "s-2" })))).toBe(false);
  });

  it("cardPropsEqual: false when gate status, fields or overlay flag change", () => {
    const t = task();
    expect(cardPropsEqual(cardProps(t), cardProps(t, { gateStatusName: "Review" }))).toBe(false);
    expect(cardPropsEqual(cardProps(t), cardProps(t, { fields: [] }))).toBe(false);
    expect(cardPropsEqual(cardProps(t), cardProps(t, { isOverlay: true }))).toBe(false);
  });

  it("cardPropsEqual: kill switch off always reports unequal", () => {
    window.__MESH_FLAGS__ = { boardCardMemo: false };
    const t = task();
    expect(cardPropsEqual(cardProps(t), cardProps(t))).toBe(false);
  });

  it("tasksArrayEqual: true for the same array, and for a different array with the same task references in order", () => {
    const a = task({ id: "a" });
    const b = task({ id: "b" });
    expect(tasksArrayEqual([a, b], [a, b])).toBe(true);
    const arr = [a, b];
    expect(tasksArrayEqual(arr, arr)).toBe(true);
  });

  it("tasksArrayEqual: false when a task was replaced, reordered, added, or removed", () => {
    const a = task({ id: "a" });
    const b = task({ id: "b" });
    expect(tasksArrayEqual([a, b], [task({ id: "a" }), b])).toBe(false); // replaced
    expect(tasksArrayEqual([a, b], [b, a])).toBe(false); // reordered
    expect(tasksArrayEqual([a, b], [a, b, task({ id: "c" })])).toBe(false); // added
    expect(tasksArrayEqual([a, b], [a])).toBe(false); // removed
  });

  it("holderMapsEqual: true for same content in two different Map instances, false otherwise", () => {
    const m1 = new Map([["u-1", "Alice"], ["u-2", "Bob"]]);
    const m2 = new Map([["u-1", "Alice"], ["u-2", "Bob"]]);
    expect(holderMapsEqual(m1, m2)).toBe(true);
    expect(holderMapsEqual(m1, new Map([["u-1", "Alice"]]))).toBe(false);
    expect(holderMapsEqual(m1, new Map([["u-1", "Someone else"], ["u-2", "Bob"]]))).toBe(false);
  });

  const column = (over: Partial<BoardColumnData> = {}): BoardColumnData => ({
    id: "c-1", kind: "status", title: "Todo", color: "#000", tasks: [], totalCount: 0, countLabel: "0",
    wipLimit: null, isClosed: false, hasMore: false, loadingMore: false, ...over,
  });
  const colProps = (c: BoardColumnData) => ({
    data: { column: c, index: 0, total: 1, dnd: { enabled: true, nonDroppableColumnIds: [] }, fields, components: { Card } },
    actions: { loadMore: async () => {}, openTask: () => {}, moveTask: async () => {} },
    themeSettings: settings,
    ctx,
  });

  it("stabilizeColumns: a rebuilt column with the same content keeps its object; a changed one does not", () => {
    const a = toTaskData(task({ id: "a" }), "todo");
    const first = stabilizeColumns(new Map(), [column({ tasks: [a] })]);
    const again = stabilizeColumns(first.cache, [column({ tasks: [a] })]);
    expect(again.columns[0]).toBe(first.columns[0]);
    const b = toTaskData(task({ id: "b" }), "todo");
    const changed = stabilizeColumns(first.cache, [column({ tasks: [a, b] })]);
    expect(changed.columns[0]).not.toBe(first.columns[0]);
  });

  it("columnPropsEqual: same column object is equal even with fresh actions; a new column object is not", () => {
    const c = column();
    expect(columnPropsEqual(colProps(c), colProps(c))).toBe(true);
    expect(columnPropsEqual(colProps(c), colProps(column()))).toBe(false);
  });

  it("columnPropsEqual: kill switch off always reports unequal", () => {
    window.__MESH_FLAGS__ = { boardCardMemo: false };
    const c = column();
    expect(columnPropsEqual(colProps(c), colProps(c))).toBe(false);
  });
});
