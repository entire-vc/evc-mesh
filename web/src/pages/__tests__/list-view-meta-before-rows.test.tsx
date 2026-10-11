import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

// The list's rows are built from statuses (grouping) and custom-field
// definitions (columns). Whichever GET answers last used to rebuild the table,
// so the DOM cost of opening the list depended on response order. The mock holds
// statuses and custom fields back while the task list answers at once.
let releaseStatuses: (v: unknown) => void = () => {};
let releaseFields: (v: unknown) => void = () => {};
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (/\/projects\/p1\/tasks$/.test(path)) {
        return { items: TASKS, total_count: TASKS.length, page: 1, per_page: 50, has_more: false };
      }
      if (/\/projects\/p1\/statuses$/.test(path)) return new Promise((r) => (releaseStatuses = r));
      if (/custom-fields/.test(path)) return new Promise((r) => (releaseFields = r));
      return { items: [] };
    }),
  };
});

import { ListViewPage } from "@/pages/list-view";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import type { Task, TaskStatus } from "@/types";

const mkStatus = (id: string, position: number): TaskStatus => ({
  id, project_id: "p1", name: id, slug: id, color: "#000", position,
  category: "todo", is_default: position === 0, auto_transition: {},
});
const STATUSES = [mkStatus("s1", 0)];
const TASKS = [
  { id: "t1", project_id: "p1", title: "Row one", status_id: "s1", position: 1, priority: "medium", labels: [] },
] as unknown as Task[];

beforeEach(() => {
  useProjectStore.setState({ currentProject: { id: "p1", slug: "proj", name: "P" } as never, statuses: [] });
  useTaskStore.setState({ tasks: [], tasksByStatus: {}, isLoading: false });
});

describe("list view: rows wait for statuses and custom fields", () => {
  it("task list answered first → no rows until both lookups answer, then rows once", async () => {
    render(
      <MemoryRouter initialEntries={["/ws/proj/list"]}>
        <Routes><Route path="/:wsSlug/:projectSlug/list" element={<ListViewPage />} /></Routes>
      </MemoryRouter>,
    );
    await act(async () => { await new Promise((r) => setTimeout(r, 30)); });
    expect(useTaskStore.getState().tasks).toHaveLength(1);
    expect(screen.queryByText("Row one")).toBeNull();

    await act(async () => { releaseStatuses(STATUSES); });
    expect(screen.queryByText("Row one")).toBeNull();

    await act(async () => { releaseFields([]); });
    expect(await screen.findByText("Row one")).toBeTruthy();
  });
});
