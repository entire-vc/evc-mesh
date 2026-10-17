import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

// W2.7: a list row shows the task.badge slot of a connected, enabled extension
// (GitLab) for tasks that have GitLab links, and nothing for tasks without them.
vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (/\/projects\/p1\/tasks$/.test(path)) {
        return { items: TASKS, total_count: TASKS.length, page: 1, per_page: 50, has_more: false };
      }
      if (/\/projects\/p1\/statuses$/.test(path)) return STATUSES;
      if (/\/workspaces\/ws-1\/extensions$/.test(path)) {
        return {
          extensions: [
            { id: "gitlab", name: "GitLab", status: { enabled: GITLAB_ENABLED }, manifest: { capabilities: { ui: ["task.badge"] } } },
          ],
        };
      }
      if (/custom-fields/.test(path)) return [];
      return { items: [] };
    }),
  };
});

import { ListViewPage } from "@/pages/list-view";
import { resetSlotRegistryForTests } from "@/lib/extension-slots";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Task, TaskStatus } from "@/types";

let GITLAB_ENABLED = true;
const STATUSES = [
  { id: "s1", project_id: "p1", name: "s1", slug: "s1", color: "#000", position: 0, category: "todo", is_default: true, auto_transition: {} },
] as TaskStatus[];
const base = { project_id: "p1", status_id: "s1", position: 1, priority: "medium", labels: [] };
const TASKS = [
  { ...base, id: "t1", title: "With GitLab", vcs_link_count: 1, vcs_providers: ["gitlab"] },
  { ...base, id: "t2", title: "Without links", vcs_link_count: 0, vcs_providers: [] },
] as unknown as Task[];

function renderList() {
  return render(
    <MemoryRouter initialEntries={["/ws/proj/list"]}>
      <Routes><Route path="/:wsSlug/:projectSlug/list" element={<ListViewPage />} /></Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  GITLAB_ENABLED = true;
  resetSlotRegistryForTests();
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws-1" } as never });
  useProjectStore.setState({ currentProject: { id: "p1", slug: "proj", name: "P" } as never, statuses: [] });
  useTaskStore.setState({ tasks: [], tasksByStatus: {}, isLoading: false });
});

describe("list view: task.badge slot", () => {
  it("GitLab enabled → badge only on the row with GitLab links", async () => {
    renderList();
    expect(await screen.findByText("Without links")).toBeTruthy();
    expect(await screen.findAllByTestId("slot-badge-gitlab")).toHaveLength(1);
    const badge = screen.getByTestId("slot-badge-gitlab");
    const title = screen.getByText("With GitLab");
    // the badge sits in the same row as the title, not in the other one
    expect(title.parentElement?.contains(badge)).toBe(true);
  });

  it("GitLab disabled (red control) → no badge on any row", async () => {
    GITLAB_ENABLED = false;
    renderList();
    expect(await screen.findByText("With GitLab")).toBeTruthy();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByTestId("slot-badge-gitlab")).toBeNull();
  });
});
