import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  boardColumnPropsEqual,
  holderMapsEqual,
  sortableTaskCardPropsEqual,
  tasksArrayEqual,
} from "@/pages/board";
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

describe("board.tsx memoization comparators (perf·Б3 board.drag)", () => {
  it("sortableTaskCardPropsEqual: true when every field is the SAME reference", () => {
    const t = task();
    const onClick = () => {};
    const onEditClick = () => {};
    const props = { task: t, columnId: "c-1", statusCategory: "todo" as const, onClick, onEditClick, checkedOutByName: undefined };
    expect(sortableTaskCardPropsEqual(props, { ...props })).toBe(true);
  });

  it("sortableTaskCardPropsEqual: false when the task object was replaced (the moved card itself)", () => {
    const onClick = () => {};
    const onEditClick = () => {};
    const base = { task: task(), columnId: "c-1", statusCategory: "todo" as const, onClick, onEditClick, checkedOutByName: undefined };
    const moved = { ...base, task: task({ status_id: "s-2" }) };
    expect(sortableTaskCardPropsEqual(base, moved)).toBe(false);
  });

  it("sortableTaskCardPropsEqual: false when onClick/onEditClick are fresh per-render closures", () => {
    const t = task();
    const props = { task: t, columnId: "c-1", statusCategory: "todo" as const, checkedOutByName: undefined };
    // Exactly the bug this props contract exists to prevent: a NEW arrow
    // function per render (e.g. `onClick={() => onTaskClick(task)}` inlined
    // in BoardColumn) defeats the memo below even though nothing else moved.
    expect(
      sortableTaskCardPropsEqual(
        { ...props, onClick: () => {}, onEditClick: () => {} },
        { ...props, onClick: () => {}, onEditClick: () => {} },
      ),
    ).toBe(false);
  });

  it("sortableTaskCardPropsEqual: kill switch off always reports unequal", () => {
    window.__MESH_FLAGS__ = { boardCardMemo: false };
    const t = task();
    const onClick = () => {};
    const onEditClick = () => {};
    const props = { task: t, columnId: "c-1", statusCategory: "todo" as const, onClick, onEditClick, checkedOutByName: undefined };
    expect(sortableTaskCardPropsEqual(props, { ...props })).toBe(false);
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

  it("boardColumnPropsEqual: content-equal tasks array in a fresh array reference still counts as equal", () => {
    const a = task({ id: "a" });
    const col = { id: "c-1", title: "Todo", color: "#000" };
    const onAddTask = () => {};
    const onTaskClick = () => {};
    const onTaskEdit = () => {};
    const holderNameById = new Map<string, string>();
    const prev = { col, tasks: [a], dndEnabled: true, onAddTask, onTaskClick, onTaskEdit, holderNameById };
    // tasksByStatus/tasksByColumn is rebuilt with a fresh array on every
    // moveTask (stores/task.ts groupByStatus) — this is the case an
    // unrelated column hits on every drag, and it must stay memoized.
    const next = { ...prev, tasks: [a] };
    expect(boardColumnPropsEqual(prev, next)).toBe(true);
  });

  it("boardColumnPropsEqual: false when the column's own content changed", () => {
    const a = task({ id: "a" });
    const col = { id: "c-1", title: "Todo", color: "#000" };
    const onAddTask = () => {};
    const onTaskClick = () => {};
    const onTaskEdit = () => {};
    const holderNameById = new Map<string, string>();
    const prev = { col, tasks: [a], dndEnabled: true, onAddTask, onTaskClick, onTaskEdit, holderNameById };
    expect(boardColumnPropsEqual(prev, { ...prev, tasks: [a, task({ id: "b" })] })).toBe(false);
    expect(boardColumnPropsEqual(prev, { ...prev, col: { ...col } })).toBe(false);
  });

  it("boardColumnPropsEqual: kill switch off always reports unequal", () => {
    window.__MESH_FLAGS__ = { boardCardMemo: false };
    const a = task({ id: "a" });
    const col = { id: "c-1", title: "Todo", color: "#000" };
    const prev = {
      col,
      tasks: [a],
      dndEnabled: true,
      onAddTask: () => {},
      onTaskClick: () => {},
      onTaskEdit: () => {},
      holderNameById: new Map<string, string>(),
    };
    expect(boardColumnPropsEqual(prev, { ...prev, tasks: [a] })).toBe(false);
  });
});
