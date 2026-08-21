import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...args),
  getAccessToken: vi.fn(() => null),
}));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));

import { CommentList } from "@/components/comment-list";
import { useProjectStore } from "@/stores/project";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Comment, Project, Workspace } from "@/types";

const PROJECT = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Demo",
  slug: "demo",
  settings: {},
} as unknown as Project;
const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as unknown as Workspace;

function makeComment(id: string, taskId: string, body: string): Comment {
  return {
    id,
    task_id: taskId,
    parent_comment_id: null,
    author_id: "agent-1",
    author_type: "agent",
    author_name: "Wally",
    body,
    is_internal: false,
    created_at: "2026-08-21T12:00:00Z",
    updated_at: "2026-08-21T12:00:00Z",
  } as unknown as Comment;
}

beforeEach(() => {
  mockedApi.mockReset();
  useProjectStore.setState({ projects: [PROJECT], currentProject: PROJECT });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
  useRulesStore.setState({ teamDirectory: { agents: [], humans: [] } as never });
});

// task-panel.tsx reuses one CommentList instance across every task the user
// views in a session — switching `currentTask` in the store does not unmount
// this component. Before the fix in c39ea2aa, a fetch issued for an
// earlier-viewed task that resolved AFTER the current task's own fetch
// silently overwrote the right comments with the wrong task's — the user
// sees a task's own metadata (title, description) paired with someone
// else's, possibly empty, comment thread. This is the regression test for
// that: A's response is deliberately made to resolve after B's.
describe("CommentList — stale response after a task switch", () => {
  it("does not let a late response for a previous task overwrite the current task's comments", async () => {
    let resolveTaskA: (value: unknown) => void;
    const taskAPromise = new Promise((resolve) => {
      resolveTaskA = resolve;
    });

    mockedApi.mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-a/comments") return taskAPromise;
      if (path === "/api/v1/tasks/task-b/comments") {
        return Promise.resolve({
          items: [makeComment("c-b", "task-b", "B's comment")],
          total_count: 1,
          page: 1,
          page_size: 50,
          total_pages: 1,
          has_more: false,
        });
      }
      throw new Error(`unexpected path ${path}`);
    });

    const { rerender } = render(
      <MemoryRouter>
        <CommentList taskId="task-a" projId={PROJECT.id} />
      </MemoryRouter>,
    );

    // Switch to task B before A's fetch has resolved — exactly the "opened
    // several cards in a row" shape from the bug report.
    rerender(
      <MemoryRouter>
        <CommentList taskId="task-b" projId={PROJECT.id} />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText("B's comment")).toBeInTheDocument();
    });

    // Now let A's fetch resolve, late, for a task the user has already left.
    resolveTaskA!({
      items: [makeComment("c-a", "task-a", "A's comment")],
      total_count: 1,
      page: 1,
      page_size: 50,
      total_pages: 1,
      has_more: false,
    });

    // Give the resolved promise a tick to (wrongly, pre-fix) flow through
    // setState before asserting the final state is unaffected by it.
    await new Promise((r) => setTimeout(r, 0));

    expect(screen.getByText("B's comment")).toBeInTheDocument();
    expect(screen.queryByText("A's comment")).not.toBeInTheDocument();
  });
});
