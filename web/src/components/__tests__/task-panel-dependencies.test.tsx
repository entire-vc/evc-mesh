import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { TaskPanel } from "../task-panel";
import { TaskSlideOver } from "../task-slide-over";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import type { Task, Project, TaskStatus } from "@/types";

const id = "380cd83a-0000-4000-8000-000000000001";
const relatedId = "11111111-0000-4000-8000-000000000001";
const task = { created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z", priority: "none", created_by_type: "user", created_by: "someone", id, title: "Current task", project_id: "project", status_id: "todo", labels: [], custom_fields: {}, assignee_type: "unassigned" } as unknown as Task;
const related = { ...task, id: relatedId, title: "Cross project blocker", project_id: "other", status_id: "other-todo", assignee_name: "Alex" };
let edges: unknown[];
let requests: string[];
let mutationWait: Promise<void> | undefined;

beforeEach(() => {
  edges = [{ id: "edge", task_id: id, depends_on_task_id: relatedId, dependency_type: "blocks", related_task_title: related.title }];
  requests = [];
  mutationWait = undefined;
  useTaskStore.setState({ tasksById: {} });
  useProjectStore.setState({ currentProject: null, projects: [{ id: "project", settings: {} }] as Project[], statuses: [{ id: "todo", project_id: "project", name: "Todo", category: "todo" }] as TaskStatus[] });
  // mock: external HTTP boundary — exercise real stores, API and components.
  vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input);
    requests.push(path);
    let data: unknown = [];
    if (["POST", "DELETE"].includes(init?.method ?? "")) await mutationWait;
    if (path === `/api/v1/tasks/${id}/dependencies/edge` && init?.method === "DELETE") edges = [];
    if (path === `/api/v1/tasks/${id}`) data = task;
    if (path === `/api/v1/tasks/${relatedId}`) data = related;
    if (path === "/api/v1/projects/other/statuses") data = [{ id: "other-todo", name: "Doing elsewhere", category: "in_progress", color: "#f00" }];
    if (path === `/api/v1/tasks/${id}/dependencies`) {
      const created = { id: "new-edge", task_id: id, depends_on_task_id: relatedId, dependency_type: "relates_to" };
      if (init?.method === "POST") edges.push(created);
      data = init?.method === "POST" ? created : { outgoing: [...edges], incoming: [] };
    }
    if (path.endsWith("/subtasks")) data = { items: [] };
    if (path.endsWith("/comments")) data = { items: [], total_count: 0 };
    if (path.endsWith("/vcs-links")) data = { vcs_links: [] };
    if (path.includes("cost-summary")) data = { session_count: 0 };
    if (path.includes("team-directory")) data = { agents: [], humans: [] };
    return new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe.each(["full", "slide"] as const)("%s task dependencies tab", (view) => {
  it.each(["add", "delete"] as const)("refreshes shared count when %s finishes after switching tabs", async (action) => {
    let finish!: () => void;
    mutationWait = new Promise<void>(resolve => { finish = resolve; });
    const router = createMemoryRouter([{ path: "/", element: view === "full" ? <TaskPanel taskId={id} /> : <TaskSlideOver taskId={id} onClose={() => {}} /> }]);
    render(<RouterProvider router={router} />);
    const tabs = await screen.findAllByRole("button", { name: /Dependencies/ });
    await waitFor(() => expect(tabs[0]).toHaveTextContent("1 · 1 open"));
    fireEvent.click(tabs[0]!);
    await screen.findByRole("button", { name: /#11111111 Cross project blocker/ });
    if (action === "add") {
      fireEvent.click(screen.getByRole("button", { name: "Add" }));
      fireEvent.change(screen.getByPlaceholderText(/550e8400/), { target: { value: relatedId } });
      fireEvent.change(screen.getByRole("combobox", { name: "Relationship type" }), { target: { value: "relates_to" } });
      fireEvent.click(screen.getByRole("button", { name: "Add Dependency" }));
    } else {
      fireEvent.click(screen.getByRole("button", { name: /Remove dependency/ }));
      fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    }
    fireEvent.click(screen.getAllByRole("button", { name: "Subtasks" })[0]!);
    finish();
    await waitFor(() => expect(tabs[0]).toHaveTextContent(action === "add" ? "2 · 1 open" : "0 · 0 open"));
    fireEvent.click(tabs[0]!);
    if (action === "add") expect(await screen.findAllByRole("button", { name: /#11111111 Cross project blocker/ })).toHaveLength(2);
    else expect(await screen.findByText(/No dependencies/)).toBeInTheDocument();
  });

  it("fetches on open, shows the same count and blocker row, and refreshes after add", async () => {
    const router = createMemoryRouter([{ path: "/", element: view === "full" ? <TaskPanel taskId={id} /> : <TaskSlideOver taskId={id} onClose={() => {}} /> }]);
    render(<RouterProvider router={router} />);
    await screen.findAllByText("Current task");
    const tabs = await screen.findAllByRole("button", { name: /Dependencies/ });
    await waitFor(() => expect(tabs[0]).toHaveTextContent("1 · 1 open"));
    expect(requests.filter(p => p === `/api/v1/tasks/${id}/dependencies`)).toHaveLength(1);
    fireEvent.click(tabs[0]!);
    expect(await screen.findByRole("button", { name: /#11111111 Cross project blocker/ })).toBeInTheDocument();
    expect(screen.getByText("Doing elsewhere")).toBeInTheDocument();
    expect(screen.getByText("Alex")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    fireEvent.change(screen.getByPlaceholderText(/550e8400/), { target: { value: relatedId } });
    fireEvent.change(screen.getByRole("combobox", { name: "Relationship type" }), { target: { value: "relates_to" } });
    fireEvent.click(screen.getByRole("button", { name: "Add Dependency" }));
    await waitFor(() => expect(tabs[0]).toHaveTextContent("2 · 1 open"));
    fireEvent.click(screen.getAllByRole("button", { name: "Subtasks" })[0]!);
    await waitFor(() => expect(requests.some(p => p.endsWith("/subtasks"))).toBe(true));
  });
});
