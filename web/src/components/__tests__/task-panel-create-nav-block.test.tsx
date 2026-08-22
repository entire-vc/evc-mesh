/**
 * useBlocker (react-router) must intercept EVERY in-app way of leaving a
 * dirty create-mode draft — not just the Cancel button that the old
 * beforeunload-only guard covered — now that App.tsx mounts a data router
 * (task #7893ab16). Exercised against a REAL data router
 * (createMemoryRouter + RouterProvider): a declarative <MemoryRouter> can't
 * host useBlocker at all — it throws outside a data router context, which is
 * exactly the gap this migration closes.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, Link, RouterProvider } from "react-router";

vi.mock("@/lib/api", () => ({
  // Team-relay integration check (useProjectTrIntegration, unrelated to this
  // test's subject) fires unconditionally on mount and needs a real promise.
  api: vi.fn(() => Promise.resolve({ enabled: false })),
  getAccessToken: vi.fn(() => null),
}));

import { TaskPanel } from "@/components/task-panel";
import { useTaskStore } from "@/stores/task";
import { useProjectStore } from "@/stores/project";
import { useMemberStore } from "@/stores/member";
import { useCustomFieldStore } from "@/stores/custom-field";
import { useRecurringStore } from "@/stores/recurring";
import { useRulesStore } from "@/stores/rules";
import type { Project, Task } from "@/types";

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

const CREATED_TASK: Task = {
  id: "9d86c756-0000-4000-8000-000000000001",
  project_id: PROJECT.id,
  status_id: "status-1",
  title: "New task",
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
};

function renderCreatePanel(initialEntries: string[] = ["/new"]) {
  const router = createMemoryRouter(
    [
      {
        path: "/new",
        element: (
          <>
            {/* Stand-in for a sidebar nav link elsewhere in AppLayout — the
                point is that useBlocker fires on ANY in-app navigation, not
                just the panel's own Cancel button. */}
            <Link to="/elsewhere" data-testid="sidebar-link">
              Sidebar
            </Link>
            <TaskPanel
              taskId={null}
              mode="create"
              createProjectId={PROJECT.id}
              onBack={() => router.navigate("/elsewhere")}
              onCreated={(task) =>
                router.navigate(`/created/${task.id}`, { replace: true })
              }
              backLabel="Cancel"
            />
          </>
        ),
      },
      { path: "/elsewhere", element: <div data-testid="elsewhere" /> },
      { path: "/created/:id", element: <div data-testid="created" /> },
    ],
    { initialEntries },
  );
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  vi.restoreAllMocks();
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
  useTaskStore.setState({
    tasksById: {},
    createTask: vi.fn().mockResolvedValue(CREATED_TASK),
  });
});

async function typeDirtyTitle() {
  const titleInput = await screen.findByPlaceholderText("Task title *");
  fireEvent.change(titleInput, { target: { value: "Something unsaved" } });
  return titleInput;
}

describe("TaskPanel create mode — useBlocker guards every in-app exit", () => {
  it("does not block leaving an untouched (clean) draft via a sidebar-style link", async () => {
    renderCreatePanel();
    await screen.findByPlaceholderText("Task title *");
    const confirmSpy = vi.spyOn(window, "confirm");

    fireEvent.click(screen.getByTestId("sidebar-link"));

    await screen.findByTestId("elsewhere");
    expect(confirmSpy).not.toHaveBeenCalled();
  });

  it("blocks a sidebar-link click while dirty, and cancelling the confirm keeps the draft", async () => {
    renderCreatePanel();
    await typeDirtyTitle();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);

    fireEvent.click(screen.getByTestId("sidebar-link"));

    await waitFor(() => expect(confirmSpy).toHaveBeenCalledTimes(1));
    // Stayed on /new — the draft's own title input is still there.
    expect(screen.getByPlaceholderText("Task title *")).toHaveValue(
      "Something unsaved",
    );
    expect(screen.queryByTestId("elsewhere")).not.toBeInTheDocument();
  });

  it("blocks a sidebar-link click while dirty, and confirming discards the draft", async () => {
    renderCreatePanel();
    await typeDirtyTitle();
    vi.spyOn(window, "confirm").mockReturnValue(true);

    fireEvent.click(screen.getByTestId("sidebar-link"));

    await screen.findByTestId("elsewhere");
  });

  it("blocks the browser back button while dirty (POP navigation), not just declarative Link clicks", async () => {
    const router = renderCreatePanel(["/elsewhere", "/new"]);
    await typeDirtyTitle();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);

    router.navigate(-1);

    await waitFor(() => expect(confirmSpy).toHaveBeenCalledTimes(1));
    expect(screen.getByPlaceholderText("Task title *")).toHaveValue(
      "Something unsaved",
    );
  });

  it("does not block the redirect after a successful submit, even though the draft is still populated", async () => {
    renderCreatePanel();
    const titleInput = await typeDirtyTitle();
    const confirmSpy = vi.spyOn(window, "confirm");

    const submitButton = await screen.findByRole("button", {
      name: /create task/i,
    });
    fireEvent.click(submitButton);

    await waitFor(() => {
      expect(useTaskStore.getState().createTask).toHaveBeenCalled();
    });
    // onCreated navigates via replace: true — this must sail through
    // unblocked despite the form fields still holding the just-submitted
    // values (isDraftDirty is still true at this instant).
    expect(confirmSpy).not.toHaveBeenCalled();
    void titleInput;
  });
});
