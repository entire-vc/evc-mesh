import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { DependencyList } from "../dependency-list";
import { DependencyBadge } from "../dependency-badge";
import { useTaskDependencies } from "@/hooks/use-task-dependencies";
import type { TaskDependency } from "@/types";

const current = "00000000-0000-4000-8000-000000000001";
const other = "11111111-0000-4000-8000-000000000001";
const newTask = "22222222-0000-4000-8000-000000000001";
function edge(id: string, type = "blocks", task = current, dependsOn = other): TaskDependency {
  return { id, task_id: task, depends_on_task_id: dependsOn, dependency_type: type, created_at: "" } as TaskDependency;
}
function response(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
}
let outgoing: TaskDependency[];
let incoming: TaskDependency[];
let category: string;
let failList: boolean;
let failTask: boolean;
let deleted: string[];
const openTask = vi.fn();
const changed = vi.fn();
let override: ((path: string, init?: RequestInit) => Promise<Response> | undefined) | undefined;

function Harness({ id = current }: { id?: string }) {
  const source = useTaskDependencies(id);
  return <><DependencyBadge source={source} /><DependencyList key={id} taskId={id} source={source} onOpenTask={openTask} onChanged={changed} /></>;
}
beforeEach(() => {
  outgoing = [edge("blocker")]; incoming = []; category = "in_progress";
  failList = false; failTask = false; deleted = []; override = undefined;
  openTask.mockReset(); changed.mockReset();
  // mock: external HTTP boundary — real API parsing, loading and components.
  vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input);
    const custom = override?.(path, init);
    if (custom) return custom;
    if (init?.method === "DELETE") {
      deleted.push(path); incoming = []; outgoing = [];
      return new Response(null, { status: 204 });
    }
    if (path.endsWith("/dependencies")) return response(failList ? { message: "offline" } : { outgoing, incoming }, failList ? 503 : 200);
    if (path.endsWith("/statuses")) return response([{ id: "status", project_id: "related-project", name: `Related ${category}`, category, color: "#f00" }]);
    return response(failTask ? { message: "forbidden" } : { id: path.split("/").slice(-1)[0], title: "Related title", project_id: "related-project", status_id: "status", assignee_name: "Alex", assignee_id: "alex" }, failTask ? 403 : 200);
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("DependencyList shared source", () => {
  it("shows directed groups in order, preserves hierarchy, and opens the related task", async () => {
    outgoing.push(edge("parent", "is_child_of"), edge("related", "relates_to"));
    incoming = [edge("blocked", "blocks", other, current), edge("child", "is_child_of", newTask, current)];
    render(<Harness />);
    await screen.findByText("5 · 1 open");
    const headings = screen.getAllByText(/^(Blocked by|Blocks|Relates to|Child of|Parent of)$/);
    expect(headings.map(h => h.textContent)).toEqual(["Blocked by", "Blocks", "Relates to", "Child of", "Parent of"]);
    fireEvent.click(screen.getAllByRole("button", { name: /#11111111 Related title/ })[0]!);
    expect(openTask).toHaveBeenCalledWith(other);
    expect(screen.getAllByText("Alex")).toHaveLength(5);
    expect(screen.getAllByText("Open blocker")).toHaveLength(1);
  });

  it.each(["done", "cancelled"])("treats %s blockers as closed", async terminal => {
    category = terminal;
    render(<Harness />);
    await screen.findByText("1 · 0 open");
    expect(screen.queryByText("Open blocker")).not.toBeInTheDocument();
    expect(screen.getByText(`Related ${terminal}`)).toBeInTheDocument();
  });

  it("uses a real empty state with add, and keeps failed requests distinct with retry", async () => {
    failList = true;
    render(<Harness />);
    await screen.findByText("Could not load dependencies.");
    expect(screen.getByText("Unavailable")).toBeInTheDocument();
    expect(screen.queryByText("No dependencies yet.")).not.toBeInTheDocument();
    failList = false; outgoing = [];
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("No dependencies yet.");
    expect(screen.getByText("0 · 0 open")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(screen.getByPlaceholderText(/550e8400/)).toBeInTheDocument();
  });

  it("does not report unknown blocker metadata as zero, and retry hydrates it", async () => {
    failTask = true;
    render(<Harness />);
    await screen.findByText("1 · 1 unknown");
    expect(screen.getByText("Blocker status unknown")).toBeInTheDocument();
    expect(screen.getByText("Status unavailable")).toBeInTheDocument();
    failTask = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("1 · 1 open");
    expect(screen.queryByText("Blocker status unknown")).not.toBeInTheDocument();
  });

  it("confirms incoming deletion on the owning task and updates both list and badge", async () => {
    outgoing = []; incoming = [edge("owned-elsewhere", "blocks", other, current)];
    render(<Harness />);
    await screen.findByText("1 · 0 open");
    fireEvent.click(screen.getByRole("button", { name: "Remove dependency" }));
    expect(deleted).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(deleted).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Remove dependency" }));
    fireEvent.click(screen.getByRole("button", { name: /^Remove$/ }));
    await screen.findByText("0 · 0 open");
    expect(deleted).toEqual([`/api/v1/tasks/${other}/dependencies/owned-elsewhere`]);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Related title")).not.toBeInTheDocument();
  });

  it("keeps the edge after a failed deletion and clears the error on a successful retry", async () => {
    override = (_path, init) => init?.method === "DELETE" ? Promise.resolve(response({ message: "offline" }, 503)) : undefined;
    render(<Harness />);
    await screen.findByText("1 · 1 open");
    fireEvent.click(screen.getByRole("button", { name: "Remove dependency" }));
    fireEvent.click(screen.getByRole("button", { name: /^Remove$/ }));
    await screen.findByRole("alert");
    expect(screen.getByText("1 · 1 open")).toBeInTheDocument();
    expect(deleted).toHaveLength(0);
    override = undefined;
    fireEvent.click(screen.getByRole("button", { name: /^Remove$/ }));
    await screen.findByText("0 · 0 open");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(changed).toHaveBeenCalledTimes(1);
  });

  it("ignores an old dependency response after task navigation", async () => {
    let finish!: (r: Response) => void;
    override = path => path === `/api/v1/tasks/${current}/dependencies` ? new Promise(resolve => { finish = resolve; }) : path === `/api/v1/tasks/${newTask}/dependencies` ? Promise.resolve(response({ outgoing: [], incoming: [] })) : undefined;
    const { rerender } = render(<Harness />);
    expect(screen.getByText("Loading dependencies...")).toBeInTheDocument();
    rerender(<Harness id={newTask} />);
    await screen.findByText("0 · 0 open");
    await act(async () => finish(response({ outgoing: [edge("old")], incoming: [] })));
    expect(screen.getByText("No dependencies yet.")).toBeInTheDocument();
    expect(screen.queryByText("Related title")).not.toBeInTheDocument();
  });

  it("ignores stale hydration after task navigation", async () => {
    let finish!: (r: Response) => void;
    override = path => path === `/api/v1/tasks/${other}` ? new Promise(resolve => { finish = resolve; }) : path === `/api/v1/tasks/${newTask}/dependencies` ? Promise.resolve(response({ outgoing: [], incoming: [] })) : undefined;
    const { rerender } = render(<Harness />);
    await screen.findByText("1 · checking");
    rerender(<Harness id={newTask} />);
    await screen.findByText("0 · 0 open");
    await act(async () => finish(response({ id: other, project_id: "old-project", status_id: "status", title: "Stale title" })));
    expect(screen.getByText("No dependencies yet.")).toBeInTheDocument();
    expect(screen.queryByText("Stale title")).not.toBeInTheDocument();
  });

  it("ignores a pending mutation after its task's list unmounts", async () => {
    let finish!: (r: Response) => void;
    override = (path, init) => init?.method === "POST" ? new Promise(resolve => { finish = resolve; }) : path === `/api/v1/tasks/${newTask}/dependencies` ? Promise.resolve(response({ outgoing: [], incoming: [] })) : undefined;
    const { rerender } = render(<Harness />);
    await screen.findByText("1 · 1 open");
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    fireEvent.change(screen.getByLabelText("Task ID (UUID)"), { target: { value: newTask } });
    fireEvent.click(screen.getByRole("button", { name: "Add Dependency" }));
    rerender(<Harness id={newTask} />);
    await screen.findByText("0 · 0 open");
    await act(async () => finish(response(edge("created-after-navigation"), 201)));
    expect(changed).not.toHaveBeenCalled();
    expect(screen.getByText("No dependencies yet.")).toBeInTheDocument();
  });

  it("caps hydration at four requests and caches repeated tasks and project statuses", async () => {
    outgoing = Array.from({ length: 9 }, (_, i) => edge(`edge-${i}`, "blocks", current, `task-${i % 6}`));
    let active = 0, peak = 0;
    const pending: (() => void)[] = [];
    override = path => {
      if (!path.includes("/tasks/task-")) return undefined;
      active++; peak = Math.max(peak, active);
      return new Promise(resolve => pending.push(() => { active--; resolve(response({ id: path.split("/").slice(-1)[0], title: "Hydrated", project_id: "related-project", status_id: "status" })); }));
    };
    render(<Harness />);
    await waitFor(() => expect(pending).toHaveLength(4));
    await act(async () => { pending.splice(0).forEach(resolve => resolve()); });
    await waitFor(() => expect(pending).toHaveLength(2));
    await act(async () => { pending.splice(0).forEach(resolve => resolve()); });
    await screen.findByText("9 · 9 open");
    expect(peak).toBe(4);
    const calls = vi.mocked(fetch).mock.calls.map(c => String(c[0]));
    expect(calls.filter(p => p.includes("/tasks/task-"))).toHaveLength(6);
    expect(calls.filter(p => p.endsWith("/statuses"))).toHaveLength(1);
    expect(within(screen.getByText("Blocked by").parentElement!).getAllByRole("button", { name: /Hydrated/ })).toHaveLength(9);
  });
});
