import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { useAuthStore } from "@/stores/auth";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { AnalyticsMetrics } from "@/types";
import { buildRegistry, createViewCache, type WidgetViewsLoader } from "../../../registry";
import { clearAnalyticsCache } from "../use-analytics";
import { formatTokens } from "../ui";
import { descriptors } from "../index";
import { METRIC_OPTIONS } from "../metrics";
import CATALOG from "../../../core-widgets.json";
import { views, type AnalyticsActions } from "../views";

// The network is the only mock: views run the real useAnalytics and its cache.
vi.mock("@/lib/api", () => ({ api: vi.fn(), apiBlob: vi.fn(), getAccessToken: () => null }));
import { api, apiBlob } from "@/lib/api";

const IDS = [
  "metric", "tasks-breakdown", "tasks-timeline", "events-breakdown", "agent-leaderboard",
  "cost-kpis", "cost-by-agent", "cost-top-tasks", "cost-by-project", "cost-by-day",
];

function metrics(over: { task?: Partial<AnalyticsMetrics["task_metrics"]>; event?: Partial<AnalyticsMetrics["event_metrics"]>; cost?: Partial<AnalyticsMetrics["cost_metrics"]> } = {}): AnalyticsMetrics {
  return {
    task_metrics: {
      total: 10,
      by_status_category: { todo: 4, in_progress: 3, done: 3 },
      by_priority: { high: 6, low: 4 },
      created_this_period: 8,
      completed_this_period: 5,
      by_assignee: [
        { assignee_id: "a1", assignee_type: "agent", name: "Linus", count: 6 },
        { assignee_id: "u1", assignee_type: "user", name: "Pat Example", count: 3 },
        { assignee_id: null, assignee_type: "unassigned", name: "", count: 1 },
      ],
      by_project: [
        { project_id: "p1", project_name: "Core", count: 7 },
        { project_id: "p2", project_name: "A project with a very long name that has to be cut", count: 3 },
      ],
      ...over.task,
    },
    agent_metrics: { total_agents: 4, active_agents: 2, tasks_by_agent: [{ agent_id: "a1", agent_name: "Linus", completed: 5 }] },
    event_metrics: { total_events: 12, by_type: { "task.created": 8, "task.moved": 4 }, retained_since: "2026-01-01T00:00:00Z", period_fully_covered: true, ...over.event },
    timeline: [
      { date: "2026-11-12", created: 3, completed: 1 },
      { date: "2026-11-13", created: 5, completed: 4 },
    ],
    cost_metrics: {
      total_cost: 9,
      total_tokens_in: 1500,
      total_tokens_out: 500,
      session_count: 4,
      reported_session_count: 3,
      by_agent: [{ agent_id: "a1", agent_name: "Linus", cost: 9, tokens_in: 1500, tokens_out: 500 }],
      by_project: [{ project_id: "p1", project_name: "Core", cost: 9, tokens_in: 1500, tokens_out: 500 }],
      by_day: [{ date: "2026-11-13", cost: 9, tokens_in: 1500, tokens_out: 500 }],
      top_tasks: [
        { task_id: "tA", task_title: "Task A", cost: 5, tokens_in: 50, tokens_out: 50, session_count: 1 },
        { task_id: "tB", task_title: "Task B", cost: 3, tokens_in: 800, tokens_out: 100, session_count: 2 },
        { task_id: "tC", task_title: "Task C", cost: 1, tokens_in: 400, tokens_out: 100, session_count: 1 },
      ],
      ...over.cost,
    },
  };
}

type Params = Record<string, string>;
const calls = () => vi.mocked(api).mock.calls.map((c) => (c[1] as { params: Params }).params);
const serve = (answer: (p: Params) => AnalyticsMetrics) =>
  vi.mocked(api).mockImplementation(async (_url, opts) => answer((opts?.params ?? {}) as Params) as never);

function view(type: string, settings: Record<string, unknown> = {}, actions: AnalyticsActions = {}): ReactElement {
  const View = views[type as keyof typeof views];
  return <View key={type + JSON.stringify(settings)} instance={{ id: type, type, x: 0, y: 0, w: 6, h: 2, settings }} settings={settings} size={{ w: 6, h: 2 }} actions={actions} />;
}
function mount(...els: ReactElement[]) {
  return render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <Routes>
        <Route path="/w/:wsSlug/dashboard" element={<>{els}</>} />
      </Routes>
    </MemoryRouter>,
  );
}
const rowsOf = (c: HTMLElement) => [...c.querySelectorAll("[data-row]")].map((e) => e.getAttribute("data-row"));

beforeEach(() => {
  clearAnalyticsCache();
  vi.mocked(api).mockReset();
  vi.mocked(apiBlob).mockReset();
  serve(() => metrics());
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useAuthStore.setState({ user: { id: "owner" } as never, isAuthenticated: true });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" } as never] });
});
afterEach(cleanup);

describe("analytics widget group", () => {
  it("registers all ten descriptors, each with a lazily loaded view, a period and a project filter, and only contract actions", async () => {
    const r = buildRegistry(
      import.meta.glob("../index.ts", { eager: true }) as Parameters<typeof buildRegistry>[0],
      true,
      import.meta.glob("../views.tsx") as Record<string, WidgetViewsLoader>,
    );
    expect(r.descriptors.map((d) => d.id)).toEqual(IDS);
    const cache = createViewCache(r);
    for (const d of descriptors) {
      expect(cache.peek(d.id), d.id).toBeUndefined(); // not in the registry chunk: views.tsx loads on first draw
      expect(await cache.load(d.id), d.id).toBe(views[d.id as keyof typeof views]);
      expect(d.settings?.properties.period?.["x-widget"], d.id).toBe("period");
      expect(d.settings?.properties.project?.["x-widget"], d.id).toBe("project-picker");
      for (const a of d.actions) expect(["openTask", "openAgent", "openMention", "retry"], `${d.id}: ${a}`).toContain(a);
      expect(d.size.min.w).toBeLessThanOrEqual(d.size.default.w);
      expect(d.size.default.w).toBeLessThanOrEqual(d.size.max.w);
    }
  });
});

describe("one request per filter", () => {
  it("six analytics widgets with the same settings make exactly one request", async () => {
    const settings = () => ({ period: { days: 30 }, project: "p1" });
    const six = ["tasks-breakdown", "tasks-timeline", "events-breakdown", "agent-leaderboard", "cost-kpis", "cost-by-agent"];
    const { container } = mount(...six.map((t) => view(t, settings())));
    await waitFor(() => expect(screen.getByText("Reported cost")).toBeTruthy());
    expect(container.querySelectorAll("[aria-busy]")).toHaveLength(0);
    expect(api).toHaveBeenCalledTimes(1);
    expect(calls()[0]).toMatchObject({ project_id: "p1" });
  });

  it("positive control: another period is another request", async () => {
    mount(view("tasks-timeline", { period: { days: 30 } }), view("events-breakdown", { period: { days: 7 } }));
    await waitFor(() => expect(screen.getByText("Events by type")).toBeTruthy());
    await waitFor(() => expect(api).toHaveBeenCalledTimes(2));
  });

  it("metric compares with the previous period of equal length; two tiles share both requests", async () => {
    serve((p) => (p.from === "2026-08-31" ? metrics({ task: { completed_this_period: 4, created_this_period: 8 } }) : metrics()));
    mount(view("metric", { metric: "tasks_closed", period: { from: "2026-10-01", to: "2026-10-31" } }), view("metric", { metric: "tasks_created", period: { from: "2026-10-01", to: "2026-10-31" } }));
    await waitFor(() => expect(screen.getAllByTestId("metric-comparison")).toHaveLength(2));
    expect(screen.getByText("+1 (+25%) vs previous period")).toBeTruthy();
    expect(screen.getByText("Same as the previous period")).toBeTruthy();
    expect(api).toHaveBeenCalledTimes(2);
    expect(calls()).toContainEqual({ from: "2026-08-31", to: "2026-09-30T23:59:59Z" });
  });

  it("a preset compares with the span of the same length right before it; two tiles still share it", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-15T12:00:42Z"));
    try {
      mount(view("metric", { metric: "tasks_closed", period: { days: 7 } }), view("metric", { metric: "tasks_created", period: { days: 7 } }));
      await waitFor(() => expect(screen.getAllByTestId("metric-comparison")).toHaveLength(2));
      expect(api).toHaveBeenCalledTimes(2);
      expect(calls()).toContainEqual({ from: "2026-11-08" });
      // 7 days and 12 h ending with the second before 08 November, as the current 08 Nov 00:00 – now:
      // not the whole days 01–07 November (168 h against 180 h), nor anything after 08 Nov 00:00
      expect(calls()).toContainEqual({ from: "2026-10-31T12:00:00Z", to: "2026-11-07T23:59:59Z" });
    } finally {
      vi.useRealTimers();
    }
  });

  // events: the log keeps 90 days, so a period it does not fully cover undercounts
  const partial = { period_fully_covered: false, retained_since: "2026-10-15T00:00:00Z" };
  const eventsTile = () => view("metric", { metric: "events_total", period: { from: "2026-10-01", to: "2026-10-31" } });

  it("an incomplete current period makes no comparison: partial 50 against a complete 100 is not a drop of 50%", async () => {
    serve((p) => (p.from === "2026-08-31" ? metrics({ event: { total_events: 100 } }) : metrics({ event: { total_events: 50, ...partial } })));
    mount(eventsTile());
    await waitFor(() => expect(screen.getByText("No comparison: this period is incomplete")).toBeTruthy());
    expect(screen.getByText("50")).toBeTruthy();
    expect(screen.queryByTestId("metric-comparison")).toBeNull();
    expect(screen.queryByText(/−50/)).toBeNull();
    expect(api).toHaveBeenCalledTimes(1); // the previous period is not even requested
  });

  it("positive control: both periods complete, the same numbers compare as −50 (−50%)", async () => {
    serve((p) => (p.from === "2026-08-31" ? metrics({ event: { total_events: 100 } }) : metrics({ event: { total_events: 50 } })));
    mount(eventsTile());
    await waitFor(() => expect(screen.getByTestId("metric-comparison").textContent).toContain("−50 (−50%) vs previous period"));
    expect(screen.queryByText("No comparison: this period is incomplete")).toBeNull();
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("an incomplete previous period still says so instead of comparing", async () => {
    serve((p) => (p.from === "2026-08-31" ? metrics({ event: { total_events: 100, ...partial } }) : metrics({ event: { total_events: 50 } })));
    mount(eventsTile());
    await waitFor(() => expect(screen.getByText("No comparison: the previous period is incomplete")).toBeTruthy());
    expect(screen.queryByTestId("metric-comparison")).toBeNull();
  });

  it("overdue: the server's count now, a snapshot without a comparison", async () => {
    serve(() => metrics({ task: { overdue: 7 } }));
    mount(view("metric", { metric: "tasks_overdue" }));
    await waitFor(() => expect(screen.getByText("Now, not limited to the period")).toBeTruthy());
    expect(screen.getByText("Overdue")).toBeTruthy();
    expect(screen.getByText("7")).toBeTruthy();
    expect(screen.queryByTestId("metric-comparison")).toBeNull();
    expect(api).toHaveBeenCalledTimes(1);
  });

  it("a snapshot metric makes no comparison request", async () => {
    mount(view("metric", { metric: "agents_active" }));
    await waitFor(() => expect(screen.getByText("Now, not limited to the period")).toBeTruthy());
    expect(api).toHaveBeenCalledTimes(1);
  });

  const urls = () => vi.mocked(api).mock.calls.map((c) => c[0] as string);

  it("awaiting triage is the queue's total from its own endpoint: no analytics request, no period, no CSV", async () => {
    vi.mocked(api).mockImplementation(async (url) =>
      (String(url).endsWith("/triage") ? { items: [{ id: "t1" }], total_count: 23 } : metrics()) as never,
    );
    mount(view("metric", { metric: "triage_waiting", period: "30d" }));
    await waitFor(() => expect(screen.getByText("23")).toBeTruthy());
    expect(screen.getByText("Awaiting triage")).toBeTruthy();
    expect(screen.getByText("Now, not limited to the period or project")).toBeTruthy();
    expect(urls()).toEqual([expect.stringMatching(/\/workspaces\/[^/]+\/triage$/)]);
    expect(calls()[0]).toEqual({ per_page: 1 });
    expect(screen.queryByLabelText("Widget menu")).toBeNull();
  });

  it("awaiting triage that fails to load says so with a retry, never a zero", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("triage is down"));
    mount(view("metric", { metric: "triage_waiting" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("triage is down"));
    expect(screen.queryByText("0")).toBeNull();
    vi.mocked(api).mockResolvedValue({ items: [], total_count: 4 } as never);
    fireEvent.click(screen.getByText("Retry"));
    await waitFor(() => expect(screen.getByText("4")).toBeTruthy());
  });

  it("unread mentions add the task and the document counts; no analytics request", async () => {
    vi.mocked(api).mockImplementation(async (url) =>
      (String(url).includes("unseen_count") ? { count: String(url).includes("doc") ? 3 : 2 } : metrics()) as never,
    );
    mount(view("metric", { metric: "mentions_unseen" }));
    await waitFor(() => expect(screen.getByText("5")).toBeTruthy());
    expect(screen.getByText("Unread mentions")).toBeTruthy();
    expect(urls().every((u) => u.includes("unseen_count"))).toBe(true);
    expect(urls()).toHaveLength(2);
  });

  it("the metric setting offers both live counts after the analytics ones", () => {
    const d = descriptors.find((x) => x.id === "metric")!;
    const ids = d.settings!.properties.metric!.enum as string[];
    expect(ids.slice(-2)).toEqual(["triage_waiting", "mentions_unseen"]);
    expect(ids).toContain("tasks_overdue");
  });

  // PUT layout checks `metric` against catalog.go's enum (core-widgets.json `metrics`, kept in step by the Go test):
  // an id the server does not know cannot be saved, and a template's id the client does not know renders another tile
  it("offers exactly the metric ids the server accepts", () => {
    const sorted = (xs: readonly string[]) => [...xs].sort();
    expect(CATALOG.metrics.length).toBeGreaterThan(0);
    expect(sorted(METRIC_OPTIONS.map((m) => m.id))).toEqual(sorted(CATALOG.metrics));
  });

  it("the Analytics template's tiles each show their own metric: events_total is Events, not Completed", async () => {
    serve(() => metrics({ task: { total: 7, created_this_period: 8, completed_this_period: 5 }, event: { total_events: 42 } }));
    mount(
      view("metric", { metric: "tasks_total" }),
      view("metric", { metric: "tasks_created" }),
      view("metric", { metric: "tasks_closed" }),
      view("metric", { metric: "events_total" }),
    );
    await waitFor(() => expect(screen.getByText("42")).toBeTruthy());
    for (const label of ["Total tasks", "Created", "Completed", "Events"]) expect(screen.getAllByText(label)).toHaveLength(1);
  });
});

describe("tasks-breakdown", () => {
  it("splits by assignee: names from by_assignee, an agent row opens the agent, Unassigned is named", async () => {
    const openAgent = vi.fn();
    const { container } = mount(view("tasks-breakdown", { dimension: "assignee" }, { openAgent }));
    await waitFor(() => expect(screen.getByText("Pat Example")).toBeTruthy());
    expect(rowsOf(container)).toEqual(["a1", "u1", "unassigned:2"]);
    expect(screen.getByText("Unassigned")).toBeTruthy();
    fireEvent.click(screen.getByTitle("Linus"));
    expect(openAgent).toHaveBeenCalledWith("a1");
  });

  it("splits by project: names from by_project, a known project links to its page, a long name is cut with a title", async () => {
    const long = "A project with a very long name that has to be cut";
    const { container } = mount(view("tasks-breakdown", { dimension: "project", view: "donut" }));
    await waitFor(() => expect(screen.getByText("Core")).toBeTruthy());
    expect(rowsOf(container)).toEqual(["p1", "p2"]);
    expect(screen.getByText("Core").closest("a")?.getAttribute("href")).toBe("/w/acme/p/core");
    expect(screen.getByTitle(long).className).toContain("truncate");
    expect(container.querySelector("svg[role=img]")).toBeTruthy();
  });

  it("keeps the top six and sums the rest, and what the server cut, into Other", async () => {
    const many = Array.from({ length: 8 }, (_, i) => ({ assignee_id: `a${i}`, assignee_type: "agent" as const, name: `Agent ${i}`, count: 10 - i }));
    // 8 listed rows add up to 52; the status split says the snapshot holds 60 tasks.
    serve(() => metrics({ task: { by_assignee: many, by_status_category: { todo: 60 } } }));
    const { container } = mount(view("tasks-breakdown", { dimension: "assignee" }));
    await waitFor(() => expect(screen.getByText("Other")).toBeTruthy());
    expect(rowsOf(container)).toEqual(["a0", "a1", "a2", "a3", "a4", "a5", "__other"]);
    expect(container.querySelector("[data-row=__other]")!.textContent).toContain("15");
  });
});

describe("states", () => {
  it("an incomplete event period shows the note with its date; a complete one does not", async () => {
    serve((p) => (p.from === "2026-10-01" ? metrics({ event: { period_fully_covered: false, retained_since: "2026-10-15T00:00:00Z" } }) : metrics()));
    mount(view("events-breakdown", { period: { from: "2026-10-01", to: "2026-10-31" } }));
    await waitFor(() => expect(screen.getByRole("note").textContent).toMatch(/^Data incomplete since /));
    cleanup();
    mount(view("events-breakdown", { period: { days: 7 } }));
    await waitFor(() => expect(screen.getByText("task.created")).toBeTruthy());
    expect(screen.queryByRole("note")).toBeNull();
  });

  it("an incomplete cost flag without a date reads 'for this period'", async () => {
    serve(() => metrics({ cost: { tokens_out_incomplete: true } }));
    mount(view("cost-kpis"));
    await waitFor(() => expect(screen.getByRole("note").textContent).toBe("Data incomplete for this period"));
    expect(screen.getByText("Reported 3 of 4 sessions (75%)")).toBeTruthy();
  });

  it("no data is the empty state, not a blank card", async () => {
    serve(() => metrics({ cost: { by_agent: [], session_count: 0 } }));
    mount(view("cost-by-agent"), view("cost-kpis"));
    await waitFor(() => expect(screen.getAllByText("No data for this period")).toHaveLength(2));
  });

  it("an error offers Retry, and Retry asks again", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("down"));
    mount(view("tasks-timeline"));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("down"));
    fireEvent.click(screen.getByText("Retry"));
    await waitFor(() => expect(screen.getByText("Created vs completed")).toBeTruthy());
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(api).toHaveBeenCalledTimes(2);
  });

  it("Retry uses the host's retry when the host gives one", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("down"));
    const retry = vi.fn();
    mount(view("cost-by-day", {}, { retry }));
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
    fireEvent.click(screen.getByText("Retry"));
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it("loading is a skeleton first", () => {
    vi.mocked(api).mockImplementation(() => new Promise(() => {}));
    const { container } = mount(view("cost-by-project"));
    expect(container.querySelector("[aria-busy=true]")).toBeTruthy();
  });
});

describe("cost-top-tasks", () => {
  it("sorts by cost by default and by tokens when set, which is also asked of the server", async () => {
    const first = mount(view("cost-top-tasks"));
    await waitFor(() => expect(screen.getByText("Task A")).toBeTruthy());
    expect(rowsOf(first.container)).toEqual(["tA", "tB", "tC"]);
    first.unmount();

    const second = mount(view("cost-top-tasks", { sort: "tokens" }));
    await waitFor(() => expect(screen.getByText("Top tasks by tokens")).toBeTruthy());
    await waitFor(() => expect(rowsOf(second.container)).toEqual(["tB", "tC", "tA"]));
    expect(calls()[calls().length - 1]).toMatchObject({ top_tasks_sort: "tokens" });
    // tokens and sessions of each task are shown
    expect(second.container.querySelector("[data-row=tB]")!.textContent).toMatch(/\$3\.00.*900.*2/);
  });
});

describe("Download CSV", () => {
  it("exports with the instance's filter through the export endpoint", async () => {
    vi.mocked(apiBlob).mockResolvedValue({ blob: new Blob(["a,b"]), filename: "analytics.csv" });
    URL.createObjectURL = vi.fn(() => "blob:x");
    URL.revokeObjectURL = vi.fn();
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    mount(view("cost-by-project", { period: { from: "2026-10-01", to: "2026-10-31" }, project: "p1" }));
    fireEvent.click(screen.getByLabelText("Widget menu"));
    fireEvent.click(screen.getByText("Download CSV"));
    await waitFor(() => expect(click).toHaveBeenCalled());
    expect(apiBlob).toHaveBeenCalledWith("/api/v1/workspaces/ws1/analytics/export", {
      from: "2026-10-01",
      to: "2026-10-31T23:59:59Z",
      project_id: "p1",
      format: "csv",
    });
    click.mockRestore();
  });
});

describe("formatTokens", () => {
  it("keeps a workspace's token total short enough for a tile: k, M and B", () => {
    expect(formatTokens(950)).toBe("950");
    expect(formatTokens(56_151_500)).toBe("56.2M");
    expect(formatTokens(56_151_538_120)).toBe("56.2B");
  });
});
