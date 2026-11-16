import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import type { RealtimeEvent } from "@entire-vc/mesh-theme-sdk";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { isTaskEvent, taskQueryParams, useTasks, useTriageCount, useTriageQueue } from "../hooks";

// The network and the socket are the only mocks.
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
const listeners = new Set<(e: RealtimeEvent) => void>();
vi.mock("@/core/primitives/use-realtime", () => ({
  useRealtime: (_topic: unknown, on: (e: RealtimeEvent) => void) => {
    listeners.add(on);
    return { connected: true };
  },
}));
import { api } from "@/lib/api";

const task = (id: string, title = `Task ${id}`) => ({ id, title, priority: "high", project_id: "p1", created_at: "2026-01-01T00:00:00Z" });
const emit = (e: RealtimeEvent) => act(async () => {
  listeners.forEach((l) => l(e));
  await new Promise((r) => setTimeout(r, 300));
});

beforeEach(() => {
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" } as never] });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  listeners.clear();
});

describe("taskQueryParams", () => {
  it("maps every TaskQuery field onto the server's names and caps the limit", () => {
    expect(
      taskQueryParams({
        scope: "project", projectId: "p1", statusCategories: ["todo", "review"], priority: ["urgent"],
        dueBefore: "2026-12-01", gated: true, sort: "priority", limit: 500,
      }),
    ).toEqual({
      scope: "project", project_id: "p1", status_category: "todo,review", priority: "urgent",
      due_before: "2026-12-01", gated: "true", sort: "priority", limit: 50,
    });
    expect(taskQueryParams({ scope: "mine" })).toEqual({
      scope: "mine", project_id: undefined, status_category: undefined, priority: undefined,
      due_before: undefined, gated: undefined, sort: undefined, limit: 50,
    });
  });
});

describe("isTaskEvent", () => {
  it("accepts a task.* type and a bus event whose subject is task.*, nothing else", () => {
    expect(isTaskEvent({ type: "task.updated", data: {}, at: "1" })).toBe(true);
    expect(isTaskEvent({ type: "status_change", data: { subject: "task.moved" }, at: "1" })).toBe(true);
    expect(isTaskEvent({ type: "custom", data: { subject: "comment.created" }, at: "1" })).toBe(false);
    expect(isTaskEvent({ type: "summary", data: {}, at: "1" })).toBe(false);
  });
});

function Triage(props: { limit?: number; projectId?: string; hardGatesOnly?: boolean }) {
  const r = useTriageQueue(props);
  if (r.state === "loading") return <p>loading</p>;
  if (r.state === "error") return <p>error</p>;
  return <p>{`${r.value.items.map((t) => `${t.title}@${r.value.projectNames[t.project_id]}`).join(",")} of ${r.value.total}`}</p>;
}

describe("useTriageQueue", () => {
  it("asks the server for the page and filters, and shows total_count, not the page length", async () => {
    vi.mocked(api).mockResolvedValue({ items: [task("a"), task("b")], total_count: 9 });
    render(<Triage limit={2} projectId="p1" hardGatesOnly />);
    await screen.findByText("Task a@Core,Task b@Core of 9");
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/ws1/triage", {
      params: { page_size: 2, project_id: "p1", hard_gates_only: "true" },
    });
  });

  it("a task moved elsewhere disappears without a reload; the rows stay on screen while it reloads", async () => {
    vi.mocked(api).mockResolvedValueOnce({ items: [task("a"), task("b")], total_count: 2 });
    render(<Triage />);
    await screen.findByText("Task a@Core,Task b@Core of 2");
    vi.mocked(api).mockResolvedValueOnce({ items: [task("b")], total_count: 1 });
    await emit({ type: "status_change", data: { subject: "task.moved", task_id: "a" }, at: "2" });
    await waitFor(() => expect(screen.getByText("Task b@Core of 1")).toBeTruthy());
    expect(screen.queryByText("loading")).toBeNull();
  });

  it("an unrelated event does not reload; a failed reload keeps the last answer", async () => {
    vi.mocked(api).mockResolvedValueOnce({ items: [task("a")], total_count: 1 });
    render(<Triage />);
    await screen.findByText("Task a@Core of 1");
    await emit({ type: "summary", data: { subject: "agent.summary" }, at: "2" });
    expect(api).toHaveBeenCalledTimes(1);
    vi.mocked(api).mockRejectedValueOnce(new Error("down"));
    await emit({ type: "task.updated", data: {}, at: "3" });
    expect(api).toHaveBeenCalledTimes(2);
    expect(screen.getByText("Task a@Core of 1")).toBeTruthy();
  });

  it("a failed first read is an error with a working retry, not an empty queue", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("down"));
    let retry: (() => void) | undefined;
    function Probe() {
      const r = useTriageQueue();
      if (r.state === "error") retry = r.retry;
      return <p>{r.state === "ready" ? `n=${r.value.items.length}` : r.state}</p>;
    }
    render(<Probe />);
    await screen.findByText("error");
    vi.mocked(api).mockResolvedValueOnce({ items: [task("a")], total_count: 1 });
    act(() => retry!());
    await screen.findByText("n=1");
  });
});

describe("useTriageCount", () => {
  it("reads one row and returns the queue's total_count", async () => {
    vi.mocked(api).mockResolvedValue({ items: [task("a")], total_count: 37 });
    function Count() {
      const r = useTriageCount({ projectId: "p1" });
      return <p>{r.state === "ready" ? `count=${r.value}` : r.state}</p>;
    }
    render(<Count />);
    await screen.findByText("count=37");
    expect(api).toHaveBeenCalledWith("/api/v1/workspaces/ws1/triage", { params: { page_size: 1, project_id: "p1" } });
  });
});

describe("useTasks", () => {
  it("re-reads when a task event arrives and when the query changes", async () => {
    vi.mocked(api).mockImplementation(async (_url: string, opts?: { params?: Record<string, unknown> }) => ({
      items: [task(String(opts?.params?.sort ?? "updated"))],
      total_count: 1,
    }));
    function List({ sort }: { sort: "priority" | "due_date" }) {
      const r = useTasks({ scope: "workspace", sort });
      return <p>{r.state === "ready" ? r.value.items[0]!.id : r.state}</p>;
    }
    const view = render(<List sort="priority" />);
    await screen.findByText("priority");
    view.rerender(<List sort="due_date" />);
    await screen.findByText("due_date");
    const before = vi.mocked(api).mock.calls.length;
    await emit({ type: "status_change", data: { subject: "task.created" }, at: "4" });
    expect(vi.mocked(api).mock.calls.length).toBe(before + 1);
  });
});
