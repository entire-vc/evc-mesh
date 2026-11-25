// U5: the statistics widget pinned to a project's tab and to an agent's page. Proven at the HTTP boundary: every
// usage read carries the pinned id, «Subscription limits» is never asked even for an admin, and «Open in
// statistics» lands on /stats with the same filter.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { useAuthStore } from "@/stores/auth";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { AgentRuntimeTabs, parseAgentTab } from "@/components/agent-runtime-tabs";
import { ProjectStatsPage } from "@/pages/project-stats";
import { clearAgentUsageCache } from "../data";
import { useCapsStore } from "../stats-body";
import { stateFromSearch } from "../state";
import { usageFixture } from "./fixtures";

const ws = { id: "ws", slug: "test" } as NonNullable<ReturnType<typeof useWorkspaceStore.getState>["currentWorkspace"]>;
const project = (id: string, slug: string) => ({ id, slug, name: `Project ${slug}` }) as never;

let fetcher: ReturnType<typeof vi.fn>;
const reads = () => fetcher.mock.calls.map(([url]) => new URL(String(url), "http://localhost"));

function Where() {
  const loc = useLocation();
  return <output data-testid="where">{`${loc.pathname}${loc.search}`}</output>;
}

beforeEach(() => {
  clearAgentUsageCache();
  useAuthStore.setState({ user: { id: "admin" } as never });
  useWorkspaceStore.setState({ currentWorkspace: ws });
  // An admin: limits and «Pool / account» are available, so their absence below is the pin's doing.
  useCapsStore.setState({ byWs: {} });
  useCapsStore.getState().set("ws", usageFixture().capabilities);
  // mock: external boundary — browser HTTP; the API helper, data hooks and widget stay real.
  fetcher = vi.fn(async (_url: RequestInfo | URL) => new Response(JSON.stringify(usageFixture())));
  vi.stubGlobal("fetch", fetcher);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderProjectTab(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="w/:wsSlug/p/:projectSlug/stats" element={<ProjectStatsPage />} />
        <Route path="w/:wsSlug/stats" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("project tab: the widget pinned to this project", () => {
  it("reads only this project, by agent, and keeps the pin after a row drilldown", async () => {
    useProjectStore.setState({ currentProject: project("proj-alpha", "alpha") });
    renderProjectTab("/w/test/p/alpha/stats");
    await screen.findByTestId("usage-table");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Project alpha");
    fireEvent.click(screen.getAllByRole("button", { name: "Only this" })[0]!);
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    for (const q of reads()) {
      expect(q.pathname).toBe("/api/v1/workspaces/ws/analytics/usage");
      expect(q.searchParams.getAll("project_id")).toEqual(["proj-alpha"]);
      expect(q.searchParams.get("group_by")).toBe("agent");
    }
  });

  it("asks no limits for an admin and offers no «Subscription limits»", async () => {
    useProjectStore.setState({ currentProject: project("proj-alpha", "alpha") });
    renderProjectTab("/w/test/p/alpha/stats");
    await screen.findByTestId("usage-table");
    expect(reads().some((q) => q.pathname.endsWith("/quota"))).toBe(false);
    expect(screen.queryByText(/Subscription limits/)).toBeNull();
    expect(screen.queryByTestId("quota-panel")).toBeNull();
  });

  it("«Open in statistics» lands on /stats with the project filter", async () => {
    useProjectStore.setState({ currentProject: project("proj-alpha", "alpha") });
    renderProjectTab("/w/test/p/alpha/stats");
    await screen.findByTestId("usage-table");
    fireEvent.click(screen.getByRole("button", { name: "Open in statistics" }));
    const where = (await screen.findByTestId("where")).textContent!;
    const url = new URL(where, "http://localhost");
    expect(url.pathname).toBe("/w/test/stats");
    const opened = stateFromSearch(url.searchParams);
    expect(opened.f).toEqual({ project: ["proj-alpha"] });
    expect(opened.g).toBe("agent");
    expect(opened.m).toBe("cost");
  });

  it("an empty period names this project and widens only the period, keeping the pin", async () => {
    fetcher.mockImplementation(async () => {
      const d = usageFixture();
      d.rows = []; d.series = []; d.totals = { value: null, turns: 0, turns_unknown: 0 };
      return new Response(JSON.stringify(d));
    });
    useProjectStore.setState({ currentProject: project("proj-alpha", "alpha") });
    renderProjectTab("/w/test/p/alpha/stats");
    const empty = await screen.findByTestId("stats-empty");
    expect(empty.textContent).toContain("No agent worked on this project in this period.");
    expect(empty.textContent).not.toMatch(/another project|all projects/);
    const before = fetcher.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Show 30 days" }));
    await waitFor(() => expect(fetcher.mock.calls.length).toBeGreaterThan(before));
    const widened = reads().slice(before);
    expect(widened.every((q) => q.searchParams.getAll("project_id").join() === "proj-alpha")).toBe(true);
    expect(widened[widened.length - 1]!.searchParams.get("period")).toBe("30d");
  });

  it("a failed read shows the error with a retry that reads the pinned project again", async () => {
    fetcher.mockImplementation(async () => new Response(JSON.stringify({ message: "boom" }), { status: 500 }));
    useProjectStore.setState({ currentProject: project("proj-alpha", "alpha") });
    renderProjectTab("/w/test/p/alpha/stats");
    const retry = await screen.findByRole("button", { name: /Retry/i });
    expect(screen.queryByTestId("usage-table")).toBeNull();
    fetcher.mockImplementation(async () => new Response(JSON.stringify(usageFixture())));
    fireEvent.click(retry);
    await screen.findByTestId("usage-table");
    expect(reads().every((q) => q.searchParams.getAll("project_id").join() === "proj-alpha")).toBe(true);
  });

  it("never reads while the bound project is another one (stale store from the previous visit)", async () => {
    useProjectStore.setState({ currentProject: project("proj-beta", "beta") });
    renderProjectTab("/w/test/p/alpha/stats");
    await new Promise((r) => setTimeout(r, 20));
    expect(fetcher).not.toHaveBeenCalled();
    expect(screen.queryByTestId("pinned-agent-usage")).toBeNull();
  });
});

describe("agent page: the Statistics tab pinned to this agent", () => {
  const agent = { id: "agent-linus" };

  function renderAgentTabs(initialTab?: ReturnType<typeof parseAgentTab>) {
    return render(
      <MemoryRouter initialEntries={["/w/test/team/agent/linus"]}>
        <Routes>
          <Route
            path="w/:wsSlug/team/agent/:slug"
            element={
              <AgentRuntimeTabs
                agent={agent}
                workspaceId="ws"
                canReadRuntime={false}
                initialTab={initialTab}
                showStats
                profile={<p>profile-content</p>}
                access={<p>grants</p>}
              />
            }
          />
          <Route path="w/:wsSlug/stats" element={<Where />} />
        </Routes>
      </MemoryRouter>,
    );
  }

  it("opens on ?tab=stats and reads only this agent, by project, without limits", async () => {
    renderAgentTabs(parseAgentTab("stats"));
    expect(screen.getByRole("tab", { name: "Statistics" })).toHaveAttribute("aria-selected", "true");
    await screen.findByTestId("usage-table");
    expect(reads().length).toBeGreaterThan(0);
    for (const q of reads()) {
      expect(q.pathname).toBe("/api/v1/workspaces/ws/analytics/usage");
      expect(q.searchParams.getAll("agent_id")).toEqual(["agent-linus"]);
      expect(q.searchParams.get("project_id")).toBeNull();
      expect(q.searchParams.get("group_by")).toBe("project");
    }
    fireEvent.click(screen.getByRole("button", { name: "Open in statistics" }));
    const url = new URL((await screen.findByTestId("where")).textContent!, "http://localhost");
    expect(stateFromSearch(url.searchParams).f).toEqual({ agent: ["agent-linus"] });
  });

  it("an empty period and a failed read on the agent tab stay about this agent", async () => {
    fetcher.mockImplementation(async () => {
      const d = usageFixture();
      d.rows = []; d.series = []; d.totals = { value: null, turns: 0, turns_unknown: 0 };
      return new Response(JSON.stringify(d));
    });
    const { unmount } = renderAgentTabs("stats");
    const empty = await screen.findByTestId("stats-empty");
    expect(empty.textContent).toContain("This agent made no turns in this period.");
    expect(empty.textContent).not.toMatch(/another project|all projects/);
    unmount();
    clearAgentUsageCache();
    fetcher.mockImplementation(async () => new Response(JSON.stringify({ message: "boom" }), { status: 500 }));
    renderAgentTabs("stats");
    await screen.findByRole("button", { name: /Retry/i });
    expect(reads().every((q) => q.searchParams.getAll("agent_id").join() === "agent-linus")).toBe(true);
  });

  it("reads nothing until the Statistics tab is opened", async () => {
    renderAgentTabs();
    expect(screen.getByText("profile-content")).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 20));
    expect(fetcher).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("tab", { name: "Statistics" }));
    await screen.findByTestId("usage-table");
    expect(reads().every((q) => q.searchParams.get("agent_id") === "agent-linus")).toBe(true);
  });

  it("has no Statistics tab where the host does not ask for it (an invited agent)", () => {
    render(
      <MemoryRouter>
        <AgentRuntimeTabs agent={agent} workspaceId="ws" canReadRuntime={false} initialTab="stats" profile={<p>profile-content</p>} access={null} />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("tab", { name: "Statistics" })).toBeNull();
    expect(screen.getByText("profile-content")).toBeInTheDocument();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("parses only known tabs from the URL", () => {
    expect(parseAgentTab("stats")).toBe("stats");
    expect(parseAgentTab("route")).toBe("route");
    expect(parseAgentTab("nope")).toBeUndefined();
    expect(parseAgentTab(null)).toBeUndefined();
  });
});
