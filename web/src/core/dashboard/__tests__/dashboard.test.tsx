import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { TaskLink } from "@/core/primitives";
import { useSlotContext } from "@/core/slots/slot-context";
import { registerSlotViewOverride } from "@/core/slots/registry";
import type { WidgetDescriptorV1 as WidgetDescriptor } from "@/core/slots/types";
import { setLayoutMajor, setThemeWidgets } from "../theme-widgets";
import { ownWidgets } from "@/core/themes/bundle";
import { useThemeStore } from "@/core/themes/store";
import {
  CORE_WIDGETS,
  DashboardContainer,
  DashboardThemeProvider,
  clampSize,
  resolveWidgets,
  useTasks,
  type DashboardLayoutView,
  type DashboardLayoutViewV1,
  type DashboardTheme,
  type DashboardWidgetView,
} from "..";

// The network is the only mock: every widget below runs its real data hook.
// getAccessToken: no session, so the realtime sockets of the widget hooks stay closed.
vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: () => null }));
import { api } from "@/lib/api";

const base = CORE_WIDGETS[0]!;
const task = (n: number, priority = "high") => ({
  id: `t${n}`, title: `Task ${n}`, priority, project_id: "p1", created_at: "2026-01-01T00:00:00Z",
});
type Mode = "normal" | "empty" | "error" | "doc-mentions-down";
function serve(mode: Mode = "normal") {
  vi.mocked(api).mockImplementation(async (url: string) => {
    if (mode === "error") throw new Error("boom");
    const empty = mode === "empty";
    if (url === "/api/v1/me/tasks") return { items: empty ? [] : [task(1), task(2)] };
    if (url.endsWith("/triage")) return { items: empty ? [] : [task(3, "urgent")] };
    if (url.endsWith("/tasks/query")) return { items: empty ? [] : [task(1), task(2)], total_count: empty ? 0 : 2 };
    if (url.endsWith("/team")) return { agents: empty ? [] : [{ id: "a1", name: "Linus", status: "online", is_stale: false }] };
    if (url === "/api/v1/me/mentions") return empty ? [] : [{ comment_id: "c1", task_id: "t9", task_title: "Mentioned task", extracted_at: "2026-01-01T00:00:00Z", seen_at: null, project_id: "p1", comment_body: "", author_id: "u", author_name: "U" }];
    if (url === "/api/v1/me/document-mentions") { if (mode === "doc-mentions-down") throw new Error("down"); return []; }
    if (url === "/api/v1/me/comments" || url.endsWith("/comments/recent")) return { items: [], next_cursor: null, next_cursor_id: null };
    return {};
  });
}

function Where() {
  const l = useLocation();
  return <output data-testid="where">{l.pathname}{l.search}</output>;
}
function mountDashboard(opts: { theme?: DashboardTheme; role?: "viewer" | "member"; width?: number } = {}) {
  window.matchMedia = ((q: string) => ({ matches: (opts.width ?? 1440) >= 768 && q.includes("768"), media: q, addEventListener() {}, removeEventListener() {} })) as never;
  useMemberStore.setState({ myRole: opts.role ?? "member" });
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" } as never] });
  return render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <DashboardThemeProvider theme={opts.theme ?? {}}>
        <Routes>
          <Route path="/w/:wsSlug/dashboard" element={<DashboardContainer />} />
          <Route path="*" element={null} />
        </Routes>
        <Where />
      </DashboardThemeProvider>
    </MemoryRouter>,
  );
}
const ids = (c: HTMLElement) => [...c.querySelectorAll("[data-mesh-widget]")].map((e) => e.getAttribute("data-mesh-widget"));
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 20)); });

beforeEach(() => serve());
afterEach(() => { cleanup(); vi.clearAllMocks(); });

const ANALYTICS = ["metric", "tasks-breakdown", "tasks-timeline", "events-breakdown", "agent-leaderboard", "cost-kpis", "cost-by-agent", "cost-top-tasks", "cost-by-project", "cost-by-day"];

describe("workspace switch", () => {
  it("never shows the previous workspace's data while the new one loads", async () => {
    mountDashboard({ theme: { settings: { widgets: ["my-tasks"] } } });
    // my-tasks' view is a lazy chunk: the first mount in this file waits for it, not for a fixed tick
    expect(await screen.findByText("Task 1", undefined, { timeout: 10_000 })).toBeTruthy();
    let release!: () => void;
    vi.mocked(api).mockImplementation(() => new Promise((r) => { release = () => r({ items: [{ ...task(7), title: "Other ws task" }] }); }));
    act(() => useWorkspaceStore.setState({ currentWorkspace: { id: "ws2", slug: "other", name: "Other" } as never }));
    expect(screen.queryByText("Task 1")).toBeNull();
    await act(async () => release());
    await waitFor(() => expect(screen.getByText("Other ws task")).toBeTruthy());
  });
});

describe("registry", () => {
  it("PRD §5.3: without a theme widgets setting the default dashboard is the old four; new core groups wait in the catalogue", () => {
    const r = resolveWidgets({});
    expect(r.shown.map((d) => d.id)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    expect(r.available.map((d) => d.id)).toEqual(expect.arrayContaining(["agents-active", "agents-status", "agents-summary"]));
    const theme = { ...base, id: "ink.own", origin: "theme" as const };
    expect(resolveWidgets({ registry: [...CORE_WIDGETS, theme] }).shown.map((d) => d.id)).toContain("ink.own");
  });

  it("core order, permission filter, picker skips unknown ids, clamp", () => {
    // the legacy group first, then the other groups by folder name (widgets/agents, analytics, comms, events, initiatives, tasks)
    expect(CORE_WIDGETS.map((d) => d.id)).toEqual([
      "mentions", "triage", "sessions", "agents-active", "agents-status", "agents-summary", ...ANALYTICS, "my-comments", "recent-comments", "event-feed", "initiatives", "initiative", "my-tasks", "triage-queue", "task-list",
    ]);
    const r = resolveWidgets({ settings: { widgets: ["sessions", "nope", "mentions", "sessions"] } });
    expect(r.shown.map((d) => d.id)).toEqual(["sessions", "mentions"]);
    expect(r.available.map((d) => d.id)).toEqual(["triage", "agents-active", "agents-status", "agents-summary", ...ANALYTICS, "my-comments", "recent-comments", "event-feed", "initiatives", "initiative", "my-tasks", "triage-queue", "task-list"]);
    const gated = [{ ...base, requires: ["task.edit" as const] }, CORE_WIDGETS[1]!];
    expect(resolveWidgets({ registry: gated, can: () => false }).shown.map((d) => d.id)).toEqual(["triage"]);
    expect(clampSize({ w: 4, h: 3 }, { ...base, size: { ...base.size, max: { w: 2, h: 2 } } })).toEqual({ w: 2, h: 2 });
  });
});

describe("dashboard.layout", () => {
  it("CT-dash-01 mesh-default draws the default core widgets (PRD 5.3) in registry order via Widget", async () => {
    const { container } = mountDashboard();
    await settle();
    expect(ids(container)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
  });

  it("CT-dash-01 widgets setting picks and orders", async () => {
    const { container } = mountDashboard({ theme: { settings: { widgets: ["my-tasks", "triage"] } } });
    await settle();
    expect(ids(container)).toEqual(["my-tasks", "triage"]);
  });

  it("CT-dash-02 empty selection shows an empty state, not a blank page", async () => {
    mountDashboard({ theme: { settings: { widgets: [] } } });
    expect(screen.getByText(/No widgets|нет виджетов/i)).toBeTruthy();
  });

  it("CT-dash-03 a throwing widget view falls back to mesh-default; siblings live", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const Bad: DashboardWidgetView<"triage"> = () => { throw new Error("render"); };
    const { container } = mountDashboard({ theme: { widgets: { triage: Bad } } });
    await settle();
    expect(ids(container)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    expect(screen.getByText("Task 3")).toBeTruthy(); // default view drew triage's data
    expect(screen.getByText("Task 1")).toBeTruthy();
  });

  it("a throwing layout view falls back to the mesh-default layout; widgets live", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const Bad: DashboardLayoutView = () => { throw new Error("layout"); };
    const { container } = mountDashboard({ theme: { layout: Bad } });
    await settle();
    expect(ids(container)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    expect(screen.getByText("Task 1")).toBeTruthy();
  });

  it("CT-dash-04 mobile: one column, nothing dropped", async () => {
    const { container } = mountDashboard({ width: 393 });
    await settle();
    expect(container.firstElementChild!.className).not.toContain("md:grid-cols-2");
    expect(ids(container)).toHaveLength(4);
  });

  it("useSlotContext() inside a widget view gives the full context of dashboard.widget.<id>", async () => {
    const Inner = () => {
      const c = useSlotContext();
      return <p>ctx {c.slot} {c.workspace?.slug} {c.contractVersion}</p>;
    };
    const Own: DashboardWidgetView = () => <Inner />;
    mountDashboard({
      theme: {
        extraWidgets: [{ ...base, id: "ink.overdue", origin: "theme", dataHook: "useTasks" }],
        widgets: { "ink.overdue": Own },
      },
    });
    await settle();
    expect(screen.getByText("ctx dashboard.widget.ink.overdue acme 1.0.0")).toBeTruthy();
  });

  it("CT-dash-06 a theme's own widget is drawn next to core ones", async () => {
    const Own: DashboardWidgetView = ({ data }) => <p>own {data.id}</p>;
    const { container } = mountDashboard({
      theme: {
        extraWidgets: [{ ...base, id: "ink.overdue", origin: "theme", dataHook: "useTasks" }],
        widgets: { "ink.overdue": Own },
      },
    });
    await settle();
    expect(ids(container)).toContain("ink.overdue");
    expect(screen.getByText("own ink.overdue")).toBeTruthy();
  });

  it("a Widget reused with another id switches data hooks without a hook-order error", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const Flip: DashboardLayoutView = ({ data }) => {
      const { Widget } = data.components;
      const [id, setId] = useState("my-tasks");
      return (
        <>
          <button onClick={() => setId("sessions")}>flip</button>
          <Widget instance={{ id, type: id, x: 0, y: 0, w: 6, h: 2, settings: {} }} />
        </>
      );
    };
    const { container } = mountDashboard({ theme: { layout: Flip } });
    expect(await screen.findByText("Task 1", undefined, { timeout: 10_000 })).toBeTruthy();
    fireEvent.click(screen.getByText("flip"));
    await settle();
    expect(ids(container)).toEqual(["sessions"]);
    expect(screen.getByText("Linus")).toBeTruthy();
    expect(errors.mock.calls.flat().join(" ")).not.toMatch(/hook/i);
  });

  it("stub layout with another order and one swapped view: data and actions still work", async () => {
    const Stub: DashboardLayoutView = ({ data }) => {
      const { Widget } = data.components;
      return (
        <section data-testid="stub">
          {[...data.widgets].reverse().map((w) => <Widget key={w.instance.id} instance={w.instance} />)}
        </section>
      );
    };
    const Mine: DashboardWidgetView<"my-tasks"> = ({ data, actions }) => {
      if (data.result.state !== "ready") return <i>wait</i>;
      return (
        <ul>
          {data.result.value.items.map((t) => (
            <li key={t.id}><button onClick={() => actions.openTask(t.id)}>swapped {t.title}</button> <TaskLink taskId={t.id}>link {t.id}</TaskLink></li>
          ))}
        </ul>
      );
    };
    const { container } = mountDashboard({ theme: { layout: Stub, widgets: { "my-tasks": Mine } } });
    await settle();
    expect(ids(container)).toEqual(["sessions", "my-tasks", "triage", "mentions"]);
    expect(screen.getByText("Linus")).toBeTruthy(); // untouched core view, real data
    expect(screen.getByText("link t1").getAttribute("href")).toBe("/t/t1");
    fireEvent.click(screen.getByText("link t1")); // the primitive reaches the widget's openTask
    expect(screen.getByTestId("where").textContent).toBe("/t/t1");
  });
});

describe("dashboard.widget.*", () => {
  it("CT-widget-01 empty and error read as the empty state; loading is a skeleton first", async () => {
    serve("empty");
    const { container } = mountDashboard();
    expect(container.querySelector('[class*="animate-pulse"], [data-slot="skeleton"]')).toBeTruthy();
    await settle();
    expect(screen.queryByText("Task 1")).toBeNull();
    cleanup();
    serve("error");
    mountDashboard();
    await settle();
    expect(screen.queryByText("Task 1")).toBeNull();
  });

  it("CT-widget-02 mentions: a source that failed is not shown as 'no mentions'", async () => {
    serve("empty");
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url === "/api/v1/me/document-mentions") throw new Error("down");
      if (url === "/api/v1/me/mentions") return [];
      return { items: [], agents: [] };
    });
    mountDashboard({ theme: { settings: { widgets: ["mentions"] } } });
    await settle();
    expect(screen.getByText(/could not be loaded/i)).toBeTruthy();
  });

  it("CT-widget-03 mentions: plain click calls openMention, modified click is left to the browser", async () => {
    const Spy: DashboardWidgetView<"mentions"> = ({ data, actions }) => {
      const first = data.result.state === "ready" ? data.result.value.items[0] : undefined;
      if (!first) return null;
      return <a href="/x" onClick={(e) => { e.preventDefault(); actions.openMention(first, { newTab: e.ctrlKey }); }}>m</a>;
    };
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    mountDashboard({ theme: { settings: { widgets: ["mentions"] }, widgets: { mentions: Spy } } });
    await settle();
    fireEvent.click(screen.getByText("m"), { ctrlKey: true });
    expect(open).toHaveBeenCalledWith("/t/t9?comment=c1", "_blank", "noopener");
    fireEvent.click(screen.getByText("m"));
    expect(screen.getByTestId("where").textContent).toBe("/t/t9?comment=c1");
  });

  it("CT-widget-04 my-tasks rows are real links to the task", async () => {
    mountDashboard({ theme: { settings: { widgets: ["my-tasks"] } } });
    await settle();
    expect(screen.getByText("Task 1").closest("a")!.getAttribute("href")).toBe("/t/t1");
  });

  it("a v2 group widget picked by the theme is drawn by the host from the folder registry (agents-active)", async () => {
    const { container } = mountDashboard({ theme: { settings: { widgets: ["agents-active"] } } });
    await settle();
    expect(ids(container)).toEqual(["agents-active"]);
    // The agents group's views load lazily (views.tsx), so the body shows after that load, not after one tick.
    await waitFor(() => expect(screen.getByText("Active agents")).toBeTruthy());
    await waitFor(() => expect(screen.getByText("Linus")).toBeTruthy());
  });

  it("CT-widget-05 triage: an urgent task past the server's first ten rows still leads the card", async () => {
    // the server's own order (hard gates first): ten low hard gates, then the urgent one
    const queue = [...Array.from({ length: 10 }, (_, i) => ({ ...task(20 + i, "low"), title: `Low gate ${i}` })), { ...task(40, "urgent"), title: "Urgent triage" }];
    vi.mocked(api).mockImplementation(async (url: string, opts?: { params?: { page_size?: number } }) => {
      if (!url.endsWith("/triage")) return {};
      return { items: queue.slice(0, opts?.params?.page_size ?? 50), total_count: queue.length };
    });
    mountDashboard({ theme: { settings: { widgets: ["triage"] } } });
    await waitFor(() => expect(screen.getByText("Urgent triage")).toBeTruthy());
    expect(screen.queryByText("Low gate 4")).toBeNull(); // five rows: the urgent one and four low gates
  });

  it("CT-widget-07 sessions: active agent shown with status", async () => {
    mountDashboard({ theme: { settings: { widgets: ["sessions"] } } });
    await settle();
    expect(screen.getByText("Linus")).toBeTruthy();
  });

  it("CT-widget-09 a theme's own widget reads only SDK hooks", async () => {
    const Own: DashboardWidgetView = () => {
      const r = useTasks({ scope: "mine", limit: 5 });
      return <p>{r.state === "ready" ? `n=${r.value.items.length}` : "loading"}</p>;
    };
    mountDashboard({
      theme: { settings: { widgets: ["ink.mine"] }, extraWidgets: [{ ...base, id: "ink.mine", origin: "theme" }], widgets: { "ink.mine": Own } },
    });
    await waitFor(() => expect(screen.getByText("n=2")).toBeTruthy());
  });

  it("CT-widget-10 useTasks hands every filter to the server and shows its order and total", async () => {
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url !== "/api/v1/workspaces/ws1/tasks/query") return {};
      return { items: [task(5), task(3), task(7)], total_count: 12 };
    });
    const Own: DashboardWidgetView = () => {
      const r = useTasks({ scope: "mine", sort: "due_date", priority: ["urgent", "high"], dueBefore: "2026-12-01", limit: 3 });
      return <p>{r.state === "ready" ? `${r.value.items.map((t) => t.id).join(",")} of ${r.value.total}` : "loading"}</p>;
    };
    mountDashboard({
      theme: { settings: { widgets: ["ink.due"] }, extraWidgets: [{ ...base, id: "ink.due", origin: "theme" }], widgets: { "ink.due": Own } },
    });
    await waitFor(() => expect(screen.getByText("t5,t3,t7 of 12")).toBeTruthy());
    expect(vi.mocked(api)).toHaveBeenCalledWith("/api/v1/workspaces/ws1/tasks/query", {
      params: expect.objectContaining({ scope: "mine", sort: "due_date", priority: "urgent,high", due_before: "2026-12-01", limit: 3 }),
    });
  });

  it("CT-widget-11 useTasks shows a failed read as an error with a retry, not as an empty list", async () => {
    let down = true;
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url !== "/api/v1/workspaces/ws1/tasks/query") return {};
      if (down) throw new Error("boom");
      return { items: [task(1)] };
    });
    const Own: DashboardWidgetView = () => {
      const r = useTasks({ scope: "mine", limit: 5 });
      if (r.state === "error") return <button onClick={() => r.retry()}>retry-it</button>;
      return <p>{r.state === "ready" ? `n=${r.value.items.length}` : "loading"}</p>;
    };
    mountDashboard({
      theme: { settings: { widgets: ["ink.err"] }, extraWidgets: [{ ...base, id: "ink.err", origin: "theme" }], widgets: { "ink.err": Own } },
    });
    const btn = await screen.findByText("retry-it");
    down = false;
    fireEvent.click(btn);
    await waitFor(() => expect(screen.getByText("n=1")).toBeTruthy());
  });
});

describe("a live theme bundle (runtime registry, no provider)", () => {
  const own: WidgetDescriptor = { id: "studio.focus", title: "Focus", description: "Due soon", origin: "theme", dataHook: "useTasks", actions: ["openTask"], size: { default: { w: 2, h: 1 }, min: { w: 1, h: 1 }, max: { w: 4, h: 2 } } };
  afterEach(() => { setThemeWidgets([]); useThemeStore.getState().set({ settings: {} }); });

  it("a registered dashboard.layout (^1) replaces the default and a theme's own widget is listed and drawn with its view", async () => {
    setLayoutMajor(1); // the runtime records the major the bundle declared
    const disposers = [
      registerSlotViewOverride("dashboard.layout", (({ data }: { data: { widgets: { descriptor: { id: string } }[]; components: { Widget: React.ComponentType<{ id: string }> } } }) => (
        <section data-testid="studio-grid">{data.widgets.map((w) => <data.components.Widget key={w.descriptor.id} id={w.descriptor.id} />)}</section>
      )) as never),
      registerSlotViewOverride("dashboard.widget.focus", (() => <p>Own focus widget</p>) as never),
    ];
    setThemeWidgets([own]);
    const { container } = mountDashboard();
    await settle();
    expect(screen.getByTestId("studio-grid")).toBeTruthy();
    expect(ids(container)).toEqual(["mentions", "triage", "my-tasks", "sessions", "studio.focus"]);
    expect(screen.getByText("Own focus widget")).toBeTruthy();
    disposers.forEach((d) => d());
    setLayoutMajor(2);
  });

  it("a ^1 layout gets v1 data from the adapter: (y, x) order, v1 sizes, editing off, no edit actions", async () => {
    setLayoutMajor(1);
    let seen: { data: { widgets: { size: { w: number; h: number } }[]; editing: boolean }; actions: Record<string, unknown> } | undefined;
    const V1: DashboardLayoutViewV1 = (props) => {
      seen = props as never;
      return <section>{props.data.widgets.map((w) => <props.data.components.Widget key={w.descriptor.id} id={w.descriptor.id} size={w.size} />)}</section>;
    };
    const { container } = mountDashboard({ theme: { layout: V1 } });
    await settle();
    expect(ids(container)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    // §5.8 6×4 in a v1 grid of at most 3 rows: 2 columns, 3 rows.
    expect(seen!.data.widgets.map((w) => w.size)).toEqual(Array(4).fill({ w: 2, h: 3 }));
    expect(seen!.data.editing).toBe(false);
    expect(Object.keys(seen!.actions)).toEqual(["openTask"]);
    setLayoutMajor(2);
  });

  it("ownWidgets keeps a well-formed descriptor and drops a foreign id, a viewless one and a wrong size", () => {
    const bundle = { views: {}, widgets: [own, { ...own, id: "other.focus" }, { ...own, id: "studio.nope" }, { ...own, id: "studio.big", size: { ...own.size, max: { w: 9, h: 1 } } }] };
    const kept = ownWidgets("studio", bundle as never, ["dashboard.widget.focus", "dashboard.widget.big"]);
    expect(kept.map((d) => d.id)).toEqual(["studio.focus"]);
  });
});
