/**
 * A task's URL names a project slug that is never checked against the task's
 * actual project_id (#b2be54e8) — so a task from "team-relay" opened under
 * `/p/local-sync/t/<id>` rendered identically instead of the URL correcting
 * itself. This pins the fix: once the task and this workspace's projects have
 * both loaded, a mismatched slug is replaced with the task's real one via a
 * `replace: true` navigation, a matching slug never navigates at all, and the
 * effect settles after firing once (it does not loop back and forth).
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

// TaskPanel is a large, heavily-wired component (comments, activity, subtasks,
// cost summary, …). The subject here is the redirect effect that lives in
// TaskDetailPage itself, so TaskPanel is stubbed down to the one thing the
// page's tests need to see: which taskId it was asked to render.
vi.mock("@/components/task-panel", () => ({
  TaskPanel: ({ taskId }: { taskId: string | null }) => (
    <div data-testid="task-panel">{taskId}</div>
  ),
}));

import { TaskDetailPage } from "@/pages/task-detail";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import type { Project, Task } from "@/types";

const TEAM_RELAY: Project = {
  id: "proj-team-relay",
  workspace_id: "ws-1",
  name: "Team Relay",
  description: "",
  slug: "team-relay",
  icon: "",
  settings: {},
  default_assignee_type: "none",
  is_archived: false,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

const LOCAL_SYNC: Project = {
  ...TEAM_RELAY,
  id: "proj-local-sync",
  name: "Local Sync",
  slug: "local-sync",
};

const TASK: Task = {
  id: "9d86c756-0000-4000-8000-000000000000",
  project_id: "proj-team-relay",
  status_id: "status-1",
  title: "Fix the docs sync",
  assignee_id: null,
  assignee_type: "unassigned",
  priority: "medium",
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

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          path="/w/:wsSlug/p/:projectSlug/t/:taskId"
          element={<TaskDetailPage />}
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedNavigate.mockReset();
  useProjectStore.setState({ projects: [TEAM_RELAY, LOCAL_SYNC] });
  useTaskStore.setState({ tasksById: { [TASK.id]: TASK } });
});

describe("TaskDetailPage — canonical project slug", () => {
  it("redirects a task opened under a foreign project's slug to its real one", async () => {
    renderAt(`/w/ws-006901c7/p/local-sync/t/${TASK.id}`);

    await waitFor(() => {
      expect(mockedNavigate).toHaveBeenCalledWith(
        `/w/ws-006901c7/p/team-relay/t/${TASK.id}`,
        { replace: true },
      );
    });
  });

  it("does not redirect when the slug already matches the task's project", async () => {
    renderAt(`/w/ws-006901c7/p/team-relay/t/${TASK.id}`);

    // Give any effect a tick to (not) fire before asserting silence.
    await screen.findByTestId("task-panel");
    await new Promise((r) => setTimeout(r, 0));

    expect(mockedNavigate).not.toHaveBeenCalled();
  });

  it("settles after one redirect instead of navigating back and forth", async () => {
    renderAt(`/w/ws-006901c7/p/local-sync/t/${TASK.id}`);

    await waitFor(() => {
      expect(mockedNavigate).toHaveBeenCalledTimes(1);
    });

    // Real navigation would swap :projectSlug to "team-relay" and re-render
    // this page at the canonical URL. Simulate that swap directly (the
    // MemoryRouter here is deliberately not re-navigated, since the mocked
    // useNavigate never touches history) and confirm the now-matching slug
    // does not produce a second call.
    renderAt(`/w/ws-006901c7/p/team-relay/t/${TASK.id}`);
    await new Promise((r) => setTimeout(r, 0));

    expect(mockedNavigate).toHaveBeenCalledTimes(1);
  });

  // Negative control from the acceptance criteria: a taskId that doesn't
  // exist must still produce TaskPanel's normal "Task not found." state, not
  // a redirect to nowhere. `currentTask` is null here (the id isn't in the
  // store), so the effect's `!currentTask` guard should keep it a no-op
  // regardless of which slug the URL carries.
  it("does not redirect for a taskId that does not exist, under any slug", async () => {
    renderAt(`/w/ws-006901c7/p/local-sync/t/does-not-exist`);

    await screen.findByTestId("task-panel");
    await new Promise((r) => setTimeout(r, 0));

    expect(mockedNavigate).not.toHaveBeenCalled();
  });
});
