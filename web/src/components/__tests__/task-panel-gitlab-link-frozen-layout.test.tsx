import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { TaskPanel } from "../task-panel";
import { TaskSlideOver } from "../task-slide-over";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Workspace, Task, Project, TaskStatus } from "@/types";

const id = "3dea1aaa-0000-4000-8000-000000000001";
const task = { created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z", priority: "none", created_by_type: "user", created_by: "someone", id, title: "Task with GitLab MR", project_id: "project", status_id: "todo", labels: [], custom_fields: {}, assignee_type: "unassigned", vcs_link_count: 1 } as unknown as Task;
const WAIT = { timeout: 10_000 };

beforeEach(() => {
  useWorkspaceStore.setState({ currentWorkspace: { id: "workspace-gitlab-frozen" } as Workspace });
  useTaskStore.setState({ tasksById: {} });
  useProjectStore.setState({ currentProject: null, projects: [{ id: "project", settings: {} }] as Project[], statuses: [{ id: "todo", project_id: "project", name: "Todo", category: "todo" }] as TaskStatus[] });
  // mock: external HTTP boundary — real stores, hooks and components.
  vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
    const path = String(input);
    let data: unknown = [];
    if (path === `/api/v1/tasks/${id}`) data = task;
    if (path.endsWith("/extensions")) data = { extensions: [{ id: "gitlab", name: "GitLab", manifest: { capabilities: { ui: ["task.panel", "task.badge"] } }, status: { enabled: true } }] };
    if (path.endsWith("/vcs-links")) data = { vcs_links: [{ id: "l1", task_id: id, provider: "gitlab", link_type: "pr", external_id: "1308", url: "https://git.example/mr/1308", title: "Frozen layout MR", status: "open", metadata: {}, created_at: "2026-10-01T00:00:00Z" }] };
    if (path.endsWith("/subtasks")) data = { items: [] };
    if (path.endsWith("/comments")) data = { items: [], total_count: 0 };
    if (path.includes("cost-summary")) data = { session_count: 0 };
    if (path.includes("team-directory") || path.endsWith("/team")) data = { agents: [], humans: [] };
    return new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

// The W2 freeze keeps the properties block as it was before the slots: a GitLab MR
// stays listed there (editor coordinates depend on it) even when the GitLab panel tab is active.
describe.each(["full", "slide"] as const)("%s task view with the GitLab slot enabled", (view) => {
  it("still lists the GitLab MR in the properties block", async () => {
    const router = createMemoryRouter([{ path: "/", element: view === "full" ? <TaskPanel taskId={id} /> : <TaskSlideOver taskId={id} onClose={() => {}} /> }]);
    render(<RouterProvider router={router} />);
    expect((await screen.findAllByText("Frozen layout MR", undefined, WAIT)).length).toBeGreaterThan(0);
  });
});
