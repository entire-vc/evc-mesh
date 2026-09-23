import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...args),
  getAccessToken: vi.fn(() => null),
}));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));

import { CommentList, missActionFor } from "@/components/comment-list";
import { useProjectStore } from "@/stores/project";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Comment, CommentDeliveryOutcome, Project, Workspace } from "@/types";

// #ed60c795 — a person's @-mention that reached nobody must be visible to
// them as a miss, with the one action that fixes it. Live case: Pavel wrote
// "@howard" on Howard's own card parked in backlog; the UI showed only a
// yellow machine word, and he had to ask whether the agent got it.

const PROJECT = { id: "proj-1", workspace_id: "ws-1", name: "Demo", slug: "demo", settings: {} } as unknown as Project;
const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as unknown as Workspace;

function row(over: Partial<CommentDeliveryOutcome>): CommentDeliveryOutcome {
  return {
    comment_id: "c-1",
    recipient_slug: "howard",
    recipient_id: "agent-howard",
    recipient_kind: "agent",
    outcome: "skipped",
    reason: "status_not_fed",
    channel: "none",
    recipient_presence: "online",
    decided_at: "2026-09-23T21:04:00Z",
    task_status_category: "backlog",
    hint: "this task is theirs but sits in backlog, which their queue doesn't poll — move it to todo if they should act on it",
    ...over,
  };
}

function comment(delivery: CommentDeliveryOutcome[]): Comment {
  return {
    id: "c-1",
    task_id: "task-1",
    parent_comment_id: null,
    author_id: "pavel",
    author_type: "user",
    author_name: "Pavel",
    body: "Дорабатывай программу @howard",
    is_internal: false,
    created_at: "2026-09-23T21:04:00Z",
    updated_at: "2026-09-23T21:04:00Z",
    delivery,
  } as unknown as Comment;
}

function serve(c: Comment) {
  mockedApi.mockImplementation((path: string, opts?: { method?: string }) => {
    if (path === "/api/v1/tasks/task-1/comments") {
      return Promise.resolve({ items: [c], total_count: 1, page: 1, page_size: 50, total_pages: 1, has_more: false });
    }
    if (path === "/api/v1/projects/proj-1/statuses") {
      return Promise.resolve([
        { id: "st-backlog", category: "backlog", position: 0 },
        { id: "st-todo", category: "todo", position: 1 },
      ]);
    }
    if (path === "/api/v1/tasks/task-1/move" && opts?.method === "POST") return Promise.resolve({});
    if (path === "/api/v1/tasks/task-1" && opts?.method === "PATCH") return Promise.resolve({ id: "task-1" });
    if (path === "/api/v1/tasks/task-1") return Promise.resolve({ id: "task-1" });
    throw new Error(`unexpected ${opts?.method ?? "GET"} ${path}`);
  });
}

function mount() {
  render(
    <MemoryRouter>
      <CommentList taskId="task-1" projId={PROJECT.id} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedApi.mockReset();
  useProjectStore.setState({ projects: [PROJECT], currentProject: PROJECT });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
  useRulesStore.setState({ teamDirectory: { agents: [], humans: [] } as never });
});

describe("CommentList — delivery miss plate", () => {
  it("shows the miss and moves the card to todo when asked", async () => {
    serve(comment([row({})]));
    mount();

    const plate = await screen.findByTestId("delivery-miss-plate");
    expect(plate).toHaveTextContent("@howard won't see this");
    expect(plate).toHaveTextContent("sits in backlog");
    expect(plate).not.toHaveTextContent("assign");

    fireEvent.click(screen.getByRole("button", { name: "Move to todo" }));

    // Behaviour, not presence: the move request carried the project's todo status.
    await waitFor(() => {
      const move = mockedApi.mock.calls.find(([p]) => p === "/api/v1/tasks/task-1/move");
      expect(move).toBeDefined();
      expect(move![1]).toMatchObject({ method: "POST", body: { status_id: "st-todo" } });
    });
    expect(await screen.findByText("Moved to todo.")).toBeInTheDocument();
  });

  it("offers assignment when the card belongs to someone else", async () => {
    serve(
      comment([
        row({
          reason: "not_assignee",
          task_status_category: "todo",
          hint: "recipient is alive but this task is assigned to someone else — assign it to them if they should act on it",
        }),
      ]),
    );
    mount();

    fireEvent.click(await screen.findByRole("button", { name: "Assign to @howard" }));
    await waitFor(() => {
      const patch = mockedApi.mock.calls.find(
        ([p, o]) => p === "/api/v1/tasks/task-1" && (o as { method?: string })?.method === "PATCH",
      );
      expect(patch).toBeDefined();
      expect(patch![1]).toMatchObject({ body: { assignee_id: "agent-howard", assignee_type: "agent" } });
    });
  });

  it("control: a delivered mention shows no plate", async () => {
    serve(comment([row({ outcome: "delivered", reason: "task_queue", channel: "task_queue", hint: undefined })]));
    mount();

    await screen.findByText("Дорабатывай программу @howard", { exact: false });
    expect(screen.queryByTestId("delivery-miss-plate")).not.toBeInTheDocument();
  });

  it("a gated card gets the explanation but no button — no action would change it", async () => {
    serve(
      comment([
        row({
          reason: "task_gated",
          hint: "this task is frozen by an armed human gate — they won't pick it up until the gate is answered",
        }),
      ]),
    );
    mount();

    const plate = await screen.findByTestId("delivery-miss-plate");
    expect(plate).toHaveTextContent("human gate");
    expect(screen.queryByRole("button", { name: /Move to todo|Assign to/ })).not.toBeInTheDocument();
  });
});

describe("missActionFor", () => {
  it("maps each reason to the single action that changes it", () => {
    expect(missActionFor(row({ reason: "status_not_fed" }))).toBe("move_to_todo");
    expect(missActionFor(row({ reason: "not_assignee" }))).toBe("assign");
    expect(missActionFor(row({ reason: "task_gated" }))).toBeNull();
    expect(missActionFor(row({ reason: "task_scheduled" }))).toBeNull();
    expect(missActionFor(row({ reason: "status_not_fed", recipient_kind: "user" }))).toBeNull();
  });
});
