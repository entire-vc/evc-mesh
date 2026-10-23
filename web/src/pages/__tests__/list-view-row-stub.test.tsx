import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

// A row view that lays the task out as a card (no <td> cells, no editors of its own) drives the same
// list container: selection, bulk bar, status change and the permission gate live in the container,
// so nothing of them may depend on the mesh-default row.
const apiMock = vi.fn();
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: (...a: unknown[]) => apiMock(...a) }));

import { ListViewPage } from "@/pages/list-view";
import { registerSlotViewOverride } from "@/core/slots/registry";
import type { TaskRowView } from "@/core/slots/types";
import { useMemberStore } from "@/stores/member";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Task, TaskStatus } from "@/types";

const STATUSES = [
  { id: "s1", project_id: "p1", name: "Todo", slug: "todo", color: "#000", position: 0, category: "todo", is_default: true, auto_transition: {} },
  { id: "s2", project_id: "p1", name: "Doing", slug: "doing", color: "#00f", position: 1, category: "in_progress", is_default: false, auto_transition: {} },
] as TaskStatus[];
const base = { project_id: "p1", status_id: "s1", position: 1, priority: "medium", labels: [] };
const TASKS = [
  { ...base, id: "t1", title: "First" },
  { ...base, id: "t2", title: "Second" },
] as unknown as Task[];

// Vertical card: everything it shows and does comes from props.
const StubRow: TaskRowView = ({ data, actions }) => (
  <tr data-stub-row={data.task.id}>
    <td>
      <strong>{data.task.title}</strong>
      <button onClick={() => actions.toggleSelected()}>{`pick ${data.task.id}`}</button>
      <button onClick={() => actions.openTask()}>{`open ${data.task.id}`}</button>
      {actions.changeStatus && <button onClick={() => void actions.changeStatus?.("s2")}>{`start ${data.task.id}`}</button>}
      {actions.quickEdit && <button onClick={() => void actions.quickEdit?.({ field: "priority", value: "high" })}>{`raise ${data.task.id}`}</button>}
    </td>
  </tr>
);

function renderList() {
  return render(
    <MemoryRouter initialEntries={["/ws/proj/list"]}>
      <Routes>
        <Route path="/:wsSlug/:projectSlug/list" element={<ListViewPage />} />
        <Route path="/w/:wsSlug/p/:projectSlug/t/:taskId" element={<div>task page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

const body = (o: { body: unknown }) => (typeof o.body === "string" ? JSON.parse(o.body) : o.body);

let dispose: () => void;
beforeEach(() => {
  apiMock.mockReset();
  apiMock.mockImplementation(async (path: string, opts?: { method?: string }) => {
    if (/\/projects\/p1\/tasks$/.test(path)) return { items: TASKS, total_count: 2, page: 1, per_page: 50, has_more: false };
    if (/\/projects\/p1\/statuses$/.test(path)) return STATUSES;
    if (opts?.method === "PATCH") return { ...TASKS[0], status_id: "s2" };
    if (/custom-fields/.test(path)) return [];
    return { items: [], extensions: [] };
  });
  useMemberStore.setState({ myRole: "member" });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1", slug: "ws", name: "W" } as never });
  useProjectStore.setState({ currentProject: { id: "p1", slug: "proj", name: "P" } as never, statuses: [] });
  useTaskStore.setState({ tasks: [], tasksByStatus: {}, isLoading: false });
  dispose = registerSlotViewOverride("task.row", StubRow);
});
afterEach(() => {
  dispose();
  cleanup();
});

describe("list container with a stub task.row view", () => {
  it("renders the stub for every task and selection drives the bulk bar", async () => {
    renderList();
    expect(await screen.findByText("First")).toBeTruthy();
    expect(document.querySelectorAll("[data-stub-row]")).toHaveLength(2);
    expect(screen.queryByText(/tasks? selected/)).toBeNull();
    fireEvent.click(screen.getByText("pick t1"));
    fireEvent.click(screen.getByText("pick t2"));
    expect(await screen.findByText("2 tasks selected")).toBeTruthy();
  });

  it("changeStatus from the view saves the status through the container", async () => {
    renderList();
    fireEvent.click(await screen.findByText("start t1"));
    await waitFor(() => {
      const patch = apiMock.mock.calls.find(([p, o]) => /\/tasks\/t1$/.test(p) && o?.method === "PATCH");
      expect(patch).toBeTruthy();
      expect(body(patch![1])).toEqual({ status_id: "s2" });
    });
  });

  it("quickEdit from the view reaches the task API with the priority", async () => {
    renderList();
    fireEvent.click(await screen.findByText("raise t2"));
    await waitFor(() => {
      const patch = apiMock.mock.calls.find(([p, o]) => /\/tasks\/t2$/.test(p) && o?.method === "PATCH");
      expect(body(patch![1])).toEqual({ priority: "high" });
    });
  });

  it("openTask navigates to the task page", async () => {
    renderList();
    fireEvent.click(await screen.findByText("open t1"));
    expect(await screen.findByText("task page")).toBeTruthy();
  });

  it("viewer: the container withholds quickEdit and changeStatus", async () => {
    useMemberStore.setState({ myRole: "viewer" });
    renderList();
    expect(await screen.findByText("First")).toBeTruthy();
    expect(screen.queryByText("start t1")).toBeNull();
    expect(screen.queryByText("raise t1")).toBeNull();
    expect(screen.getByText("pick t1")).toBeTruthy();
  });

  it("a view that throws falls back to mesh-default (CT-common-07)", async () => {
    dispose();
    dispose = registerSlotViewOverride("task.row", () => {
      throw new Error("boom");
    });
    vi.spyOn(console, "error").mockImplementation(() => {});
    renderList();
    expect(await screen.findByRole("link", { name: "First" })).toBeTruthy();
  });
});
