import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { DependencyList } from "../dependency-list";
import { DependencyBadge } from "../dependency-badge";
import { useTaskDependencies } from "@/hooks/use-task-dependencies";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Workspace } from "@/types";

const a = "aaaaaaaa-1111-4111-8111-111111111111";
const b = "bbbbbbbb-2222-4222-8222-222222222222";
const target = { id: b, title: "Target release", project_id: "project-b", status_id: "open" };
let edges: Array<{ id: string; task_id: string; depends_on_task_id: string; dependency_type: string }>;
let custom: ((path: string, init?: RequestInit) => Promise<Response> | undefined) | undefined;
function json(data: unknown, status = 200) { return new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } }); }
function Harness({ id = a }: { id?: string }) {
  const source = useTaskDependencies(id);
  return <section aria-label={id}><DependencyBadge source={source} /><DependencyList taskId={id} source={source} /></section>;
}
// mock: external HTTP boundary; real components, hooks and API parsing are exercised.
beforeEach(() => {
  edges = []; custom = undefined;
  useWorkspaceStore.setState({ currentWorkspace: { id: "workspace" } as Workspace });
  vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input); const result = custom?.(path, init); if (result) return result;
    if (init?.method === "POST") {
      const edge = { id: "edge", task_id: path.split("/").slice(-2)[0]!, ...JSON.parse(String(init.body)) };
      edges.push(edge); return json(edge, 201);
    }
    if (init?.method === "DELETE") { edges = []; return new Response(null, { status: 204 }); }
    if (path.endsWith("/dependencies")) {
      const id = path.split("/").slice(-2)[0];
      return json({ outgoing: edges.filter(e => e.task_id === id), incoming: edges.filter(e => e.depends_on_task_id === id) });
    }
    if (path.endsWith("/statuses")) return json([{ id: "open", category: "in_progress" }]);
    if (path.includes("/workspaces/workspace/tasks?")) return json({ items: [target] });
    if (path.includes("/projects/")) return json({ id: "project-b", workspace_id: "workspace", name: "Other project" });
    return json(target);
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); useWorkspaceStore.setState({ currentWorkspace: null }); });
async function open(query = "release") {
  render(<Harness />); await screen.findByText("No dependencies yet.");
  fireEvent.click(screen.getByRole("button", { name: "Add" }));
  fireEvent.change(screen.getByRole("combobox", { name: "Find task" }), { target: { value: query } });
}
async function choose() { fireEvent.click(await screen.findByRole("option", { name: /Target release/ })); }
describe("Dependency search and mutation behavior", () => {
  it("searches titles across the workspace and supports keyboard selection with project context", async () => {
    await open(); await screen.findByRole("option", { name: /#bbbbbbbb Target release Other project/ });
    const input = screen.getByRole("combobox", { name: "Find task" });
    fireEvent.keyDown(input, { key: "ArrowDown" }); fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByText(/Selected:.*Target release/)).toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(c => String(c[0]).includes("/workspaces/workspace/tasks?search=release"))).toBe(true);
    expect(edges).toHaveLength(0);
  });
  it.each(["#bbbbbbbb", b])("resolves %s without title search", async query => {
    await open(query); await choose();
    expect(vi.mocked(fetch).mock.calls.some(c => String(c[0]).includes("/workspaces/workspace/tasks?"))).toBe(false);
    expect(vi.mocked(fetch).mock.calls.some(c => String(c[0]) === (query.startsWith("#") ? "/api/v1/tasks/by-short-id/bbbbbbbb" : `/api/v1/tasks/${b}`))).toBe(true);
  });
  it("shows ambiguous ID and search errors, empty results, and refuses self/foreign targets", async () => {
    custom = path => path.includes("by-short-id") ? Promise.resolve(json({ message: "ambiguous short ID" }, 400)) : undefined;
    await open("#bbbbbb"); await screen.findByText(/short ID is ambiguous/);
    const input = screen.getByRole("combobox", { name: "Find task" });
    custom = path => path.includes("/workspaces/") ? Promise.resolve(json({ items: [] })) : undefined;
    fireEvent.change(input, { target: { value: "missing" } }); await screen.findByText("No matching tasks.");
    custom = path => path.includes("/tasks/") && !path.endsWith("/dependencies") ? Promise.resolve(json({ ...target, id: a })) : undefined;
    fireEvent.change(input, { target: { value: a } }); await screen.findByText(/cannot depend on itself/);
    custom = path => path.includes("/projects/") ? Promise.resolve(json({ workspace_id: "foreign", name: "Hidden foreign project" })) : undefined;
    fireEvent.change(input, { target: { value: b } }); await screen.findByText(/current workspace/);
    expect(screen.queryByText(/Hidden foreign/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add Dependency" })).toBeDisabled();
  });
  it("aborts obsolete search and ignores a late response after another target was selected", async () => {
    let finish!: (r: Response) => void; let signal: AbortSignal | null | undefined;
    custom = (path, init) => path.includes("search=old") ? new Promise(resolve => { finish = resolve; signal = init?.signal; }) : undefined;
    await open("old"); await waitFor(() => expect(finish).toBeDefined());
    fireEvent.change(screen.getByRole("combobox", { name: "Find task" }), { target: { value: "release" } });
    await choose(); expect(signal?.aborted).toBe(true);
    await act(async () => finish(json({ items: [{ ...target, id: a, title: "Wrong late target" }] })));
    expect(screen.queryByText(/Wrong late target/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add Dependency" }));
    await waitFor(() => expect(edges[0]?.depends_on_task_id).toBe(b));
  });
  it.each([['depends_on', a, b, 'blocks'], ['blocks', b, a, 'blocks'], ['related', a, b, 'relates_to']])("creates %s with the existing API direction and refreshes both sources", async (kind, owner, dependsOn, type) => {
    await open(); await choose();
    fireEvent.change(screen.getByRole("combobox", { name: "Relationship type" }), { target: { value: kind } });
    render(<Harness id={b} />); await waitFor(() => expect(screen.getAllByText("0 · 0 open")).toHaveLength(2));
    fireEvent.click(screen.getByRole("button", { name: "Add Dependency" }));
    await waitFor(() => expect(edges).toEqual([{ id: "edge", task_id: owner, depends_on_task_id: dependsOn, dependency_type: type }]));
    await waitFor(() => expect(screen.queryAllByText("0 · 0 open")).toHaveLength(0));
    const gets = vi.mocked(fetch).mock.calls.filter(c => c[1]?.method === "GET").map(c => String(c[0]));
    expect(gets.filter(p => p === `/api/v1/tasks/${a}/dependencies`).length).toBeGreaterThan(1);
    expect(gets.filter(p => p === `/api/v1/tasks/${b}/dependencies`).length).toBeGreaterThan(1);
  });
  it("keeps counts unchanged on POST refusal", async () => {
    await open(); await choose();
    custom = (_path, init) => init?.method === "POST" ? Promise.resolve(json({ message: "Not permitted" }, 403)) : undefined;
    fireEvent.click(screen.getByRole("button", { name: "Add Dependency" }));
    await screen.findByText("Failed to add dependency."); expect(edges).toHaveLength(0); expect(screen.getByText("0 · 0 open")).toBeInTheDocument();
  });
  it("requires deletion confirmation, keeps both sides on refusal, and deletes via the incoming owner", async () => {
    edges = [{ id: "edge", task_id: b, depends_on_task_id: a, dependency_type: "blocks" }];
    render(<><Harness /><Harness id={b} /></>);
    const current = within(screen.getByRole("region", { name: a }));
    await current.findByText("1 · 0 open");
    fireEvent.click(current.getByRole("button", { name: "Remove dependency" }));
    expect(edges).toHaveLength(1);
    fireEvent.click(current.getByRole("button", { name: "Cancel" }));
    expect(vi.mocked(fetch).mock.calls.some(c => c[1]?.method === "DELETE")).toBe(false);
    fireEvent.click(current.getByRole("button", { name: "Remove dependency" }));
    custom = (_path, init) => init?.method === "DELETE" ? Promise.resolve(json({}, 403)) : undefined;
    fireEvent.click(current.getByRole("button", { name: "Remove" }));
    await current.findByText("Failed to remove dependency."); expect(edges).toHaveLength(1);
    custom = undefined; fireEvent.click(current.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(screen.getAllByText("0 · 0 open")).toHaveLength(2));
    expect(vi.mocked(fetch).mock.calls.filter(c => c[1]?.method === "DELETE").every(c => String(c[0]) === `/api/v1/tasks/${b}/dependencies/edge`)).toBe(true);
  });
  it("shows a search refusal and cannot select restricted task metadata", async () => {
    custom = path => path.includes("/workspaces/") ? Promise.resolve(json({}, 403)) : undefined;
    await open(); await screen.findByText("Could not search tasks.");
    expect(within(screen.getByRole("listbox", { name: "Matching tasks" })).queryByRole("option")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add Dependency" })).toBeDisabled();
  });
  it("clears selection on workspace change and ignores the previous workspace response", async () => {
    await open(); await choose();
    act(() => useWorkspaceStore.setState({ currentWorkspace: { id: "another" } as Workspace }));
    await waitFor(() => expect(screen.queryByText(/Selected:/)).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Add Dependency" })).toBeDisabled();
  });
});
