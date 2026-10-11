import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router";
import { TimelinePage } from "@/pages/timeline";
import { useProjectStore } from "@/stores/project";
import type { Project } from "@/types";
import type { DependencyGraph, DependencyNode } from "@/lib/dependency-graph";

const node = (id: string, extra = {}): DependencyNode => ({ id, title: `Task ${id}`, availability: "available",
  project_id: "project", status_category: "todo", status_name: "Todo", priority: "high", blockers: 0,
  unfinished_blockers: 0, unknown_blockers: 0, dependents: 0, ...extra });
const branch = (ids: string[] = []) => ({ ids, total_count: ids.length, completed_count: 0, unknown_count: 0, page: 1, has_more: false });
function graph(focus = "center"): DependencyGraph {
  return { mode: "focus", focus, tasks: [node(focus, { blockers: 1, unfinished_blockers: 1 }), node("blocker"), node("dependent")],
    dependencies: [ { id: "e1", task_id: focus, depends_on_task_id: "blocker", dependency_type: "blocks" },
      { id: "e2", task_id: "dependent", depends_on_task_id: focus, dependency_type: "blocks" } ],
    before: branch(["blocker"]), after: branch(["dependent"]), relations: branch(),
    total_count: 3, scope_total_count: 3, completed_count: 0, unknown_count: 0, page: 1, page_size: 50,
    has_more: false, complete: true, visibility_complete: true, revision: "r1", cycle_ids: [], cycle_count: 0 };
}
function Navigation() {
  const location = useLocation(), navigate = useNavigate();
  return <><output data-testid="url">{location.search}</output><button onClick={() => navigate(-1)}>Browser Back</button>
    <button onClick={() => navigate("?focus=new")}>New focus</button></>;
}
function show(query = "?focus=center") {
  return render(<MemoryRouter initialEntries={[`/w/ws/p/project/timeline${query}`]}><Navigation />
    <Routes><Route path="/w/:wsSlug/p/:projectSlug/timeline" element={<TimelinePage />} /></Routes></MemoryRouter>);
}
// mock: external boundary — browser HTTP; actual transport, stores, router and UI execute.
const fetchMock = vi.fn<typeof fetch>();
const respond = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status }));
beforeEach(() => {
  useProjectStore.setState({ currentProject: { id: "project", slug: "project", name: "Project" } as Project });
  fetchMock.mockReset(); vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockImplementation(async input => graphResponse(new URL(String(input), "http://local")));
});
afterEach(() => vi.unstubAllGlobals());
function graphResponse(url: URL) { return respond(graph(url.searchParams.get("focus") || "center")); }

describe("dependencies overview and focus", () => {
  it("walks 201 tasks past the first page while bounding the overview DOM to 50", async () => {
    fetchMock.mockImplementation(input => {
      const url = new URL(String(input), "http://local");
      const page = Number(url.searchParams.get("page")), g = graph();
      g.mode = "overview"; delete g.focus; g.page = page; g.total_count = 201;
      g.tasks = Array.from({ length: Math.min(50, 201 - (page - 1) * 50) }, (_, i) => node(String((page - 1) * 50 + i)));
      g.has_more = page < 5; g.complete = !g.has_more;
      return respond(g);
    });
    const { container } = show("?page=5");
    await waitFor(() => expect(screen.getByText(/201 of 201 matching tasks loaded/)).toBeVisible());
    expect(container.querySelectorAll("[data-dependency-node]")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Task 200" })).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(5);
    expect(fetchMock.mock.calls.slice(1).every(([input]) => String(input).includes("revision=r1"))).toBe(true);
  });
  it("expands only the requested branch and retains its earlier neighbors", async () => {
    fetchMock.mockImplementation(input => {
      const url = new URL(String(input), "http://local"), g = graph();
      const page = Number(url.searchParams.get("before_page"));
      g.before = { ...branch(Array.from({ length: page === 1 ? 10 : 1 }, (_, i) => `b${(page - 1) * 10 + i}`)), page, total_count: 11, has_more: page === 1 };
      g.tasks = [node("center", { unfinished_blockers: 11 }), node("dependent"), ...g.before.ids.map(id => node(id))];
      return respond(g);
    });
    const { container } = show();
    fireEvent.click(await screen.findByRole("button", { name: /Show more blockers/ }));
    expect(await screen.findByRole("button", { name: "Task b10" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Task b0" })).toBeVisible();
    expect(container.querySelectorAll("[data-dependency-node]")).toHaveLength(13);
    expect(fetchMock.mock.calls.every(([input]) => String(input).includes("after_page=1"))).toBe(true);
  });
  it("shows redacted unknown boundaries and explicit self-loop diagnostics", async () => {
    fetchMock.mockImplementation(() => {
      const g = graph(); g.tasks.push(node("private", { availability: "restricted", title: "DO NOT SHOW", assignee_name: "SECRET OWNER" }));
      g.tasks[1]!.project_id = "other"; g.tasks[1]!.project_name = "Other project";
      g.tasks[0]!.unknown_blockers = 1; g.before.ids.push("private"); g.before.total_count++;
      g.before.unknown_count = 1; g.visibility_complete = false; g.cycle_count = 1; g.cycle_ids = ["center", "private", ...Array.from({ length: 15 }, (_, i) => `cycle${i}`)];
      g.dependencies.push({ id: "private-cycle", task_id: "center", depends_on_task_id: "private", dependency_type: "blocks" });
      g.dependencies.push({ id: "self", task_id: "center", depends_on_task_id: "center", dependency_type: "blocks" });
      return respond(g);
    });
    show();
    expect(await screen.findByText("There is an unavailable dependency")).toBeVisible();
    expect(screen.getByText("Other project: Other project")).toBeVisible();
    expect(screen.getByText(/blocks itself \(self-loop\)/)).toBeVisible();
    expect(screen.queryByText(/DO NOT SHOW/)).toBeNull(); expect(screen.queryByText("SECRET OWNER")).toBeNull();
    expect(screen.queryByText("No unfinished direct blockers")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Inspect cycle members" }));
    expect(within(screen.getByRole("list", { name: "Cycle members" })).getAllByRole("listitem")).toHaveLength(10);
    fireEvent.click(screen.getByRole("button", { name: "Next 10 members" }));
    expect(within(screen.getByRole("list", { name: "Cycle members" })).getAllByRole("listitem")).toHaveLength(7);
    fireEvent.click(screen.getByRole("button", { name: "Inspect task #cycle14" }));
    await waitFor(() => expect(screen.getByTestId("url").textContent).toContain("focus=cycle14"));
  });
  it("preserves canonical direction, changes focus and restores it with Browser Back", async () => {
    show();
    expect(await within(await screen.findByRole("region", { name: "What blocks this task" })).findByRole("button", { name: "Task blocker" })).toBeVisible();
    expect(within(screen.getByRole("region", { name: "What waits for this task" })).getByRole("button", { name: "Task dependent" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Task blocker" }));
    await waitFor(() => expect(within(screen.getByRole("region", { name: "Selected task" })).getByRole("button", { name: "Task blocker" })).toHaveAttribute("aria-current", "true"));
    fireEvent.click(screen.getByText("Browser Back"));
    await waitFor(() => expect(within(screen.getByRole("region", { name: "Selected task" })).getByRole("button", { name: "Task center" })).toHaveAttribute("aria-current", "true"));
    expect(fetchMock.mock.calls.every(([, init]) => init?.method === "GET")).toBe(true);
  });
  it("retains a filtered-out center and reveals hidden blockers without claiming readiness", async () => {
    show("?focus=center&search=absent");
    expect(await screen.findByText("1 unfinished direct blockers")).toBeVisible();
    expect(screen.getByRole("button", { name: "Task center" })).toBeVisible();
    const before = screen.getByRole("region", { name: "What blocks this task" });
    expect(within(before).queryByRole("button", { name: "Task blocker" })).toBeNull();
    fireEvent.click(within(before).getByRole("button", { name: /hidden by search/ }));
    expect(within(before).getByRole("button", { name: "Task blocker" })).toBeVisible();
  });
  it("restores a completed bridge and its actual links, and keeps hierarchy separate", async () => {
    fetchMock.mockImplementation(input => {
      const url = new URL(String(input), "http://local"), g = graph();
      const expanded = url.searchParams.get("completed") === "true";
      g.tasks = [node("center"), node("parent")];
      g.before = { ...branch(expanded ? ["done"] : []), total_count: 1, completed_count: 1 };
      g.relations = branch(["parent"]); g.complete = expanded;
      g.tasks[0]!.parent_task_id = "parent";
      g.dependencies = [{ id: "parent-link", task_id: "center", depends_on_task_id: "parent", dependency_type: "is_child_of" }];
      if (expanded) {
        g.tasks.push(node("done", { status_category: "done", status_name: "Done" }));
        g.dependencies.push({ id: "done-center", task_id: "center", depends_on_task_id: "done", dependency_type: "blocks" });
      }
      return respond(g);
    });
    show();
    fireEvent.click(await screen.findByRole("button", { name: /completed · expand original links/ }));
    expect(await screen.findByRole("button", { name: "Task done" })).toBeVisible();
    expect(screen.getByText("Parent task")).toBeVisible();
    expect(within(screen.getByRole("region", { name: "What blocks this task" })).queryByText("Task parent")).toBeNull();
  });
  it("rejects stale focus responses and cancels their transport", async () => {
    let finish!: (response: Response) => void;
    fetchMock.mockImplementation(input => String(input).includes("focus=center")
      ? new Promise(resolve => { finish = resolve; }) : respond(graph("new")));
    show();
    fireEvent.click(screen.getByText("New focus"));
    expect(await screen.findByRole("button", { name: "Task new" })).toBeVisible();
    expect(fetchMock.mock.calls[0]![1]!.signal!.aborted).toBe(true);
    finish(new Response(JSON.stringify(graph("center"))));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Task center" })).toBeNull());
  });
  it("restarts overview pagination on 409 and does not keep the invalidated pages", async () => {
    let conflict = true;
    fetchMock.mockImplementation(input => {
      const url = new URL(String(input), "http://local"), g = graph();
      if (url.searchParams.get("page") === "2" && conflict) { conflict = false; return respond({ code: "CONFLICT" }, 409); }
      g.mode = "overview"; delete g.focus; g.tasks = [node(conflict ? "old" : "fresh")]; g.total_count = 201; g.has_more = true; g.complete = false;
      return respond(g);
    });
    show("?page=2");
    expect(await screen.findByText(/Pagination restarted/)).toBeVisible();
    await waitFor(() => expect(screen.getByRole("button", { name: "Task fresh" })).toBeVisible());
    expect(screen.queryByRole("button", { name: "Task old" })).toBeNull();
    await waitFor(() => expect(screen.getByTestId("url").textContent).not.toContain("page=2"));
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("revision=r1"))).toBe(true);
  });
  it("keeps successful overview pages with an explicit partial-data error", async () => {
    fetchMock.mockImplementation(input => {
      const url = new URL(String(input), "http://local"), g = graph();
      if (url.searchParams.get("page") === "2") return respond({}, 503);
      g.mode = "overview"; delete g.focus; g.tasks = [node("first")]; g.total_count = 201; g.has_more = true;
      return respond(g);
    });
    show("?page=2");
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load dependencies");
    expect(screen.getByRole("button", { name: "Task first" })).toBeVisible();
    expect(screen.getByText(/Partial data/)).toBeVisible();
    expect(screen.queryByText("No unfinished direct blockers")).toBeNull();
  });
  it("distinguishes an empty project from an empty search", async () => {
    fetchMock.mockImplementation(input => {
      const url = new URL(String(input), "http://local"), g = graph();
      g.mode = "overview"; delete g.focus; g.tasks = []; g.total_count = 0; g.scope_total_count = 0;
      g.completed_count = url.searchParams.has("search") ? 0 : 2;
      return respond(g);
    });
    show("");
    expect(await screen.findByText("No open tasks")).toBeVisible();
    expect(screen.getByRole("button", { name: /2 completed · expand/ })).toBeVisible();
    fireEvent.change(screen.getByRole("textbox", { name: "Search title or ID" }), { target: { value: "absent" } });
    expect(await screen.findByText("No matching tasks")).toBeVisible();
  });
  it("shows missing focus and retries a transport error", async () => {
    fetchMock.mockImplementationOnce(() => respond({}, 404)).mockImplementationOnce(() => respond(graph()));
    show();
    expect(await screen.findByRole("alert")).toHaveTextContent(/deleted or is unavailable/);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("button", { name: "Task center" })).toBeVisible();
  });
});
