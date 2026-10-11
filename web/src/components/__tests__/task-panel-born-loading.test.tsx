/**
 * The task panel is born loading (perf: task.open layout count).
 *
 * With the task already in the store, the panel used to mount its whole content
 * for one frame before the load effect switched to the skeleton, firing the
 * comments GET twice. Now nothing but the skeleton is mounted until the task
 * fetch resolves, and the comments GET goes out exactly once.
 */
import { describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider, useParams } from "react-router";


const apiPaths: string[] = [];

vi.mock("@/lib/api", () => ({
  api: vi.fn((path: string) => {
    apiPaths.push(path);
    if (path.includes("/comments")) return Promise.resolve({ comments: [] });
    if (path.includes("/vcs-links")) return Promise.resolve({ vcs_links: [] });
    if (path.includes("/dependencies")) {
      return Promise.resolve({ blocked_by: [], blocks: [], parent: null, children: [] });
    }
    return Promise.resolve({ enabled: false });
  }),
  getAccessToken: vi.fn(() => null),
  getTaskCostSummary: vi.fn(() =>
    Promise.resolve({
      total_cost: 0,
      tokens_in: 0,
      tokens_out: 0,
      session_count: 0,
      rework_count: 0,
      quality_flag: "",
    }),
  ),
  uploadPendingImages: vi.fn((_id: string, _p: unknown, desc: string) =>
    Promise.resolve(desc),
  ),
}));

import { TaskPanel } from "@/components/task-panel";
import { useAuthStore } from "@/stores/auth";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { useMemberStore } from "@/stores/member";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useRecurringStore } from "@/stores/recurring";
import { useRulesStore } from "@/stores/rules";
import { useTemplateStore } from "@/stores/template";
import type { Project, Task, User } from "@/types";

const PROJECT: Project = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Demo",
  description: "",
  slug: "demo",
  icon: "",
  settings: {},
  default_assignee_type: "none",
  is_archived: false,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

const USER = { id: "user-1", email: "p@example.com", name: "P" } as unknown as User;

function plainTask(): Task {
  return {
    id: "b044935d-0000-4000-8000-000000000001",
    project_id: PROJECT.id,
    status_id: "status-1",
    title: "A plain task",
    assignee_id: null,
    assignee_type: "unassigned",
    priority: "none",
    human_gate: false,
    parent_task_id: null,
    position: 0,
    due_date: null,
    estimated_hours: null,
    custom_fields: null,
    labels: null,
    created_by: "u1",
    created_by_type: "user",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    completed_at: null,
  } as Task;
}

function PanelFromRoute() {
  return <TaskPanel taskId={useParams().id ?? null} />;
}

function mount(task: Task, user: User | null, fetchTask: () => Promise<Task>) {
  useAuthStore.setState({ user, isAuthenticated: user !== null });
  useProjectStore.setState({
    projects: [PROJECT],
    currentProject: PROJECT,
    statuses: [],
    fetchStatuses: vi.fn().mockResolvedValue(undefined),
  });
  useMemberStore.setState({
    projectMembers: [],
    fetchProjectMembers: vi.fn().mockResolvedValue(undefined),
  });
  useCustomFieldStore.setState({
    fields: [],
    fetchFields: vi.fn().mockResolvedValue(undefined),
  });
  useRecurringStore.setState({
    schedules: [],
    fetchSchedules: vi.fn().mockResolvedValue(undefined),
  });
  useRulesStore.setState({
    teamDirectory: { agents: [], humans: [] } as never,
    fetchTeamDirectory: vi.fn().mockResolvedValue(undefined),
  });
  useTemplateStore.setState({
    templates: [],
    fetchTemplates: vi.fn().mockResolvedValue(undefined),
  });
  useTaskStore.setState({
    tasksById: { [task.id]: task, [`${task.id}-b`]: { ...task, id: `${task.id}-b`, title: "Another task" } },
    fetchTask: vi.fn(fetchTask),
  });

  const router = createMemoryRouter(
    [{ path: "/:id", element: <PanelFromRoute /> }],
    { initialEntries: [`/${task.id}`] },
  );
  return { router, ...render(<RouterProvider router={router} />) };
}


describe("TaskPanel — born loading", () => {
  it("mounts no content and issues no comments GET until the task fetch resolves, then exactly one", async () => {
    apiPaths.length = 0;
    let resolve!: (t: Task) => void;
    const task = plainTask();
    mount(task, USER, () => new Promise<Task>((r) => (resolve = r)));

    await waitFor(() => expect(useTaskStore.getState().fetchTask).toHaveBeenCalled());
    expect(screen.queryByText("A plain task")).toBeNull();
    expect(apiPaths.filter((p) => p.includes("/comments"))).toHaveLength(0);

    resolve(task);
    await waitFor(() => expect(apiPaths.filter((p) => p.includes("/comments")).length).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 50));
    expect(apiPaths.filter((p) => p.includes("/comments"))).toHaveLength(1);
    // Members: one store fetch for the project, and no extra members GET from the
    // content that used to mount early.
    expect(useMemberStore.getState().fetchProjectMembers).toHaveBeenCalledTimes(1);
    expect(apiPaths.filter((p) => p.includes("/members")).length).toBeLessThanOrEqual(1);
  });

  it("switching tasks inside a mounted panel is loading at once: the new task's content does not mount early", async () => {
    apiPaths.length = 0;
    const task = plainTask();
    let pending: ((t: Task) => void) | null = null;
    const { router } = mount(task, USER, () => Promise.resolve(task));
    await waitFor(() => expect(screen.getAllByText("A plain task").length).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 50));
    const before = apiPaths.filter((p) => p.includes("/comments")).length;

    const next = { ...task, id: `${task.id}-b`, title: "Another task" };
    useTaskStore.setState({ fetchTask: vi.fn(() => new Promise<Task>((r) => (pending = r))) } as never);
    await act(async () => {
      await router.navigate(`/${next.id}`);
    });
    expect(screen.queryAllByText("Another task")).toHaveLength(0);
    expect(apiPaths.filter((p) => p.includes("/comments")).length).toBe(before);

    await act(async () => {
      pending!(next);
    });
    await waitFor(() => expect(screen.getAllByText("Another task").length).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 50));
    expect(apiPaths.filter((p) => p.includes("/comments")).length).toBe(before + 1);
  });
});
