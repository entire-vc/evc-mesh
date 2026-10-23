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
  email: "alex@example.com",
  name: "Alex",
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
        <Route path="w/:wsSlug/me" element={<div>PROFILE-SCREEN</div>} />
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

describe("Header — Docs tab on an open task", () => {
  it("has no standalone docs icon link on a task", () => {
    renderHeaderAt("/w/acme/p/team-a/t/task-1");
    expect(screen.queryByRole("link", { name: /project docs/i })).toBeNull();
  });

  it("shows Docs as a tab in the view strip with no tab active", () => {
    renderHeaderAt("/w/acme/p/team-a/t/task-1");
    for (const name of ["Board", "List", "Docs"]) {
      const tab = screen.getByRole("button", { name });
      expect(tab).not.toHaveAttribute("aria-current");
    }
  });

  it("clicking Docs goes to the OPEN TASK's project docs, not the URL's project", () => {
    render(
      <MemoryRouter initialEntries={["/w/acme/p/team-a/t/task-1"]}>
        <Routes>
          <Route
            path="w/:wsSlug/p/:projectSlug/t/:taskId"
            element={<Header onToggleSidebar={() => {}} />}
          />
          <Route path="w/:wsSlug/p/team-b/docs" element={<div>DOCS-OF-B</div>} />
          <Route path="w/:wsSlug/p/team-a/docs" element={<div>DOCS-OF-A</div>} />
        </Routes>
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Docs" }));
    expect(screen.getByText("DOCS-OF-B")).toBeInTheDocument();
  });
});

describe("Header — user menu", () => {
  it("has no theme, language or app-access items: those live on the profile screen", () => {
    renderHeaderAt("/w/acme/p/team-a");
    fireEvent.click(screen.getByRole("button", { name: "P" }));

    expect(screen.getByRole("menuitem", { name: "Open profile" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Log out" })).toBeInTheDocument();
    for (const gone of ["Theme", "Language", "Connected apps", "App access"]) {
      expect(screen.queryByText(gone)).toBeNull();
    }
    expect(screen.queryByText("Русский")).toBeNull();
  });

  it("the name and email entry opens /w/:ws/me", () => {
    renderHeaderAt("/w/acme/p/team-a");
    fireEvent.click(screen.getByRole("button", { name: "P" }));

    const item = screen.getByRole("menuitem", { name: "Open profile" });
    expect(item).toHaveTextContent("Alex");
    expect(item).toHaveTextContent("alex@example.com");
    fireEvent.click(item);

    expect(screen.getByText("PROFILE-SCREEN")).toBeInTheDocument();
  });
});
