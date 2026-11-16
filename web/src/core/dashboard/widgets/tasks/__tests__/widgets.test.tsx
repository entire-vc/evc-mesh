// The task widgets as the dashboard draws them: the core's Widget host, real data hooks with the instance settings,
// the mesh-default views. Only the network and the socket are mocked.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { RealtimeEvent } from "@entire-vc/mesh-theme-sdk";
import { useMemberStore } from "@/stores/member";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { Widget } from "../../../slots";
import type { WidgetInstance } from "../../../types";
import { myTasksQuery, taskListQuery, triageQueueOptions } from "../query";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: () => null }));
const listeners = new Set<(e: RealtimeEvent) => void>();
vi.mock("@/core/primitives/use-realtime", () => ({
  useRealtime: (_topic: unknown, on: (e: RealtimeEvent) => void) => {
    listeners.add(on);
    return { connected: true };
  },
}));
import { api } from "@/lib/api";

const HOUR = 3_600_000;
const task = (id: string, over: Record<string, unknown> = {}) => ({
  id, title: `Task ${id}`, priority: "high", project_id: "p1", created_by_name: "Ann",
  created_at: new Date(Date.now() - 3 * HOUR).toISOString(), ...over,
});
const emit = (e: RealtimeEvent) =>
  act(async () => {
    listeners.forEach((l) => l(e));
    await new Promise((r) => setTimeout(r, 300));
  });
const instance = (type: string, settings: Record<string, unknown> = {}): WidgetInstance => ({ id: type, type, x: 0, y: 0, w: 3, h: 4, settings });
function mount(inst: WidgetInstance, role: "member" | "viewer" = "member") {
  useMemberStore.setState({ myRole: role });
  return render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <Widget instance={inst} />
    </MemoryRouter>,
  );
}
const calls = (path: string) => vi.mocked(api).mock.calls.filter(([url]) => url === path);

beforeEach(() => {
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} })) as never;
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" }, { id: "p2", slug: "ops", name: "Ops" }] as never });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  listeners.clear();
});

describe("instance settings -> server query", () => {
  it("my-tasks: limit 5..50, status categories, project; junk falls back to the defaults", () => {
    expect(myTasksQuery({ limit: 80, statuses: ["review", "bogus"], projectId: "p1" })).toEqual({
      scope: "mine", projectId: "p1", statusCategories: ["review"], sort: "updated", limit: 50,
    });
    expect(myTasksQuery({ limit: 1 }).limit).toBe(5);
    expect(myTasksQuery({ limit: "x", statuses: "todo", projectId: "" })).toEqual({
      scope: "mine", projectId: undefined, statusCategories: undefined, sort: "updated", limit: 10,
    });
  });

  it("task-list: every TaskQuery field; scope project without a project reads the workspace", () => {
    expect(
      taskListQuery({ scope: "project", projectId: "p2", statuses: ["todo"], priority: ["urgent", "nope"], dueBefore: "2026-12-01", gated: true, sort: "due_date", limit: 7 }),
    ).toEqual({ scope: "project", projectId: "p2", statusCategories: ["todo"], priority: ["urgent"], dueBefore: "2026-12-01", gated: true, sort: "due_date", limit: 7 });
    expect(taskListQuery({ scope: "project" }).scope).toBe("workspace");
    expect(taskListQuery({ scope: "galaxy", sort: "random" })).toMatchObject({ scope: "mine", sort: "updated", limit: 20, gated: undefined });
  });

  it("triage-queue: limit, project, hard gates only", () => {
    expect(triageQueueOptions({ limit: 0, projectId: "p1", hardGatesOnly: true })).toEqual({ limit: 1, projectId: "p1", hardGatesOnly: true });
    expect(triageQueueOptions({ hardGatesOnly: "yes" })).toEqual({ limit: 10, projectId: undefined, hardGatesOnly: false });
  });
});

describe("my-tasks and task-list on the dashboard", () => {
  it("my-tasks asks the server with its settings and shows the real total; rows link to the task", async () => {
    vi.mocked(api).mockResolvedValue({ items: [task("a"), task("b", { due_date: "2026-12-01" })], total_count: 31 });
    const { container } = mount(instance("my-tasks", { limit: 5, statuses: ["in_progress"], projectId: "p1" }));
    await screen.findByText("Task a");
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/ws1/tasks/query", {
      params: { scope: "mine", project_id: "p1", status_category: "in_progress", priority: undefined, due_before: undefined, gated: undefined, sort: "updated", limit: 5 },
    });
    expect(screen.getByTestId("widget-total").textContent).toBe("31");
    expect(screen.getByText("Task a").closest("a")!.getAttribute("href")).toContain("/t/a");
    // task.edit: priority and due date are edited in place
    expect(container.querySelectorAll("[data-inline-edit=priority]")).toHaveLength(2);
    expect(container.querySelectorAll("[data-inline-edit=due_date]")).toHaveLength(2);
  });

  it("a viewer gets the rows without editors", async () => {
    vi.mocked(api).mockResolvedValue({ items: [task("a")], total_count: 1 });
    const { container } = mount(instance("my-tasks"), "viewer");
    await screen.findByText("Task a");
    expect(container.querySelectorAll("[data-inline-edit]")).toHaveLength(0);
  });

  it("the priority editor opens on the listed task's current value (the task is not in the board store)", async () => {
    vi.mocked(api).mockResolvedValue({ items: [task("a", { priority: "low" })], total_count: 1 });
    const { container } = mount(instance("my-tasks"));
    await screen.findByText("Task a");
    fireEvent.click(container.querySelector("[data-inline-edit=priority]")!);
    expect((screen.getByRole("combobox", { name: "priority" }) as HTMLSelectElement).value).toBe("low");
  });

  it("task-list sends the whole filter", async () => {
    vi.mocked(api).mockResolvedValue({ items: [task("a")], total_count: 1 });
    mount(instance("task-list", { scope: "workspace", priority: ["urgent", "high"], dueBefore: "2026-12-01", gated: true, sort: "priority", limit: 15 }));
    await screen.findByText("Task a");
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/ws1/tasks/query", {
      params: { scope: "workspace", project_id: undefined, status_category: undefined, priority: "urgent,high", due_before: "2026-12-01", gated: "true", sort: "priority", limit: 15 },
    });
  });

  it("loading, error with retry, and empty are three different states", async () => {
    let fail = true;
    vi.mocked(api).mockImplementation(async () => {
      if (fail) throw new Error("boom");
      return { items: [], total_count: 0 };
    });
    mount(instance("task-list"));
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("boom");
    fail = false;
    fireEvent.click(within(alert).getByRole("button"));
    await screen.findByText("No tasks match this filter");
  });
});

describe("triage-queue on the dashboard", () => {
  const serveQueue = (rows: () => unknown[]) =>
    vi.mocked(api).mockImplementation(async (url: string, opts?: { method?: string }) => {
      if (url === "/api/v1/workspaces/ws1/triage") return { items: rows(), total_count: rows().length };
      if (url === "/api/v1/projects/p2/statuses") return [{ id: "s-tri", name: "Inbox", category: "triage" }, { id: "s-todo", name: "To do", category: "todo" }];
      if (url === "/api/v1/projects/p1/statuses") return [{ id: "s1", name: "Ready", category: "todo" }];
      if (opts?.method === "POST") return {};
      return {};
    });

  it("shows project, priority, author and age, with the settings sent to the server", async () => {
    serveQueue(() => [task("a", { project_id: "p2" })]);
    mount(instance("triage-queue", { limit: 5, projectId: "p2", hardGatesOnly: true }));
    const row = (await screen.findByText("Task a")).closest("li")!;
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/ws1/triage", { params: { page_size: 5, project_id: "p2", hard_gates_only: "true" } });
    const text = row.textContent!;
    expect(text).toContain("Ops");
    expect(text).toContain("high");
    expect(text).toContain("by Ann");
    expect(text).toContain("3h");
    expect(within(row).getByText("Open").closest("a")!.getAttribute("href")).toContain("/t/a");
  });

  it("Move: project, then a status outside triage; another project goes through move-to-project first", async () => {
    serveQueue(() => [task("a")]);
    mount(instance("triage-queue"));
    fireEvent.click(await screen.findByRole("button", { name: 'Move "Task a"' }));
    const dialog = await screen.findByRole("dialog");
    const [projectSel] = within(dialog).getAllByRole("combobox") as HTMLSelectElement[];
    expect(projectSel!.value).toBe("p1");
    fireEvent.change(projectSel!, { target: { value: "p2" } });
    await waitFor(() => expect((within(dialog).getAllByRole("combobox")[1] as HTMLSelectElement).value).toBe("s-todo"));
    expect(within(dialog).queryByRole("option", { name: "Inbox" })).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Move" }));
    await waitFor(() => expect(calls("/api/v1/tasks/a/move")).toHaveLength(1));
    const posts = vi.mocked(api).mock.calls.filter(([, o]) => (o as { method?: string } | undefined)?.method === "POST").map(([url, o]) => [url, (o as { body: unknown }).body]);
    expect(posts).toEqual([
      ["/api/v1/tasks/a/move-to-project", { project_id: "p2" }],
      ["/api/v1/tasks/a/move", { status_id: "s-todo" }],
    ]);
  });

  it("no Move without task.move", async () => {
    serveQueue(() => [task("a")]);
    mount(instance("triage-queue"), "viewer");
    await screen.findByText("Task a");
    expect(screen.queryByRole("button", { name: /Move/ })).toBeNull();
  });

  it("a task moved out of triage in another window leaves the queue without a reload", async () => {
    let rows = [task("a"), task("b")];
    serveQueue(() => rows);
    mount(instance("triage-queue"));
    await screen.findByText("Task a");
    expect(screen.getByTestId("widget-total").textContent).toBe("2");
    rows = [task("b")];
    await emit({ type: "status_change", data: { subject: "task.moved", task_id: "a" }, at: "1" });
    await waitFor(() => expect(screen.queryByText("Task a")).toBeNull());
    expect(screen.getByText("Task b")).toBeTruthy();
    expect(screen.getByTestId("widget-total").textContent).toBe("1");
  });
});
