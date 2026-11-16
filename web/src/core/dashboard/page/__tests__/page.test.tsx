import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { setLayoutMajor, setThemeWidgets } from "../../theme-widgets";
import { registerSlotViewOverride } from "@/core/slots/registry";
import { resetExtensionWidgetsForTests } from "@/lib/extension-widgets";
import type { DashboardDoc, WidgetInstance } from "../model";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: vi.fn() }));
import { api, ApiRequestError } from "@/lib/api";

type LiveEvent = { type: string; data: unknown; at: string };
// Every mounted subscriber gets the event, as on the real socket (the page and its widgets each subscribe).
const subs = vi.hoisted(() => new Set<{ current: (e: { type: string; data: unknown; at: string }) => void }>());
const live = (e: LiveEvent) => subs.forEach((s) => s.current(e));
vi.mock("@/core/primitives/use-realtime", async () => {
  const { useEffect, useRef } = await import("react");
  return {
    useRealtime: (_t: unknown, fn: (e: LiveEvent) => void) => {
      const ref = useRef(fn);
      ref.current = fn;
      useEffect(() => {
        subs.add(ref);
        return () => void subs.delete(ref);
      }, []);
      return { connected: true };
    },
  };
});

import { DashboardEntry, DashboardPage } from "../dashboard-page";

const W = (id: string, type: string, x: number, y: number, w: number, h: number, settings?: Record<string, unknown>): WidgetInstance =>
  ({ id, widget_type: type, x, y, w, h, ...(settings ? { settings } : {}) });
const doc = (widgets: WidgetInstance[], extra: Partial<DashboardDoc> = {}): DashboardDoc => ({
  id: "d1", name: "Ops", icon: "🚦", placement: "top", position: 0, version: 3, can_edit: true, widgets, ...extra,
});
const FOUR = [W("i1", "mentions", 0, 0, 12, 2), W("i2", "my-tasks", 0, 2, 6, 4), W("i3", "triage", 6, 2, 6, 4), W("i4", "sessions", 0, 6, 3, 2)];

function Where() {
  const l = useLocation();
  return <div data-testid="where">{l.pathname + l.search}</div>;
}
function mount(path = "/w/acme/d/d1") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Where />
      <Routes>
        <Route path="/w/:wsSlug" element={<DashboardEntry />} />
        <Route path="/w/:wsSlug/dashboard" element={<DashboardEntry />} />
        <Route path="/w/:wsSlug/d/:docId" element={<DashboardPage />} />
      </Routes>
    </MemoryRouter>,
  );
}
function serve(d: DashboardDoc | (() => DashboardDoc), list: unknown = []) {
  vi.mocked(api).mockImplementation(async (url: string) => {
    if (url.endsWith("/dashboards")) {
      if (list instanceof Error) throw list;
      return list;
    }
    if (url.startsWith("/api/v1/workspaces/ws1/dashboards/")) return typeof d === "function" ? d() : d;
    if (url === "/api/v1/me/tasks" || url.endsWith("/triage")) return { items: [] };
    if (url.endsWith("/team")) return { agents: [] };
    return [];
  });
}
const setWidth = (px: number) => {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: px });
  window.dispatchEvent(new Event("resize"));
};

beforeEach(() => {
  setWidth(1440);
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  window.matchMedia ??= (() => ({ matches: true, addEventListener() {}, removeEventListener() {} })) as never;
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  setThemeWidgets([]);
  useProjectStore.setState({ projects: [] });
  subs.clear();
});

describe("dashboard page", () => {
  it("places widgets by coordinates at 1440, 1024 and 393 and loses none", async () => {
    serve(doc(FOUR));
    mount();
    const grid = await screen.findByTestId("dashboard-grid");
    const cell = (id: string) => grid.querySelector<HTMLElement>(`[data-widget-instance="${id}"]`)!;
    expect(grid.dataset.cols).toBe("12");
    expect(cell("i2").style.gridColumn).toBe("1 / 7");
    expect(cell("i3").style.gridColumn).toBe("7 / 13");
    expect(cell("i4").style.gridRow).toBe("7 / span 2");

    act(() => setWidth(1024));
    expect(grid.dataset.cols).toBe("6");
    expect(cell("i1").style.gridColumn).toBe("1 / 7");
    expect(cell("i4").style.gridColumn).toBe("1 / 3");

    act(() => setWidth(393));
    expect(grid.dataset.cols).toBe("1");
    expect([...grid.children].map((c) => (c as HTMLElement).dataset.widgetInstance)).toEqual(["i1", "i2", "i3", "i4"]);
    expect(cell("i2").style.gridColumn).toBe("");
    expect(grid.querySelectorAll("[data-widget-instance]").length).toBe(4);
  });

  it("paper keeps the 12-column place of every cell at any window width (§11.6 print)", async () => {
    serve(doc(FOUR));
    mount();
    const grid = await screen.findByTestId("dashboard-grid");
    const cell = (id: string) => grid.querySelector<HTMLElement>(`[data-widget-instance="${id}"]`)!;
    const onPaper = () => ["i1", "i2", "i3", "i4"].map((id) => [cell(id).style.getPropertyValue("--print-col"), cell(id).style.getPropertyValue("--print-row")]);
    expect(grid.hasAttribute("data-print-grid")).toBe(true);
    const desktop = onPaper();
    expect(desktop[1]).toEqual(["1 / 7", "3 / span 4"]);
    expect(desktop[3]).toEqual(["1 / 4", "7 / span 2"]);
    for (const px of [1024, 393]) {
      act(() => setWidth(px));
      expect(onPaper()).toEqual(desktop);
    }
    // The dashboard page brings the sheet that fits the 12 columns: landscape.
    expect(document.querySelector('style[media="print"]')?.textContent).toContain("size: landscape");
  });

  it("header: emoji, name, 👥 when shared, gear only with can_edit", async () => {
    serve(doc(FOUR, { shared: true }));
    mount();
    expect(await screen.findByRole("heading", { name: /Ops/ })).toHaveTextContent("🚦");
    expect(screen.getByRole("img", { name: /Shared dashboard|Общий дашборд/ })).toBeTruthy();
    const gear = screen.getByRole("button", { name: /Edit layout|Править раскладку/ });
    fireEvent.click(gear);
    expect(screen.getByRole("button", { name: /Done|Готово/ })).toHaveAttribute("aria-pressed", "true");
  });

  it("header: an icon:<name> value is drawn as the icon, never as its text", async () => {
    serve(doc(FOUR, { icon: "icon:layout-dashboard" }));
    mount();
    const h1 = await screen.findByRole("heading", { name: /Ops/ });
    expect(h1.textContent).not.toContain("icon:");
    await waitFor(() => expect(h1.querySelector("svg")).not.toBeNull());
  });

  it("the gear mounts the editor: catalog from the server with core strings, an added widget saved under the workspace", async () => {
    const catalog = { widgets: [{ id: "initiatives", origin: "core", category: "initiatives", size: { default: { w: 6, h: 4 }, min: { w: 3, h: 3 }, max: { w: 12, h: 8 } } }] };
    serve(doc(FOUR));
    const served = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation(async (url: string, opts?: unknown) => {
      if (url === "/api/v1/workspaces/ws1/dashboard-widgets") return catalog;
      if (url.endsWith("/layout")) {
        const body = (opts as { body: { widgets: WidgetInstance[] } }).body;
        return doc(body.widgets.map((w, i) => ({ ...w, id: w.id ?? `new${i}` })), { version: 4 });
      }
      return served(url, opts as never);
    });
    mount();
    await screen.findByTestId("dashboard-grid");
    fireEvent.click(screen.getByRole("button", { name: /Edit layout|Править раскладку/ }));
    expect(await screen.findByTestId("dash-editor")).toBeTruthy();
    expect(screen.queryByTestId("dashboard-grid")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: /Add widget|Добавить виджет/ }));
    const group = await screen.findByRole("region", { name: /Initiatives|Инициативы/ });
    fireEvent.click(within(group).getByRole("button", { name: /Initiatives/ }));
    await waitFor(() => expect(vi.mocked(api).mock.calls.some(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1/layout")).toBe(true));
    const [, opts] = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1/layout")!;
    const body = (opts as { body: { version: number; widgets: { id?: string; widget_type: string }[] } }).body;
    expect(body.version).toBe(3);
    expect(body.widgets.map((w) => w.widget_type)).toEqual(["mentions", "my-tasks", "triage", "sessions", "initiatives"]);
    expect(body.widgets[4]).not.toHaveProperty("id");
    fireEvent.click(screen.getByRole("button", { name: /Done|Готово/ }));
    expect(await screen.findByTestId("dashboard-grid")).toBeTruthy();
    expect(screen.queryByTestId("dash-editor")).toBeNull();
  });

  it("review: an edit session ends with its route; nothing is saved into the dashboard left behind", async () => {
    const byId: Record<string, DashboardDoc> = { d1: doc(FOUR), d2: doc([W("j1", "mentions", 0, 0, 12, 2)], { id: "d2", name: "Second" }) };
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url === "/api/v1/workspaces/ws1/dashboard-widgets") return { widgets: [] };
      const m = /^\/api\/v1\/workspaces\/ws1\/dashboards\/([^/]+)$/.exec(url);
      if (m) return byId[m[1]!]!;
      if (url.endsWith("/team")) return { agents: [] };
      return { items: [] };
    });
    function Go() {
      const nav = useNavigate();
      return (
        <>
          <button type="button" onClick={() => nav("/w/acme/d/d2")}>to-d2</button>
          <button type="button" onClick={() => nav("/w/acme/d/d1")}>to-d1</button>
        </>
      );
    }
    render(
      <MemoryRouter initialEntries={["/w/acme/d/d1"]}>
        <Go />
        <Routes>
          <Route path="/w/:wsSlug/d/:docId" element={<DashboardPage />} />
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByTestId("dashboard-grid");
    fireEvent.click(screen.getByRole("button", { name: /Edit layout|Править раскладку/ }));
    expect(await screen.findByTestId("dash-editor")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "to-d2" }));
    expect(await screen.findByRole("heading", { name: /Second/ })).toBeTruthy();
    expect(await screen.findByTestId("dashboard-grid")).toBeTruthy();
    expect(screen.queryByTestId("dash-editor")).toBeNull();
    expect(screen.queryByRole("button", { name: /Add widget|Добавить виджет/ })).toBeNull();
    // back on d1 the old session does not come back either
    fireEvent.click(screen.getByRole("button", { name: "to-d1" }));
    expect(await screen.findByRole("heading", { name: /Ops/ })).toBeTruthy();
    expect(await screen.findByTestId("dashboard-grid")).toBeTruthy();
    expect(screen.queryByTestId("dash-editor")).toBeNull();
    expect(vi.mocked(api).mock.calls.some(([, o]) => (o as { method?: string } | undefined)?.method === "PUT")).toBe(false);
  });

  it("review: under a dashboard.layout v1 theme the gear explains why editing is unavailable (§5.10)", async () => {
    setLayoutMajor(1);
    try {
      serve(doc(FOUR));
      mount();
      await screen.findByRole("heading", { name: /Ops/ });
      expect(screen.queryByRole("button", { name: /Edit layout|Править раскладку/ })).toBeNull();
      const gear = screen.getByRole("button", { name: /Edit dashboard|Править дашборд/ });
      expect(gear).toHaveAttribute("aria-disabled", "true");
      fireEvent.click(gear);
      expect(screen.getByRole("status")).toHaveTextContent(/theme does not support|Тема не поддерживает/);
      expect(screen.queryByTestId("dash-editor")).toBeNull();
    } finally {
      setLayoutMajor(2);
    }
  });

  it("no gear without can_edit", async () => {
    serve(doc(FOUR, { can_edit: false }));
    mount();
    await screen.findByTestId("dashboard-grid");
    expect(screen.queryByRole("button", { name: /Edit layout|Править раскладку/ })).toBeNull();
  });

  it("empty dashboard shows the stub", async () => {
    serve(doc([]));
    mount();
    expect(await screen.findByTestId("dashboard-empty")).toHaveTextContent(/Add your first widget|Добавьте первый виджет/);
    expect(screen.queryByTestId("dashboard-grid")).toBeNull();
  });

  it("unknown type: stub with the title and 'Remove widget' that saves the layout without it", async () => {
    serve(doc([W("i1", "mentions", 0, 0, 6, 2), W("i9", "legacy-unknown", 6, 0, 6, 2)]));
    mount();
    const stub = await screen.findByTestId("widget-stub");
    expect(stub).toHaveTextContent("legacy-unknown");
    expect(screen.getByTestId("dashboard-grid").querySelector('[data-widget-instance="i1"]')).toBeTruthy();
    fireEvent.click(within(stub).getByRole("button", { name: /Remove widget|Убрать виджет/ }));
    await waitFor(() => {
      const put = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1/layout");
      expect(put).toBeTruthy();
      const body = put![1] as { body: { version: number; widgets: WidgetInstance[] } };
      expect(body.body.version).toBe(3);
      expect(body.body.widgets.map((w) => w.id)).toEqual(["i1"]);
    });
  });

  it("an extension's type whose extension is gone: its stub names the extension, 'Remove widget' saves without it", async () => {
    serve(doc([W("i1", "mentions", 0, 0, 6, 2), W("i9", "gitlab.failed-pipelines", 6, 0, 6, 2)]));
    mount();
    const stub = await screen.findByTestId("extension-widget-stub");
    expect(stub).toHaveTextContent(/gitlab/);
    fireEvent.click(within(stub).getByRole("button", { name: /Remove widget|Убрать виджет/ }));
    await waitFor(() => {
      const put = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1/layout");
      expect((put![1] as { body: { widgets: WidgetInstance[] } }).body.widgets.map((w) => w.id)).toEqual(["i1"]);
    });
  });

  it("an external extension's frame widget draws in its cell and asks ui-context for this dashboard and instance", async () => {
    const ext = {
      id: "reference-ext", name: "Reference extension",
      manifest: { capabilities: { ui: ["dashboard.widget"] }, widgets: [{ id: "status", title: "Reference widget", scope: "workspace", render: { frame: { path: "/mesh/ui/widget" } } }] },
      status: { enabled: true, source: "workspace", consented_capabilities: ["ui:dashboard.widget"] },
    };
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url.startsWith("/api/v1/workspaces/ws1/dashboards/")) return doc([W("i1", "mentions", 0, 0, 6, 2), { ...W("x1", "reference-ext.status", 6, 0, 6, 3), settings: { label: "L" } }]);
      if (url === "/api/v1/workspaces/ws1/extensions") return { extensions: [ext] };
      if (url === "/api/v1/ext/reference-ext/ui-context") return new Promise(() => {});
      return [];
    });
    resetExtensionWidgetsForTests(); // the workspace's extension list is cached between tests
    mount();
    const cell = await waitFor(() => {
      const el = screen.getByTestId("dashboard-grid").querySelector('[data-widget-instance="x1"]');
      expect(el?.querySelector('[data-testid="external-widget"]')).toBeTruthy();
      return el!;
    });
    expect(cell.querySelector('[data-testid="extension-widget-card"][data-extension-widget="reference-ext.status"]')).toBeTruthy();
    await waitFor(() => {
      const call = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/ext/reference-ext/ui-context");
      expect((call![1] as { body: unknown }).body).toMatchObject({ slot: "dashboard.widget", dashboard_id: "d1", instance_id: "x1", widget_id: "status" });
    });
  });

  it("a crashing widget view does not take the others down", async () => {
    registerSlotViewOverride("dashboard.widget.my-tasks", (() => {
      throw new Error("boom");
    }) as never);
    vi.spyOn(console, "error").mockImplementation(() => {});
    serve(doc(FOUR));
    mount();
    const grid = await screen.findByTestId("dashboard-grid");
    expect(grid.querySelectorAll("[data-widget-instance]").length).toBe(4);
    await waitFor(() => expect(within(grid).getAllByText(/Mentions|Упоминания/).length).toBeGreaterThan(0));
  });

  it("a triage-queue instance (the Triage template's type) draws the triage queue: project, author, age and Move", async () => {
    const task = {
      id: "t1", title: "Inbox card", priority: "medium", project_id: "p6", created_by_name: "Hugh", created_at: "2026-11-15T10:00:00Z",
    };
    const served = (url: string) => {
      if (url === "/api/v1/workspaces/ws1/triage") return { items: [task], total_count: 1 };
      return undefined;
    };
    useProjectStore.setState({ projects: [{ id: "p6", name: "Ops project" }] as never });
    serve(doc([W("q1", "triage-queue", 0, 0, 6, 4)]));
    const base = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation(async (url: string, opts?: unknown) => served(url) ?? base(url, opts as never));
    mount();
    const cell = await screen.findByTestId("dashboard-grid").then((g) => g.querySelector<HTMLElement>('[data-widget-instance="q1"]')!);
    await within(cell).findByText("Inbox card");
    // the v1 triage widget draws the title too, but as a bare row: no data-task-row, no Move, no project or author
    const row = cell.querySelector<HTMLElement>('[data-task-row="t1"]');
    expect(row).not.toBeNull();
    if (!row) return;
    expect(within(row).getByRole("button", { name: /Inbox card/ })).toBeTruthy();
    expect(within(row).getByText(/Hugh/)).toBeTruthy();
    expect(within(row).getByText("Ops project")).toBeTruthy();
  });

  it("⤢ links to ?w=, the expanded widget opens from a direct link, Back returns", async () => {
    serve(doc(FOUR));
    mount();
    const grid = await screen.findByTestId("dashboard-grid");
    const expand = within(grid.querySelector('[data-widget-instance="i2"]') as HTMLElement).getByRole("link", { name: /Expand|Развернуть/ });
    expect(expand).toHaveAttribute("href", "/w/acme/d/d1?w=i2");
    fireEvent.click(expand);
    expect(await screen.findByTestId("dashboard-expanded")).toBeTruthy();
    expect(screen.getByTestId("where")).toHaveTextContent("/w/acme/d/d1?w=i2");
    expect(screen.queryByTestId("dashboard-grid")).toBeNull();
    fireEvent.click(screen.getByRole("link", { name: /Back|Назад/ }));
    expect(await screen.findByTestId("dashboard-grid")).toBeTruthy();
    expect(screen.getByTestId("where")).toHaveTextContent(/^\/w\/acme\/d\/d1$/);
  });

  it("direct ?w= link opens; a gone widget says so, with Back", async () => {
    serve(doc(FOUR));
    mount("/w/acme/d/d1?w=i3");
    expect(await screen.findByTestId("dashboard-expanded")).toBeTruthy();
    cleanup();
    mount("/w/acme/d/d1?w=nope");
    expect(await screen.findByText(/no longer on the dashboard|больше нет/)).toBeTruthy();
  });

  it("dashboard.updated re-reads without a reload and ignores other dashboards", async () => {
    let current = doc([W("i1", "mentions", 0, 0, 12, 2)]);
    serve(() => current);
    mount();
    const grid = await screen.findByTestId("dashboard-grid");
    expect(grid.querySelectorAll("[data-widget-instance]").length).toBe(1);
    const calls = () => vi.mocked(api).mock.calls.filter(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1").length;
    const before = calls();
    current = doc([W("i1", "mentions", 0, 0, 12, 2), W("i2", "sessions", 0, 2, 6, 2)], { version: 4 });
    // The server's payload: {event_type, workspace_id, id, version} (dashboards/service.go).
    const ev = (id: string, extra: object = {}) => ({ event_type: "dashboard.updated", workspace_id: "ws1", id, version: 4, ...extra });
    act(() => live({ type: "dashboard.updated", data: ev("other"), at: "" }));
    expect(calls()).toBe(before);
    act(() => live({ type: "dashboard.updated", data: ev("d1"), at: "" }));
    await waitFor(() => expect(screen.getByTestId("dashboard-grid").querySelectorAll("[data-widget-instance]").length).toBe(2));
    expect(screen.queryByTestId("dashboard-loading")).toBeNull();
  });

  it("dashboard.updated {id, deleted:true} → the missing screen, without a reload", async () => {
    let gone = false;
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url === "/api/v1/workspaces/ws1/dashboards/d1") {
        if (gone) throw new ApiRequestError("nf", "not_found", 404);
        return doc([W("i1", "mentions", 0, 0, 12, 2)]);
      }
      return { items: [] };
    });
    mount();
    expect(await screen.findByTestId("dashboard-grid")).toBeTruthy();
    gone = true;
    act(() => live({ type: "dashboard.updated", data: { event_type: "dashboard.updated", workspace_id: "ws1", id: "d1", version: 3, deleted: true }, at: "" }));
    expect(await screen.findByText(/not found|не найден/i)).toBeTruthy();
    expect(screen.queryByTestId("dashboard-grid")).toBeNull();
  });

  it("a failed silent re-read that is not a 404 keeps the dashboard on screen", async () => {
    let fail = false;
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url === "/api/v1/workspaces/ws1/dashboards/d1") {
        if (fail) throw new Error("network down");
        return doc([W("i1", "mentions", 0, 0, 12, 2)]);
      }
      return { items: [] };
    });
    mount();
    expect(await screen.findByTestId("dashboard-grid")).toBeTruthy();
    fail = true;
    act(() => live({ type: "dashboard.updated", data: { event_type: "dashboard.updated", workspace_id: "ws1", id: "d1", version: 4 }, at: "" }));
    await waitFor(() => expect(vi.mocked(api).mock.calls.filter(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1").length).toBe(2));
    await act(async () => {});
    expect(screen.getByTestId("dashboard-grid")).toBeTruthy();
    expect(screen.queryByText(/not found|не найден/i)).toBeNull();
    expect(screen.queryByText(/network down/)).toBeNull();
  });

  it("error shows retry; 404 shows not-found", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("boom"));
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    cleanup();
    vi.mocked(api).mockRejectedValueOnce(new ApiRequestError("nf", "not_found", 404));
    mount();
    expect(await screen.findByText(/not found|не найден/i)).toBeTruthy();
  });

  it("routes: /w/:ws and /w/:ws/dashboard open the first top dashboard, else the first bottom", async () => {
    const list = [
      { id: "b1", name: "B", icon: "", placement: "bottom", position: 0 },
      { id: "t2", name: "T2", icon: "", placement: "top", position: 1 },
      { id: "t1", name: "T1", icon: "", placement: "top", position: 0 },
    ];
    serve(doc(FOUR, { id: "t1" }), list);
    mount("/w/acme");
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/w/acme/d/t1"));
    cleanup();
    serve(doc(FOUR, { id: "b1" }), [list[0]]);
    mount("/w/acme/dashboard");
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/w/acme/d/b1"));
  });

  it.each([404, 501])("API answers %i: the page that always worked (legacy container) is shown, no redirect", async (status) => {
    serve(doc(FOUR), new ApiRequestError("nope", "not_found", status));
    mount("/w/acme");
    await waitFor(() => expect(document.querySelector("[data-mesh-widget]")).toBeTruthy());
    expect(screen.queryByTestId("dashboard-grid")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByTestId("where")).toHaveTextContent(/^\/w\/acme$/);
  });

  // The dashboards backend: GET /workspaces/:ws/dashboards answers {dashboards: [...]} with the caller's
  // virtual default (id "default") until their first change; its widgets have no ids yet.
  it("server contract: the {dashboards} list leads to the virtual default, read under the workspace", async () => {
    const virtualDefault = doc([
      { widget_type: "mentions", x: 0, y: 0, w: 12, h: 2 } as WidgetInstance,
      { widget_type: "my-tasks", x: 0, y: 2, w: 6, h: 4 } as WidgetInstance,
    ], { id: "default", virtual: true, version: 0 });
    serve(virtualDefault, {
      dashboards: [
        { id: "s1", name: "Shared", icon: "", placement: "bottom", position: 0 },
        { id: "default", name: "Main", icon: "", placement: "top", position: 0, virtual: true },
      ],
    });
    mount("/w/acme/dashboard");
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/w/acme/d/default"));
    const grid = await screen.findByTestId("dashboard-grid");
    expect(vi.mocked(api).mock.calls.some(([u]) => u === "/api/v1/workspaces/ws1/dashboards/default")).toBe(true);
    expect(grid.querySelectorAll("[data-widget-instance]")).toHaveLength(2);
    expect(screen.queryByText(/not found|не найден/i)).toBeNull();
  });

  it("virtual default: widgets without ids get their own cells and expand links; a save sends no made-up ids", async () => {
    const virtualDefault = doc([
      { widget_type: "nope-x", x: 0, y: 0, w: 6, h: 2 } as WidgetInstance,
      { widget_type: "nope-x", x: 6, y: 0, w: 6, h: 2 } as WidgetInstance,
    ], { id: "default", virtual: true, version: 0 });
    serve(virtualDefault);
    mount("/w/acme/d/default");
    const grid = await screen.findByTestId("dashboard-grid");
    const cells = [...grid.querySelectorAll<HTMLElement>("[data-widget-instance]")];
    expect(cells).toHaveLength(2);
    expect(new Set(cells.map((c) => c.dataset.widgetInstance)).size).toBe(2);
    fireEvent.click(within(cells[1]!).getByRole("button", { name: /remove widget|убрать виджет/i }));
    await waitFor(() => {
      const put = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/workspaces/ws1/dashboards/default/layout");
      expect(put?.[1]).toMatchObject({ method: "PUT", body: { version: 0, widgets: [{ widget_type: "nope-x", x: 0, y: 0, w: 6, h: 2 }] } });
      expect((put![1] as { body: { widgets: object[] } }).body.widgets[0]).not.toHaveProperty("id");
    });
  });

  it("a real list failure (500) is an error with Retry, not the legacy page", async () => {
    serve(doc(FOUR), new ApiRequestError("down", "internal", 500));
    mount("/w/acme");
    expect(await screen.findByRole("alert")).toHaveTextContent("down");
    expect(document.querySelector("[data-mesh-widget]")).toBeNull();
  });
});

describe("dashboard.layout slot", () => {
  it("a registered dashboard.layout override draws the page, with the doc's widgets as SDK instances with coordinates", async () => {
    const off = registerSlotViewOverride("dashboard.layout", (({ data }: { data: { columns: number; widgets: { descriptor: { id: string }; instance: { x: number; y: number; w: number } }[] } }) => (
      <div data-testid="theme-layout">
        {data.columns}:{data.widgets.map((w) => `${w.descriptor.id}@${w.instance.x},${w.instance.y},${w.instance.w}`).join(" ")}
      </div>
    )) as never);
    try {
      serve(doc([W("a", "sessions", 6, 2, 6, 4), W("b", "my-tasks", 0, 0, 6, 4), W("c", "legacy-unknown", 0, 9, 3, 2)]));
      mount();
      expect(await screen.findByTestId("theme-layout")).toHaveTextContent("12:my-tasks@0,0,6 sessions@6,2,6 legacy-unknown@0,9,3");
      expect(screen.queryByTestId("dashboard-grid")).toBeNull();
    } finally {
      off();
    }
  });

  it("under a theme layout an unknown type keeps its stub and 'Remove widget' (PRD §7.2)", async () => {
    type Placed = { instance: { id: string } };
    const off = registerSlotViewOverride("dashboard.layout", (({ data }: { data: { widgets: Placed[]; components: { Widget: (p: { instance: unknown }) => React.ReactNode } } }) => (
      <div data-testid="theme-layout">
        {data.widgets.map((w) => (
          <data.components.Widget key={w.instance.id} instance={w.instance} />
        ))}
      </div>
    )) as never);
    try {
      serve(doc([W("i1", "mentions", 0, 0, 6, 2), W("i9", "review-unknown", 6, 0, 6, 2)]));
      mount();
      const layout = await screen.findByTestId("theme-layout");
      expect(screen.queryByTestId("dashboard-grid")).toBeNull();
      const stub = within(layout).getByTestId("widget-stub");
      expect(stub).toHaveTextContent("review-unknown");
      fireEvent.click(within(stub).getByRole("button", { name: /Remove widget|Убрать виджет/ }));
      await waitFor(() => {
        const put = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1/layout");
        expect(put).toBeTruthy();
        expect((put![1] as { body: { widgets: WidgetInstance[] } }).body.widgets.map((w) => w.id)).toEqual(["i1"]);
      });
    } finally {
      off();
    }
  });

  it("a ^1 theme layout reaches the same cells by descriptor id: the unknown type is a stub with 'Remove widget'", async () => {
    setLayoutMajor(1);
    const off = registerSlotViewOverride("dashboard.layout", (({ data }: { data: { widgets: { descriptor: { id: string } }[]; components: { Widget: (p: { id: string }) => React.ReactNode } } }) => (
      <div data-testid="theme-layout">
        {data.widgets.map((w) => (
          <data.components.Widget key={w.descriptor.id} id={w.descriptor.id} />
        ))}
      </div>
    )) as never);
    try {
      serve(doc([W("i1", "mentions", 0, 0, 6, 2), W("i9", "review-unknown", 6, 0, 6, 2)]));
      mount();
      const layout = await screen.findByTestId("theme-layout");
      expect(layout.querySelector('a[href="/w/acme/d/d1?w=i1"]')).toBeTruthy();
      const stub = within(layout).getByTestId("widget-stub");
      expect(stub).toHaveTextContent("review-unknown");
      expect(within(stub).getByRole("button", { name: /Remove widget|Убрать виджет/ })).toBeTruthy();
    } finally {
      off();
      setLayoutMajor(2);
    }
  });

  it("a ^1 theme keeps instance identity for two widgets of one type: each cell, expand and remove are its own", async () => {
    setLayoutMajor(1);
    const off = registerSlotViewOverride("dashboard.layout", (({ data }: { data: { widgets: { descriptor: { id: string } }[]; components: { Widget: (p: { id: string }) => React.ReactNode } } }) => (
      <div data-testid="theme-layout">
        {data.widgets.map((w) => (
          <section key={w.descriptor.id} data-v1-id={w.descriptor.id}>
            <data.components.Widget id={w.descriptor.id} />
          </section>
        ))}
      </div>
    )) as never);
    try {
      serve(doc([W("i1", "mentions", 0, 0, 6, 2), W("i2", "mentions", 6, 0, 6, 2), W("u1", "review-unknown", 0, 2, 6, 2), W("u2", "review-unknown", 6, 2, 6, 2)]));
      mount();
      const layout = await screen.findByTestId("theme-layout");
      const cells = [...layout.querySelectorAll("section")];
      expect(cells).toHaveLength(4);
      // the first of a type keeps the plain descriptor id; the second carries its instance
      expect(cells.map((c) => c.dataset.v1Id)).toEqual(["mentions", "mentions:i2", "review-unknown", "review-unknown:u2"]);
      expect(cells[0]!.querySelector('a[href="/w/acme/d/d1?w=i1"]')).toBeTruthy();
      expect(cells[1]!.querySelector('a[href="/w/acme/d/d1?w=i2"]')).toBeTruthy();
      fireEvent.click(within(cells[3]!).getByRole("button", { name: /Remove widget|Убрать виджет/ }));
      await waitFor(() => {
        const put = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1/layout");
        expect(put).toBeTruthy();
        expect((put![1] as { body: { widgets: WidgetInstance[] } }).body.widgets.map((w) => w.id)).toEqual(["i1", "i2", "u1"]);
      });
    } finally {
      off();
      setLayoutMajor(2);
    }
  });

  it("a theme layout that crashes falls back to the coordinate grid", async () => {
    const quiet = vi.spyOn(console, "error").mockImplementation(() => {});
    const off = registerSlotViewOverride("dashboard.layout", (() => {
      throw new Error("theme layout broke");
    }) as never);
    try {
      serve(doc(FOUR));
      mount();
      expect(await screen.findByTestId("dashboard-grid")).toBeTruthy();
    } finally {
      off();
      quiet.mockRestore();
    }
  });

  it("without an override the coordinate grid is the layout", async () => {
    serve(doc(FOUR));
    mount();
    expect((await screen.findByTestId("dashboard-grid")).dataset.cols).toBe("12");
  });
});

describe("dashboard page: extension widgets (PRD §7.2)", () => {
  const gitlab = (enabled: boolean) => ({
    id: "gitlab",
    name: "GitLab",
    manifest: {
      capabilities: { ui: ["dashboard.widget"] },
      widgets: [{ id: "failed-pipelines", title: "Failed pipelines", title_i18n: { ru: "Упавшие пайплайны" }, scope: "project", render: { builtin: "GitlabFailedPipelinesWidget" } }],
    },
    status: { enabled, source: "instance", consented_capabilities: [] },
  });
  const LAYOUT = [W("i1", "mentions", 0, 0, 6, 2), W("i9", "gitlab.failed-pipelines", 6, 0, 6, 4, { project_id: "p1" })];
  function serveExt(enabled: boolean) {
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url === "/api/v1/workspaces/ws1/extensions") return { extensions: [gitlab(enabled)] };
      if (url.startsWith("/api/v1/workspaces/ws1/widgets/gitlab/failed-pipelines")) return { items: [] };
      if (url.startsWith("/api/v1/workspaces/ws1/dashboards/")) return doc(LAYOUT);
      if (url.endsWith("/dashboards")) return [];
      return [];
    });
  }
  beforeEach(() => resetExtensionWidgetsForTests());

  it("an enabled extension draws its widget in the cell with the manifest title and its settings", async () => {
    serveExt(true);
    mount();
    const card = await screen.findByTestId("extension-widget-card");
    expect(card.closest("[data-widget-instance]")?.getAttribute("data-widget-instance")).toBe("i9");
    await waitFor(() => expect(card).toHaveTextContent(/Failed pipelines|Упавшие пайплайны/));
    expect(await within(card).findByTestId("gitlab-failed-pipelines-empty")).toBeTruthy();
    expect(vi.mocked(api).mock.calls.some(([u]) => u === "/api/v1/workspaces/ws1/widgets/gitlab/failed-pipelines?project_id=p1")).toBe(true);
    expect(screen.queryByTestId("widget-stub")).toBeNull();
  });

  it("a disabled extension keeps the instance with its stub and 'Remove widget'", async () => {
    serveExt(false);
    mount();
    const stub = await screen.findByTestId("extension-widget-stub");
    expect(stub).toHaveTextContent("GitLab");
    expect(stub.closest("[data-widget-instance]")?.getAttribute("data-widget-instance")).toBe("i9");
    fireEvent.click(within(stub).getByTestId("extension-widget-remove"));
    await waitFor(() => {
      const put = vi.mocked(api).mock.calls.find(([u]) => u === "/api/v1/workspaces/ws1/dashboards/d1/layout");
      expect(put).toBeTruthy();
      expect((put![1] as { body: { widgets: WidgetInstance[] } }).body.widgets.map((w) => w.id)).toEqual(["i1"]);
    });
  });
});
