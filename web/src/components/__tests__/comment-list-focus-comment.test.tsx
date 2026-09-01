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

function makeComment(
  id: string,
  taskId: string,
  body: string,
  parentCommentId: string | null = null,
): Comment {
  return {
    id,
    task_id: taskId,
    parent_comment_id: parentCommentId,
    author_id: "agent-1",
    author_type: "agent",
    author_name: "Wally",
    body,
    is_internal: false,
    created_at: "2026-08-21T12:00:00Z",
    updated_at: "2026-08-21T12:00:00Z",
  } as unknown as Comment;
}

// jsdom does not implement scrollIntoView at all — every CommentItem calls it
// unconditionally in an effect (guarded by `focused`), so it has to exist as
// a no-op before render or the whole tree throws.
beforeEach(() => {
  mockedApi.mockReset();
  Element.prototype.scrollIntoView = vi.fn();
  useProjectStore.setState({ projects: [PROJECT], currentProject: PROJECT });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
  useRulesStore.setState({ teamDirectory: { agents: [], humans: [] } as never });
});

function highlightedIds(): string[] {
  return Array.from(document.querySelectorAll("[data-comment-id]"))
    .filter((el) => el.className.includes("border-yellow-400"))
    .map((el) => el.getAttribute("data-comment-id") as string);
}

// The regression this guards: a mention's `?comment=` link opened the task
// but landed the reader at the rail's default scroll position — the target
// comment was in the DOM the whole time, un-scrolled-to and unmarked. A rail
// that is merely *visible* (true on desktop by default, per task-panel.tsx)
// proves nothing about this; only checking the specific target's state does.
describe("CommentList — focusCommentId scroll/highlight", () => {
  it("highlights and scrolls to the target comment already on the loaded page", async () => {
    mockedApi.mockResolvedValue({
      items: [
        makeComment("c-1", "task-x", "Newest"),
        makeComment("c-2", "task-x", "Second"),
      ],
      total_count: 2,
      page: 1,
      page_size: 50,
      total_pages: 1,
      has_more: false,
    });

    render(
      <MemoryRouter>
        <CommentList taskId="task-x" projId={PROJECT.id} focusCommentId="c-2" />
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByText("Second")).toBeInTheDocument());

    expect(highlightedIds()).toEqual(["c-2"]);
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    // Only the loaded page was fetched — nothing to paginate for.
    expect(mockedApi).toHaveBeenCalledTimes(1);
  });

  it("does not highlight comments other than the target (negative control)", async () => {
    mockedApi.mockResolvedValue({
      items: [
        makeComment("c-1", "task-x", "Newest"),
        makeComment("c-2", "task-x", "Second"),
      ],
      total_count: 2,
      page: 1,
      page_size: 50,
      total_pages: 1,
      has_more: false,
    });

    render(
      <MemoryRouter>
        <CommentList taskId="task-x" projId={PROJECT.id} focusCommentId="c-2" />
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByText("Second")).toBeInTheDocument());

    // c-1 is real and on-screen, but is not the mentioned comment.
    expect(highlightedIds()).not.toContain("c-1");
  });

  it("auto-paginates to an older page to find the target, then highlights it", async () => {
    mockedApi.mockImplementation(
      (path: string, opts?: { params?: { page?: number } }) => {
        if (path !== "/api/v1/tasks/task-x/comments") {
          throw new Error(`unexpected path ${path}`);
        }
        const page = opts?.params?.page ?? 1;
        if (page === 1) {
          return Promise.resolve({
            items: [
              makeComment("c-1", "task-x", "Newest"),
              makeComment("c-2", "task-x", "Second"),
            ],
            total_count: 4,
            page: 1,
            page_size: 2,
            total_pages: 2,
            has_more: true,
          });
        }
        if (page === 2) {
          return Promise.resolve({
            items: [
              makeComment("c-3", "task-x", "Third — the target"),
              makeComment("c-4", "task-x", "Oldest"),
            ],
            total_count: 4,
            page: 2,
            page_size: 2,
            total_pages: 2,
            has_more: false,
          });
        }
        throw new Error(`unexpected page ${page}`);
      },
    );

    render(
      <MemoryRouter>
        <CommentList taskId="task-x" projId={PROJECT.id} focusCommentId="c-3" />
      </MemoryRouter>,
    );

    // Not on page 1 — must not render (and must not falsely highlight
    // anything) until the second page has actually loaded.
    await waitFor(() => expect(screen.getByText("Newest")).toBeInTheDocument());
    expect(screen.queryByText("Third — the target")).not.toBeInTheDocument();

    await waitFor(() =>
      expect(screen.getByText("Third — the target")).toBeInTheDocument(),
    );
    expect(highlightedIds()).toEqual(["c-3"]);
    expect(mockedApi).toHaveBeenCalledTimes(2);
  });

  // Regression for a defect a fresh-context verifier caught before merge: a
  // reply's own id can appear in the flat, already-loaded `comments` state
  // (newer than its root, so it lands on an earlier/newer page) while its
  // root — required to render it at all — is still on an older page. The
  // pagination stop-check must not treat the reply's bare presence as
  // "found"; it has to confirm the root it nests under is loaded too, or the
  // reply is fetched but permanently unrendered: no scroll, no highlight.
  it("keeps paginating for a reply whose root comment is on a later page", async () => {
    mockedApi.mockImplementation(
      (path: string, opts?: { params?: { page?: number } }) => {
        if (path !== "/api/v1/tasks/task-x/comments") {
          throw new Error(`unexpected path ${path}`);
        }
        const page = opts?.params?.page ?? 1;
        if (page === 1) {
          return Promise.resolve({
            // The target: a reply to "root-old", which hasn't loaded yet.
            items: [makeComment("reply-1", "task-x", "The reply — target", "root-old")],
            total_count: 2,
            page: 1,
            page_size: 1,
            total_pages: 2,
            has_more: true,
          });
        }
        if (page === 2) {
          return Promise.resolve({
            items: [makeComment("root-old", "task-x", "The old root")],
            total_count: 2,
            page: 2,
            page_size: 1,
            total_pages: 2,
            has_more: false,
          });
        }
        throw new Error(`unexpected page ${page}`);
      },
    );

    render(
      <MemoryRouter>
        <CommentList taskId="task-x" projId={PROJECT.id} focusCommentId="reply-1" />
      </MemoryRouter>,
    );

    // Before the root arrives, the reply must not be silently "found" —
    // it has nothing to nest under, so it can't be on screen yet either.
    await waitFor(() => expect(mockedApi).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("The reply — target")).not.toBeInTheDocument();

    await waitFor(() =>
      expect(screen.getByText("The reply — target")).toBeInTheDocument(),
    );
    expect(highlightedIds()).toEqual(["reply-1"]);
    expect(mockedApi).toHaveBeenCalledTimes(2);
  });

  it("stops asking once the server reports no more pages (explicit not-found, no infinite loop)", async () => {
    mockedApi.mockResolvedValue({
      items: [makeComment("c-1", "task-x", "Only comment")],
      total_count: 1,
      page: 1,
      page_size: 50,
      total_pages: 1,
      has_more: false,
    });

    render(
      <MemoryRouter>
        <CommentList taskId="task-x" projId={PROJECT.id} focusCommentId="c-deleted" />
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByText("Only comment")).toBeInTheDocument());
    // Give any wrongly-unbounded retry loop a chance to fire before asserting.
    await new Promise((r) => setTimeout(r, 0));

    expect(mockedApi).toHaveBeenCalledTimes(1);
    expect(highlightedIds()).toEqual([]);
  });
});
