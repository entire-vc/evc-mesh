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
import type { WidgetDescriptor } from "@/core/slots/types";
import { setThemeWidgets } from "../theme-widgets";
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
  type DashboardTheme,
  type DashboardWidgetView,
} from "..";

// The network is the only mock: every widget below runs its real data hook.
vi.mock("@/lib/api", () => ({ api: vi.fn() }));
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
    if (url.endsWith("/team")) return { agents: empty ? [] : [{ id: "a1", name: "Alice", status: "online", is_stale: false }] };
    if (url === "/api/v1/me/mentions") return empty ? [] : [{ comment_id: "c1", task_id: "t9", task_title: "Mentioned task", extracted_at: "2026-01-01T00:00:00Z", seen_at: null, project_id: "p1", comment_body: "", author_id: "u", author_name: "U" }];
    if (url === "/api/v1/me/document-mentions") { if (mode === "doc-mentions-down") throw new Error("down"); return []; }
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

describe("workspace switch", () => {
  it("never shows the previous workspace's data while the new one loads", async () => {
    mountDashboard({ theme: { settings: { widgets: ["my-tasks"] } } });
    await settle();
    expect(screen.getByText("Task 1")).toBeTruthy();
    let release!: () => void;
    vi.mocked(api).mockImplementation(() => new Promise((r) => { release = () => r({ items: [{ ...task(7), title: "Other ws task" }] }); }));
    act(() => useWorkspaceStore.setState({ currentWorkspace: { id: "ws2", slug: "other", name: "Other" } as never }));
    expect(screen.queryByText("Task 1")).toBeNull();
    await act(async () => release());
    await waitFor(() => expect(screen.getByText("Other ws task")).toBeTruthy());
  });
});

describe("registry", () => {
  it("core order, permission filter, picker skips unknown ids, clamp", () => {
    expect(CORE_WIDGETS.map((d) => d.id)).toEqual(["mentions", "triage", "my-tasks", "sessions"]);
    const r = resolveWidgets({ settings: { widgets: ["sessions", "nope", "mentions", "sessions"] } });
    expect(r.shown.map((d) => d.id)).toEqual(["sessions", "mentions"]);
    expect(r.available.map((d) => d.id)).toEqual(["triage", "my-tasks"]);
    const gated = [{ ...base, requires: ["task.edit" as const] }, CORE_WIDGETS[1]!];
    expect(resolveWidgets({ registry: gated, can: () => false }).shown.map((d) => d.id)).toEqual(["triage"]);
    expect(clampSize({ w: 4, h: 3 }, { ...base, size: { ...base.size, max: { w: 2, h: 2 } } })).toEqual({ w: 2, h: 2 });
  });
});

describe("dashboard.layout", () => {
  it("CT-dash-01 mesh-default draws every core widget in registry order via Widget", async () => {
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
          <Widget id={id} />
        </>
      );
    };
    const { container } = mountDashboard({ theme: { layout: Flip } });
    await settle();
    expect(screen.getByText("Task 1")).toBeTruthy();
    fireEvent.click(screen.getByText("flip"));
    await settle();
    expect(ids(container)).toEqual(["sessions"]);
    expect(screen.getByText("Alice")).toBeTruthy();
    expect(errors.mock.calls.flat().join(" ")).not.toMatch(/hook/i);
  });

  it("stub layout with another order and one swapped view: data and actions still work", async () => {
    const Stub: DashboardLayoutView = ({ data }) => {
      const { Widget } = data.components;
      return (
        <section data-testid="stub">
          {[...data.widgets].reverse().map((w) => <Widget key={w.descriptor.id} id={w.descriptor.id} />)}
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
    expect(screen.getByText("Alice")).toBeTruthy(); // untouched core view, real data
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

  it("CT-widget-07 sessions: active agent shown with status", async () => {
    mountDashboard({ theme: { settings: { widgets: ["sessions"] } } });
    await settle();
    expect(screen.getByText("Alice")).toBeTruthy();
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

  it("CT-widget-10 useTasks asks for a full page and sorts by due date, undated last, before the limit", async () => {
    const dues = [null, "2026-03-09", "2026-03-02", "2026-03-07", "2026-03-01", "2026-03-05", "2026-03-03", "2026-03-04"];
    vi.mocked(api).mockImplementation(async (url: string, opts?: { params?: { per_page?: number } }) => {
      if (url !== "/api/v1/me/tasks") return {};
      const all = dues.map((d, i) => ({ ...task(i + 1), due_date: d }));
      return { items: all.slice(0, opts?.params?.per_page ?? 20) };
    });
    const Own: DashboardWidgetView = () => {
      const r = useTasks({ scope: "mine", sort: "due_date", limit: 3 });
      return <p>{r.state === "ready" ? r.value.items.map((t) => t.id).join(",") : "loading"}</p>;
    };
    mountDashboard({
      theme: { settings: { widgets: ["ink.due"] }, extraWidgets: [{ ...base, id: "ink.due", origin: "theme" }], widgets: { "ink.due": Own } },
    });
    await waitFor(() => expect(screen.getByText("t5,t3,t7")).toBeTruthy());
  });

  it("CT-widget-11 useTasks shows a failed read as an error with a retry, not as an empty list", async () => {
    let down = true;
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url !== "/api/v1/me/tasks") return {};
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

  it("a registered dashboard.layout replaces the default and a theme's own widget is listed and drawn with its view", async () => {
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
  });

  it("ownWidgets keeps a well-formed descriptor and drops a foreign id, a viewless one and a wrong size", () => {
    const bundle = { views: {}, widgets: [own, { ...own, id: "other.focus" }, { ...own, id: "studio.nope" }, { ...own, id: "studio.big", size: { ...own.size, max: { w: 9, h: 1 } } }] };
    const kept = ownWidgets("studio", bundle as never, ["dashboard.widget.focus", "dashboard.widget.big"]);
    expect(kept.map((d) => d.id)).toEqual(["studio.focus"]);
  });
});
