import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn().mockResolvedValue({}),
  getAccessToken: vi.fn(() => null),
}));

import { Header } from "@/components/layout/header";
import { useAuthStore } from "@/stores/auth";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import type { Project, Task, User } from "@/types";

/**
 * Two projects: "team-a" is what the sidebar happens to have selected
 * (useProjectStore.currentProject), "team-b" actually owns the open task.
 * This is exactly the direct-link-into-someone-else's-project shape called
 * out in the task: the Docs shortcut must follow the task, not the sidebar.
 */
const TEAM_A: Project = {
  id: "proj-a",
  workspace_id: "ws1",
  name: "Team A",
  description: "",
  slug: "team-a",
  icon: "",
  settings: {},
  default_assignee_type: "none",
  is_archived: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const TEAM_B: Project = { ...TEAM_A, id: "proj-b", name: "Team B", slug: "team-b" };

const OPEN_TASK: Task = {
  id: "task-1",
  project_id: TEAM_B.id,
  status_id: "status-1",
  title: "Some task",
  assignee_id: null,
  assignee_type: "unassigned",
  priority: "medium",
  parent_task_id: null,
  position: 0,
  due_date: null,
  estimated_hours: null,
  custom_fields: null,
  labels: null,
  created_by: "u1",
  created_by_type: "user",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  completed_at: null,
  human_gate: false,
};

const USER: User = {
  id: "u1",
  email: "pavel@example.com",
  name: "Pavel",
  avatar_url: "",
  is_active: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function renderHeaderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          path="w/:wsSlug/p/:projectSlug/t/:taskId"
          element={<Header onToggleSidebar={() => {}} />}
        />
        <Route
          path="w/:wsSlug/p/:projectSlug"
          element={<Header onToggleSidebar={() => {}} />}
        />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.classList.remove("dark");
  useAuthStore.setState({ user: USER, isAuthenticated: true, isLoading: false });
  useProjectStore.setState({
    projects: [TEAM_A, TEAM_B],
    currentProject: TEAM_A, // sidebar's current selection — deliberately NOT team-b
  });
  useTaskStore.setState({ tasksById: { [OPEN_TASK.id]: OPEN_TASK } });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("Header — docs shortcut", () => {
  it("is absent when no task is open", () => {
    renderHeaderAt("/w/acme/p/team-a");
    expect(screen.queryByRole("link", { name: /project docs/i })).toBeNull();
  });

  it("links to the OPEN TASK's own project docs, not the sidebar's current project", () => {
    // URL literally names team-a, but the task loaded into the store belongs
    // to team-b — the same shape as a direct link into a foreign project.
    renderHeaderAt("/w/acme/p/team-a/t/task-1");

    const link = screen.getByRole("link", { name: /project docs/i });
    expect(link).toHaveAttribute("href", "/w/acme/p/team-b/docs");
  });
});

describe("Header — theme toggle", () => {
  it("is not rendered as a standalone header control before the user menu opens", () => {
    renderHeaderAt("/w/acme/p/team-a");
    // The dropdown content (and with it the "Theme" item) does not exist in
    // the DOM at all until the trigger is clicked — proving it isn't also
    // present as a separate always-visible header button.
    expect(screen.queryByText("Theme")).toBeNull();
  });

  it("shows the active theme inside the user menu item, and toggling persists (localStorage) across reload", () => {
    renderHeaderAt("/w/acme/p/team-a");

    fireEvent.click(screen.getByRole("button", { name: "P" }));

    const themeItem = screen.getByText("Theme").closest('[role="menuitem"]');
    expect(themeItem).not.toBeNull();
    // Starts light: main.tsx's early-init logic mirrors this — no saved
    // theme and (in jsdom) no dark system preference.
    expect(themeItem).toHaveTextContent("Light");

    fireEvent.click(themeItem!);

    expect(document.documentElement.classList.contains("dark")).toBe(true);
    // This is the persisted value main.tsx reads on the NEXT page load —
    // the regression this covers is exactly "moved the button, dropped the
    // write".
    expect(localStorage.getItem("theme")).toBe("dark");
  });
});
