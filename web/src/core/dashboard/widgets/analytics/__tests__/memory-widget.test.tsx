// The «Память Mesh» widget: the filter (instance settings → §6 request parameters), the mockup's tile
// order, «no data» apart from a real 0, the journal-since note, the legend's line item, and the two
// main actions — a click on an agent's row narrows the whole widget, a period change refetches.
// The network is the only mock (as in widgets.test.tsx): the view runs the real useCachedGet and cache.
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { userEvent } from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { useAgentStore } from "@/stores/agent";
import { useAuthStore } from "@/stores/auth";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import { CACHE_MS, clearAnalyticsCache } from "../use-analytics";
import { activeFilterCount, memoryDataParams, memoryExportRequest, memoryParams, readMemoryFilter } from "../memory-filters";
import { previousPeriod } from "../filters";
import { detailFromWire, fromWire, lastDay } from "../memory-wire";
import * as e2e from "../../../../../../e2e-memory/fixtures";
import golden from "../../../../../../e2e-memory/u5-golden.json";
import type { WireEmptyQuery, WireKpis, WireList, WireMemoryAnalytics, WireRecordRow, WireRow, WireUnknownSource } from "../memory-wire";
import { views, type AnalyticsActions } from "../views";

vi.mock("@/lib/api", () => ({ api: vi.fn(), apiBlob: vi.fn(), getAccessToken: () => null }));
import { api, apiBlob } from "@/lib/api";

function view(settings: Record<string, unknown> = {}, actions: AnalyticsActions = {}): ReactElement {
  const View = views["memory-usage"];
  return <View instance={{ id: "w1", type: "memory-usage", x: 0, y: 0, w: 12, h: 8, settings }} settings={settings} size={{ w: 12, h: 8 }} actions={actions} />;
}

// -- fixtures ---------------------------------------------------------------------------------------------

const day = (offsetDays: number) => new Date(Date.now() + offsetDays * 86_400_000).toISOString().slice(0, 10);

// Every fixture is the U5 server's own JSON (memory-wire.ts mirrors internal/service/memory_analytics_types.go):
// the widget is tested through the same adapter production uses.

function kpis(over: Partial<WireKpis> = {}): WireKpis {
  return {
    reads: 84,
    writes: 30,
    with_reason: 24,
    hit_rate: 0.75,
    useful_rate: 0.6,
    miss_rate: 0.25,
    dead30: 0.4,
    tokens_est: 12_000,
    superseded_served_rate: 0.05,
    ...over,
  };
}

const row = (id: string, label: string, over: Partial<WireRow> = {}): WireRow => ({
  key: id,
  label,
  key2: null,
  reads: id === "a1" ? 50 : 34,
  hits: id === "a1" ? 40 : 23,
  useful: id === "a1" ? 28 : 12,
  writes: id === "a1" ? 20 : 10,
  with_reason: id === "a1" ? 16 : 8,
  hit_rate: id === "a1" ? 0.8 : 0.65,
  useful_rate: id === "a1" ? 0.7 : 0.5,
  tokens_est: id === "a1" ? 8000 : 4000,
  served_rows: id === "a1" ? 90 : 40,
  superseded_served_rate: 0.05,
  reasons: {},
  ...over,
});

function analytics(over: Partial<WireMemoryAnalytics> = {}): WireMemoryAnalytics {
  return {
    period: { from: `${day(-7)}T00:00:00Z`, to: `${day(0)}T12:00:00Z` },
    previous_period: { from: `${day(-15)}T00:00:00Z`, to: `${day(-7)}T00:00:00Z` },
    kpis: kpis(),
    previous_kpis: kpis({ reads: 70, useful_rate: 0.5, miss_rate: 0.3, dead30: 0.45, tokens_est: 9000, writes: 25 }),
    series: Array.from({ length: 7 }, (_, i) => ({
      day: day(i - 6),
      reads: 12,
      reads_by_op: { recall: 8, recall_with_graph: 0, get_memory: 4, get_project_knowledge: 0, get_project_updates: 0 },
      served_rows_by_source: { memory: 20, project_knowledge: 0, doc: 5, unknown: 0 },
      useful_rate: 0.6,
      writes: 4,
      with_reason: 3,
      hits: 6,
      useful: 4,
      reasons: {},
    })),
    rows: [row("a1", "Liam"), row("a2", "Maya")],
    totals: row("total", "total", { reads: 84, hits: 63, useful: 38, writes: 30, with_reason: 24, hit_rate: 0.75, useful_rate: 0.6, tokens_est: 12_000 }),
    quality: { day: day(-1), total: 130, active: 100, archived: 30, superseded_active: 8, conflicted: 2, no_kind: 10, statuslog_like: 5, dup_groups: 3, dup_rows: 7, dead30: 40, dead90: 25 },
    coverage: { journal_since: "2020-01-01T00:00:00Z", unknown_share: 0.02, caller_kind: [{ caller_kind: "agent_tool", reads: 70 }, { caller_kind: "runner", reads: 14 }] },
    reasons: {},
    ...over,
  };
}

const list = <T,>(items: T[], over: Partial<WireList<T>> = {}): WireList<T> => ({ items, total: items.length, page: 1, page_size: 20, ...over });
const emptyQuery = (over: Partial<WireEmptyQuery> = {}): WireEmptyQuery => ({
  query_hash: "h1", query_text: "how to restart runner", redacted: false, agent_id: "a1", agent_name: "Liam", count: 3, last_at: "2026-11-17T10:00:00Z", ...over,
});
const record = (over: Partial<WireRecordRow> = {}): WireRecordRow => ({
  memory_id: "m1", key: "deploy-url", redacted: false, scope: "workspace", kind: "fact", project_id: null, agent_id: "a1",
  created_at: "2026-10-01T00:00:00Z", served: 12, useful: 5, last_served_at: "2026-11-16T09:00:00Z", ...over,
});
const unknownSource: WireUnknownSource = { route: "/api/v1/memories/recall", user_agent: "curl/8", reads: 9, sample_keys: ["deploy-url"] };

/** The detail list of a URL, each in its own row type. */
function detailOf(url: string): WireList<unknown> {
  if (url.endsWith("/empty-queries")) return list([emptyQuery()]);
  if (url.endsWith("/unknown-sources")) return list([unknownSource]);
  return list([record()]);
}

type Params = Record<string, string>;
const calls = () => vi.mocked(api).mock.calls.map(([url, opts]) => ({ url: String(url), params: (opts?.params ?? {}) as Params }));
const lastParams = () => calls().slice(-1)[0]!.params;

/** Serves the main payload by its parameters (period length and agent filter are visible in the numbers). */
function serve(impl?: (p: Params, url: string) => unknown) {
  vi.mocked(api).mockImplementation(async (url, opts) => {
    const p = (opts?.params ?? {}) as Params;
    const u = String(url);
    if (impl) return impl(p, u) as never;
    if (u.includes("/analytics/memory/")) return detailOf(u) as never;
    // a preset sends only `from` (the server derives the period): its distance to now is the period length
    const days = p.from ? Math.max(1, Math.floor((Date.now() - Date.parse(p.from)) / 86_400_000)) : 7;
    const only = p.agent_id?.split(",")[0];
    const base = analytics();
    return {
      ...base,
      kpis: kpis({ reads: days * 12, useful_rate: only ? 0.7 : 0.6 }),
      rows: only ? [row(only, only === "a1" ? "Liam" : "Maya")] : base.rows,
    } satisfies WireMemoryAnalytics as never;
  });
}

async function mount(settings: Record<string, unknown> = {}) {
  const utils = render(
    <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
      <Routes>
        <Route path="/w/:wsSlug/dashboard" element={<>{view(settings)}</>} />
      </Routes>
    </MemoryRouter>,
  );
  await screen.findByTestId("memory-kpis");
  return utils;
}

const tile = (id: string) => document.querySelector<HTMLElement>(`[data-kpi="${id}"]`)!;

beforeEach(() => {
  clearAnalyticsCache();
  vi.mocked(api).mockReset();
  vi.mocked(apiBlob).mockReset();
  serve();
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws1", slug: "acme", name: "Acme" } as never });
  useAuthStore.setState({ user: { id: "owner" } as never, isAuthenticated: true });
  useProjectStore.setState({ projects: [{ id: "p1", slug: "core", name: "Core" } as never] });
  useAgentStore.setState({ agents: [
    { id: "a1", name: "Liam" } as never,
    { id: "a2", name: "Maya" } as never,
  ] });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

// -- the filter (pure) --------------------------------------------------------------------------------------

describe("readMemoryFilter and memoryParams", () => {
  it("empty settings open on a week of agent_tool reads by agent, as a table", () => {
    const f = readMemoryFilter(undefined);
    expect(f.period).toEqual({ days: 7 });
    expect(f.callers).toEqual(["agent_tool"]);
    expect(f.groupBy).toBe("agent");
    expect(f.view).toBe("table");
    expect(f.agents).toEqual([]);
    expect(f.op).toBe("all");
    expect(activeFilterCount(f)).toBe(0);
  });

  it("instance settings seed every dimension; a malformed value falls back to its default", () => {
    const f = readMemoryFilter({ period: { days: 90 }, project: "p1", agents: ["a2", "a1"], op: "recall", sources: ["memory", "doc"], callers: [], group_by: "project", group_by2: "project", view: "chart" });
    expect(f.period).toEqual({ days: 90 });
    expect(f.projectId).toBe("p1");
    expect(f.agents).toEqual(["a2", "a1"]);
    expect(f.op).toBe("recall");
    expect(f.sources).toEqual(["memory", "doc"]);
    expect(f.callers).toEqual([]);
    expect(f.groupBy).toBe("project");
    expect(f.groupBy2).toBeUndefined(); // the second split equal to the first is dropped
    expect(f.view).toBe("chart");
    const broken = readMemoryFilter({ group_by: "bogus", op: 7, view: null, callers: "yes", agents: [1, "a1", "a1"] });
    expect(broken.groupBy).toBe("agent");
    expect(broken.op).toBe("all");
    expect(broken.view).toBe("table");
    expect(broken.callers).toEqual(["agent_tool"]);
    expect(broken.agents).toEqual(["a1"]);
  });

  it("the §6 parameters: lists sorted and comma-joined, `all` and the default split stay out", () => {
    const now = new Date("2026-11-18T12:00:00Z");
    const p = memoryParams(readMemoryFilter({ agents: ["a2", "a1"], sources: ["doc", "memory"], callers: ["ui", "runner"], group_by: "agent", group_by2: "day" }), now);
    expect(p.agent_id).toBe("a1,a2");
    expect(p.source).toBe("doc,memory");
    expect(p.caller_kind).toBe("runner,ui");
    expect(p.group_by).toBe("agent");
    expect(p.group_by2).toBe("day");
    expect(p.from).toBe("2026-11-11");
    expect(p.to).toBeUndefined(); // a preset sends only its first day
    expect(p.op).toBeUndefined();
    const def = memoryParams(readMemoryFilter(undefined), now);
    expect(def.caller_kind).toBe("agent_tool");
    expect(def.group_by2).toBeUndefined();
  });

  it("the data request carries the comparison window of the shared previousPeriod; CSV has none", () => {
    const now = new Date("2026-11-18T12:00:00Z");
    const preset = readMemoryFilter({ period: { days: 7 } });
    const p = memoryDataParams(preset, now);
    expect(p.prev_from).toBe("2026-11-03T12:00:00Z");
    // the window's last day is sent to its end: the API reads a bare date as that day's midnight, exclusive
    expect(p.prev_to).toBe("2026-11-10T23:59:59Z");
    const shared = previousPeriod(preset.period, now);
    expect({ from: p.prev_from, to: p.prev_to }).toEqual({ from: shared.from, to: `${shared.to}T23:59:59Z` });
    expect(p.from).toBe(memoryParams(preset, now).from);
    const custom = memoryDataParams(readMemoryFilter({ period: { from: "2026-11-01", to: "2026-11-10" } }), now);
    expect([custom.prev_from, custom.prev_to]).toEqual(["2026-10-22", "2026-10-31T23:59:59Z"]);
    expect(memoryExportRequest("ws1", preset).params.prev_from).toBeUndefined();
  });

  it("the CSV export asks the memory endpoint with the same parameters", () => {
    const { path, params } = memoryExportRequest("ws1", readMemoryFilter({ op: "recall" }));
    expect(path).toBe("/api/v1/workspaces/ws1/analytics/memory/export");
    expect(params.format).toBe("csv");
    expect(params.op).toBe("recall");
  });
});

// -- the fixtures against the server's own JSON -------------------------------------------------------------

// u5-golden.json: the U5 server's serialization of internal/service/memory_analytics_types.go with every field set
// (generated from the Go structs; U5's own test keeps it in step with them). Keys the Go side may omit (omitempty).
const OMITTABLE = new Set(["reasons", "label2", "reason", "group_id", "group_size", "coverage"]);
type Obj = Record<string, unknown>;

/** A fixture object keeps the server's keys: none it does not send, and none it always sends missing. */
function sameKeys(path: string, fixture: unknown, server: unknown) {
  const f = Object.keys(fixture as Obj).sort();
  const s = Object.keys(server as Obj).sort();
  expect(f.filter((k) => !s.includes(k)), `${path}: keys the server does not send`).toEqual([]);
  expect(s.filter((k) => !f.includes(k) && !OMITTABLE.has(k)), `${path}: keys the server always sends`).toEqual([]);
}

function sameAnalytics(name: string, f: Obj, g: Obj) {
  sameKeys(name, f, g);
  for (const k of ["period", "previous_period", "kpis", "previous_kpis", "totals", "quality", "coverage"]) sameKeys(`${name}.${k}`, f[k], g[k]);
  sameKeys(`${name}.series[]`, (f.series as Obj[])[0], (g.series as Obj[])[0]);
  sameKeys(`${name}.rows[]`, (f.rows as Obj[])[0], (g.rows as Obj[])[0]);
  const callers = (f.coverage as Obj).caller_kind as Obj[];
  if (callers.length) sameKeys(`${name}.coverage.caller_kind[]`, callers[0], ((g.coverage as Obj).caller_kind as Obj[])[0]);
}

describe("fixtures and the U5 answer", () => {
  const g = golden as unknown as Obj;
  const ga = g.analytics as Obj;
  const params = new URLSearchParams({ from: day(-7) });

  it("the unit and Playwright payloads carry exactly the server's keys", () => {
    sameAnalytics("unit", analytics() as unknown as Obj, ga);
    sameAnalytics("e2e main", e2e.mainPayload(params) as unknown as Obj, ga);
    sameAnalytics("e2e empty journal", e2e.emptyJournalPayload(params) as unknown as Obj, ga);
    const lists: [string, unknown, string][] = [
      ["unit empty query", emptyQuery(), "empty_queries"],
      ["unit record", record(), "records"],
      ["unit unknown source", unknownSource, "unknown_sources"],
      ["e2e empty query", e2e.emptyQuery, "empty_queries"],
      ["e2e record", e2e.record, "records"],
      ["e2e unknown source", e2e.unknownSource, "unknown_sources"],
    ];
    for (const [name, item, list] of lists) {
      sameKeys(`${name} list`, e2e.page1([item]), g[list]);
      sameKeys(name, item, ((g[list] as Obj).items as Obj[])[0]);
    }
  });

  it("a fixture with a key the server does not send fails the check (control)", () => {
    expect(() => sameKeys("control", { ...emptyQuery(), per_page: 20 }, ((g.empty_queries as Obj).items as Obj[])[0])).toThrow(/does not send/);
    const { last_at: _dropped, ...partial } = emptyQuery();
    expect(() => sameKeys("control", partial, ((g.empty_queries as Obj).items as Obj[])[0])).toThrow(/always sends/);
  });

  it("the server's own answer goes through the adapter whole", () => {
    const m = fromWire(golden.analytics as unknown as WireMemoryAnalytics, "agent");
    expect(m.kpis.reads).toBe(84);
    expect(m.kpis.reads_by_op).toEqual({ recall: 8, get_memory: 4 });
    expect(m.kpis.dead30_count).toBe(40);
    expect(m.kpis.hits).toBe(40);
    expect(m.previous_period?.kpis.reads).toBe(84);
    expect(m.period.to).toBe("2026-11-18");
    expect(m.rows[0]).toMatchObject({ agent_id: "6a1b669f-5cb4-4822-99fc-45cdc5b0fc95", label: "Liam", label2: "recall", reads_per_write: 2.5 });
    expect(m.quality).toMatchObject({ no_kind: 10, statuslog_like: 5, with_reason_rate: 0.8 });
    expect(m.coverage.caller_kind).toEqual({ agent_tool: 70 });
    expect(m.series[0]).toMatchObject({ date: "2026-11-17", served_rows_by_source: { memory: 20, doc: 5 } });
    const eq = detailFromWire("empty-queries", golden.empty_queries as WireList<unknown>);
    expect(eq.items[0]).toMatchObject({ label: "how to restart runner", count: 3, extra: ["Liam"], reason: "text_expired" });
    expect(eq).toMatchObject({ page_size: 20, reason: "no_data", coverage: { reason: "retention" } });
    expect(detailFromWire("duplicates", golden.records as WireList<unknown>).items[0]).toMatchObject({ label: "deploy-url", count: 2, kind: "fact", scope: "workspace" });
    expect(detailFromWire("top-records", golden.records as WireList<unknown>).items[0]).toMatchObject({ count: 12, useful: 5 });
    expect(detailFromWire("unknown-sources", golden.unknown_sources as WireList<unknown>).items[0]).toMatchObject({ label: "/api/v1/memories/recall", count: 9, extra: ["curl/8", "deploy-url"] });
  });
});

// -- the widget ----------------------------------------------------------------------------------------------

describe("MemoryUsageView", () => {
  it.each([{ group_by: "kind" }, { group_by: "scope" }, { group_by: "agent", group_by2: "kind" }])("$group_by/$group_by2 split shows writes without read metrics, on every view", async (settings) => {
    const user = userEvent.setup();
    serve(() => analytics({
      rows: [row("fact", "fact", { reads: null, hits: null, useful: null, hit_rate: null, useful_rate: null, with_reason: 12, reasons: { reads: "not_applicable" } })],
      totals: row("total", "total", { reads: null, writes: 30, with_reason: 20 }),
    }));
    await mount(settings);
    expect(document.querySelectorAll("[data-kpi]")).toHaveLength(1);
    expect(tile("writes")).toHaveTextContent("30");
    expect(screen.queryByTestId("memory-chart")).toBeNull();
    expect(screen.getByTestId("memory-writes-note")).toHaveTextContent("only writes");
    const table = screen.getByTestId("memory-table");
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(settings.group_by2 ? ["Agent", "Kind", "Writes", "With a reason", ""] : [settings.group_by === "kind" ? "Kind" : "Scope", "Writes", "With a reason", ""]);
    expect(table).toHaveTextContent("12");
    await user.click(screen.getByRole("button", { name: "Top-N" }));
    expect(await screen.findByTestId("memory-top")).toHaveTextContent("by writes");
    await user.click(screen.getByRole("button", { name: "Chart" }));
    expect(screen.queryByTestId("memory-chart")).toBeNull();
    expect(screen.getByTestId("memory-breakdown")).toBeTruthy();
  });

  it("unknown sources opens a detail list with the current caller and agent filters", async () => {
    await mount({ agents: ["a1"], callers: ["unknown"] });
    await userEvent.click(screen.getByRole("button", { name: "unknown sources" }));
    await screen.findByTestId("memory-detail");
    const request = calls().find((c) => c.url.endsWith("/unknown-sources"));
    expect(request?.params.agent_id).toBe("a1");
    expect(request?.params.caller_kind).toBe("unknown");
  });

  it("asks /analytics/memory with the default filter and draws the six tiles in the mockup's order", async () => {
    await mount();
    const p = lastParams();
    expect(calls().slice(-1)[0]!.url).toContain("/analytics/memory");
    expect(p.group_by).toBe("agent");
    expect(p.caller_kind).toBe("agent_tool");
    expect(p.prev_from).toBeTruthy();
    expect(p.prev_to).toBeTruthy();
    expect([...document.querySelectorAll("[data-kpi]")].map((e) => e.getAttribute("data-kpi"))).toEqual([
      "useful_rate", "miss_rate", "dead30", "tokens_est", "reads", "writes",
    ]);
    // hit-rate is the substring of the useful-recall tile, not a tile
    expect(within(tile("useful_rate")).getByText(/hit-rate 75%/)).toBeTruthy();
    // the delta to the previous period is drawn
    expect(within(tile("reads")).getByText("+14 vs previous")).toBeTruthy();
  });

  it("a null metric is «no data» with its reason; a real 0 stays 0", async () => {
    serve((_p, u) => {
      if (u.includes("/analytics/memory/")) return detailOf(u) as never;
      return analytics({ kpis: kpis({ reads: null, miss_rate: 0 }), reasons: { reads: "no_journal_before" }, coverage: { journal_since: `${day(-2)}T00:00:00Z`, unknown_share: null, caller_kind: [] } }) as never;
    });
    await mount();
    const reads = tile("reads");
    expect(within(reads).getByText("no data")).toBeTruthy();
    expect(within(reads).getByText(/kept since/)).toBeTruthy();
    expect(reads.hasAttribute("data-nodata")).toBe(true);
    // negative control: a counted 0 is a number, not «no data»
    const miss = tile("miss_rate");
    expect(within(miss).getByText("0%")).toBeTruthy();
    expect(miss.hasAttribute("data-nodata")).toBe(false);
  });

  it("a re-render minutes later asks nothing new: the comparison window is pinned per filter", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(new Date("2026-11-18T12:00:30Z"));
      const utils = await mount();
      const asked = calls().filter((c) => c.url.endsWith("/analytics/memory")).length;
      vi.setSystemTime(new Date("2026-11-18T12:03:30Z")); // prev_from is cut to the minute: it would move
      utils.rerender(
        <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
          <Routes>
            <Route path="/w/:wsSlug/dashboard" element={<>{view({})}</>} />
          </Routes>
        </MemoryRouter>,
      );
      await Promise.resolve();
      expect(calls().filter((c) => c.url.endsWith("/analytics/memory")).length).toBe(asked);
      expect(screen.getByTestId("memory-kpis")).toBeTruthy(); // no skeleton in between
    } finally {
      vi.useRealTimers();
    }
  });

  it("the journal note names the date when the period starts before journal_since, and stays away when it does not", async () => {
    await mount();
    expect(screen.queryByTestId("memory-journal-note")).toBeNull(); // journal_since 2020 covers the period
    serve(() => analytics({ coverage: { journal_since: `${day(-3)}T00:00:00Z`, unknown_share: null, caller_kind: [] } }));
    cleanup();
    clearAnalyticsCache(); // the same parameters would answer from the cache of the mount above
    await mount();
    expect(screen.getByTestId("memory-journal-note").textContent).toMatch(/kept since/);
  });

  it("the bars are reads by operation (§7): the legend holds the line and only the ops the days used; a bar names its served rows", async () => {
    await mount();
    const legend = screen.getByTestId("memory-legend");
    expect([...legend.querySelectorAll("[data-legend]")].map((e) => e.getAttribute("data-legend"))).toEqual(["recall", "get_memory", "useful-line"]);
    expect(screen.getByTestId("memory-useful-line")).toBeTruthy(); // the line itself is drawn
    const day0 = document.querySelector<SVGGElement>(`[data-day="${day(0)}"]`)!;
    // the stack is the day's reads: 8 recall + 4 get_memory, zero ops draw nothing
    expect([...day0.querySelectorAll("rect")].map((r) => r.getAttribute("data-op"))).toEqual(["recall", "get_memory"]);
    expect(day0.querySelector("rect[data-op='recall'] title")!.textContent).toMatch(/recall: 8\nserved rows: memory 20 · documents 5$/);
  });

  it("a wide frame labels the line's points; a 393 frame keeps the chart 210 px tall with the percentages in the tooltips only", async () => {
    await mount(); // no ResizeObserver in jsdom: the 700-wide drawing
    const wide = screen.getByTestId("memory-chart");
    expect(wide.getAttribute("height")).toBe("210");
    expect(wide.querySelectorAll("[data-point-label]").length).toBeGreaterThan(0);
    cleanup();
    clearAnalyticsCache();
    // the frame of a 393 screen: the chart draws at its width instead of scaling a 700-wide drawing down to ~100 px
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(private cb: ResizeObserverCallback) {}
        observe() {
          this.cb([{ contentRect: { width: 345 } } as ResizeObserverEntry], this as unknown as ResizeObserver);
        }
        disconnect() {}
      },
    );
    try {
      await mount();
      const chart = await waitFor(() => {
        const c = screen.getByTestId("memory-chart");
        expect(c.hasAttribute("data-narrow")).toBe(true);
        return c;
      });
      expect(chart.getAttribute("viewBox")).toBe("0 0 345 210");
      expect(chart.getAttribute("height")).toBe("210");
      expect(chart.querySelectorAll("[data-point-label]")).toHaveLength(0);
      expect(chart.querySelector("circle title")!.textContent).toMatch(/useful recall, %: \d+%$/);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("the base quality reads the server's counts: shares of active records, writes with a reason from the KPIs", async () => {
    await mount();
    const q = screen.getByTestId("memory-quality");
    expect(q.querySelector("[data-quality='no_kind']")).toHaveTextContent("10 · 10%");
    expect(q.querySelector("[data-quality='statuslog_like']")).toHaveTextContent("5 · 5%");
    expect(q.querySelector("[data-quality='with_reason_rate']")).toHaveTextContent("80%"); // 24 of 30 writes
    expect(within(tile("dead30")).getByText("40 records not served in 30 days")).toBeTruthy(); // quality.dead30
    expect(within(tile("tokens_est")).getByText(/≈143 per read/)).toBeTruthy(); // 12 000 / 84
  });

  it("no base snapshot: every quality line is «no data» with the server's reason, not 0", async () => {
    const empty = { day: null, total: null, active: null, archived: null, superseded_active: null, conflicted: null, no_kind: null, statuslog_like: null, dup_groups: null, dup_rows: null, dead30: null, dead90: null };
    serve(() => analytics({ quality: empty, kpis: kpis({ dead30: null }), reasons: { quality: "not_collected", dead30: "not_collected" } }));
    await mount();
    const q = screen.getByTestId("memory-quality");
    for (const id of ["active", "dup_groups", "no_kind", "statuslog_like", "superseded_active", "conflicted"]) {
      expect(q.querySelector(`[data-quality='${id}']`)).toHaveTextContent("not collected yet");
    }
    expect(within(tile("dead30")).getByText("not collected yet")).toBeTruthy();
  });

  it("the table title ends on the period's last day: the server's exclusive midnight end is not a day more", async () => {
    serve(() => analytics({ period: { from: "2026-11-01T00:00:00Z", to: "2026-11-11T00:00:00Z" } }));
    await mount({ period: { from: "2026-11-01", to: "2026-11-10" } });
    expect(screen.getByTestId("memory-breakdown").querySelector("h4")).toHaveTextContent("By agent · 11/01–11/10 ·");
    expect(lastDay("2026-11-18T12:00:00Z")).toBe("2026-11-18"); // an open period ends today
  });

  it("a split by source names the multi-source search row, not its raw key", async () => {
    serve(() => analytics({ rows: [row("*", "поиск, несколько источников"), row("doc", "doc")] }));
    await mount({ group_by: "source" });
    const table = screen.getByTestId("memory-table");
    expect(within(table).getByText("search, several sources")).toBeTruthy();
    expect(within(table).getByText("documents")).toBeTruthy();
  });

  it("an agent click filters the widget; return and reselect reuse isolated cache entries within the same minute and TTL", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-21T19:57:59.900Z"));
    const user = userEvent.setup();
    await mount();
    expect(document.querySelectorAll('[data-testid="memory-table"] tr[data-row]')).toHaveLength(2);
    await user.click(within(document.querySelector("[data-row='a2']") as HTMLElement).getByRole("button", { name: "Maya" }));
    await waitFor(() => expect(lastParams().agent_id).toBe("a2"));
    await waitFor(() => expect(document.querySelectorAll("[data-row]")).toHaveLength(1));
    expect(within(tile("useful_rate")).getByText("70%")).toBeTruthy(); // the tiles re-read the agent's numbers
    const opening = calls()[0]!;
    expect(calls()).toHaveLength(2);
    expect(lastParams()).toEqual({ ...opening.params, agent_id: "a2" });
    await user.click(within(document.querySelector("[data-row='a2']") as HTMLElement).getByRole("button", { name: "Maya" }));
    // Same minute and younger than CACHE_MS: dropping the filter restores the exact opening key.
    await waitFor(() => expect(document.querySelectorAll("[data-row]")).toHaveLength(2));
    expect(within(tile("useful_rate")).getByText("60%")).toBeTruthy();
    expect(calls()).toHaveLength(2);
    // Returning to Maya also reuses her own entry, never the unfiltered answer.
    await user.click(within(document.querySelector("[data-row='a2']") as HTMLElement).getByRole("button", { name: "Maya" }));
    await waitFor(() => expect(document.querySelectorAll("[data-row]")).toHaveLength(1));
    expect(within(tile("useful_rate")).getByText("70%")).toBeTruthy();
    expect(calls()).toHaveLength(2);
  });

  it.each([
    { reason: "comparison minute changes before TTL", elapsed: 200, settings: {}, changedComparison: true, invalidate: false },
    { reason: "identical custom-range key reaches TTL", elapsed: CACHE_MS, settings: { period: { from: "2026-11-01", to: "2026-11-07" } }, changedComparison: false, invalidate: false },
    { reason: "same key is explicitly invalidated", elapsed: 0, settings: {}, changedComparison: false, invalidate: true },
  ])("returning from an agent refetches when $reason", async ({ elapsed, settings, changedComparison, invalidate }) => {
    // Date is the only controlled clock: real timers, effects, cache and HTTP promise lifecycle run normally.
    // 200 ms crosses the minute that previousPeriod includes in the preset key, well before the 30 s TTL.
    vi.useFakeTimers({ toFake: ["Date"] });
    const start = new Date("2026-11-21T19:57:59.900Z");
    vi.setSystemTime(start);
    const user = userEvent.setup();
    await mount(settings);
    const opening = calls()[0]!;
    await user.click(within(document.querySelector("[data-row='a2']") as HTMLElement).getByRole("button", { name: "Maya" }));
    await waitFor(() => expect(document.querySelectorAll("[data-row]")).toHaveLength(1));
    expect(within(tile("useful_rate")).getByText("70%")).toBeTruthy();
    expect(lastParams()).toEqual({ ...opening.params, agent_id: "a2" });
    expect(calls()).toHaveLength(2);

    vi.setSystemTime(start.getTime() + elapsed);
    if (invalidate) clearAnalyticsCache();
    // mock: external HTTP boundary — a fresh server answer must replace both cached totals and agent data.
    vi.mocked(api).mockResolvedValueOnce(analytics({ kpis: kpis({ useful_rate: 0.65 }) }) as never);
    await user.click(within(document.querySelector("[data-row='a2']") as HTMLElement).getByRole("button", { name: "Maya" }));
    await waitFor(() => expect(within(tile("useful_rate")).getByText("65%")).toBeTruthy());
    expect(document.querySelectorAll("[data-row]")).toHaveLength(2);
    expect(calls()).toHaveLength(3);
    expect(calls()[2]!.url).toBe(opening.url);
    expect(lastParams()).toEqual({
      ...opening.params,
      prev_from: changedComparison ? "2026-11-06T04:02:00Z" : opening.params.prev_from,
    });
    if (changedComparison) expect(lastParams().prev_from).not.toBe(opening.params.prev_from);
  });

  it("a period change refetches and the numbers follow the answer", async () => {
    const user = userEvent.setup();
    await mount();
    expect(within(tile("reads")).getByText("84")).toBeTruthy(); // 7 days × 12
    await user.click(screen.getByRole("button", { name: "30d" }));
    await waitFor(() => expect(within(tile("reads")).getByText("360")).toBeTruthy()); // 30 days × 12
    expect(lastParams().from).toBe(day(-30));
  });

  it("an empty read journal is the reason, never a zero", async () => {
    serve((_p, u) => {
      if (u.includes("/analytics/memory/")) return detailOf(u) as never;
      return analytics({
        kpis: kpis({ reads: null, hit_rate: null, useful_rate: null, miss_rate: null, dead30: null, tokens_est: null, writes: null }),
        reasons: { reads: "no_journal_before", useful_rate: "no_journal_before", miss_rate: "no_journal_before", dead30: "not_collected", tokens_est: "not_collected", writes: "no_journal_before" },
        coverage: { journal_since: null, unknown_share: null, caller_kind: [] },
      }) as never;
    });
    await mount();
    for (const id of ["useful_rate", "miss_rate", "dead30", "tokens_est", "reads", "writes"]) {
      expect(within(tile(id)).getByText("no data")).toBeTruthy();
    }
    expect(within(tile("dead30")).getByText("not collected yet")).toBeTruthy();
    expect(screen.getByTestId("memory-journal-note").textContent).toMatch(/not kept yet/);
  });

  it("an error is a framed alert with Retry; Retry asks again", async () => {
    let fails = true;
    vi.mocked(api).mockImplementation(async () => {
      if (fails) throw new Error("boom");
      return analytics() as never;
    });
    render(
      <MemoryRouter initialEntries={["/w/acme/dashboard"]}>
        <Routes>
          <Route path="/w/:wsSlug/dashboard" element={<>{view()}</>} />
        </Routes>
      </MemoryRouter>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Could not load memory statistics");
    fails = false;
    await userEvent.click(within(alert).getByRole("button", { name: "Retry" }));
    await screen.findByTestId("memory-kpis");
  });

  it("the views: top draws the ranked list, chart drops the breakdown", async () => {
    const user = userEvent.setup();
    await mount();
    await user.click(screen.getByRole("button", { name: "Top-N" }));
    await waitFor(() => expect(screen.getByTestId("memory-top")).toBeTruthy());
    expect(screen.queryByTestId("memory-breakdown")).toBeNull();
    expect(within(screen.getByTestId("memory-top")).getByText("Liam")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Chart" }));
    await waitFor(() => expect(screen.queryByTestId("memory-breakdown")).toBeNull());
    expect(screen.getByTestId("memory-chart")).toBeTruthy();
  });

  it("«empty queries» opens the §6 detail list with the widget's filter; a top record links to Memory", async () => {
    const user = userEvent.setup();
    await mount();
    await user.click(within(tile("miss_rate")).getByRole("button", { name: "empty queries" }));
    let dialog = await screen.findByTestId("memory-detail");
    expect(within(dialog).getByText("Empty queries")).toBeTruthy();
    expect(await within(dialog).findByText("how to restart runner")).toBeTruthy();
    expect(within(dialog).getByText(/^Liam · /)).toBeTruthy(); // the asking agent and the last time
    const asked = calls().find((c) => c.url.includes("/analytics/memory/empty-queries"));
    expect(asked).toBeTruthy();
    expect(asked!.params.page_size).toBe("20");
    expect(asked!.params.per_page).toBeUndefined();
    expect(asked!.params.sort).toBeUndefined(); // the server's default order is not sent
    expect(asked!.params.caller_kind).toBe("agent_tool");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByTestId("memory-detail")).toBeNull());
    await user.click(within(document.querySelector("[data-row='a1']") as HTMLElement).getByRole("button", { name: "top records" }));
    dialog = await screen.findByTestId("memory-detail");
    expect(await within(dialog).findByRole("link", { name: "deploy-url" })).toHaveAttribute("href", expect.stringContaining("/memories?q=deploy-url"));
    expect(within(dialog).getByText(/^fact · workspace · useful 5 · /)).toBeTruthy();
    expect(within(dialog).getByText("12")).toBeTruthy(); // served
    expect(calls().find((c) => c.url.endsWith("/top-records"))!.params.agent_id).toBe("a1");
  });

  it("details paginate, search and sort on the server and explain expired text", async () => {
    serve((p, url) => {
      if (!url.includes("/analytics/memory/")) return analytics();
      return list(
        [
          emptyQuery({ query_hash: `page-${p.page}`, query_text: p.q || `page ${p.page}`, count: 12 }),
          emptyQuery({ query_hash: "old", query_text: null, reason: "text_expired" }),
          emptyQuery({ query_hash: "private", query_text: null, redacted: true }),
        ],
        { total: 41, page: Number(p.page), coverage: { detail_since: "2026-11-01T00:00:00Z", reason: "retention" } },
      );
    });
    const user = userEvent.setup();
    await mount({ agents: ["a1"] });
    await user.click(within(tile("miss_rate")).getByRole("button", { name: "empty queries" }));
    const dialog = await screen.findByTestId("memory-detail");
    await within(dialog).findByText("page 1");
    expect(within(dialog).getByText(/outside the detail retention period/)).toBeTruthy();
    expect(within(dialog).getByText("query text has expired")).toBeTruthy();
    expect(within(dialog).getByText("hidden from you")).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Next" }));
    await within(dialog).findByText("page 2");
    expect(lastParams().page).toBe("2");
    expect(lastParams().agent_id).toBe("a1");
    await user.type(within(dialog).getByRole("searchbox"), "deploy");
    await within(dialog).findByText("deploy");
    expect(lastParams()).toMatchObject({ page: "1", q: "deploy", caller_kind: "agent_tool" });
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Sort" }), "last_at");
    await waitFor(() => expect(lastParams().sort).toBe("last_at"));
  });

  it("a detail list before the journal names the journal date, not «not kept yet»", async () => {
    serve((_p, url) => {
      if (!url.includes("/analytics/memory/")) return analytics();
      return list([emptyQuery({ query_hash: "q1", query_text: "deploy", count: 3 })], {
        total: 1,
        page: 1,
        coverage: { detail_since: "2026-11-19T00:00:00Z", reason: "no_journal_before" },
      });
    });
    const user = userEvent.setup();
    await mount();
    await user.click(within(tile("miss_rate")).getByRole("button", { name: "empty queries" }));
    const dialog = await screen.findByTestId("memory-detail");
    await within(dialog).findByText("deploy");
    const note = within(dialog).getByRole("note");
    expect(note.textContent).toContain("kept since");
    expect(note.textContent).not.toContain("not kept yet");
  });

  it("a quick Next survives the search debounce: only a changed search goes back to page 1", async () => {
    serve((p, url) => {
      if (!url.includes("/analytics/memory/")) return analytics();
      return list([emptyQuery({ query_hash: `page-${p.page}`, query_text: p.q ? `${p.q} p${p.page}` : `page ${p.page}` })], { total: 41, page: Number(p.page) });
    });
    const user = userEvent.setup();
    await mount();
    await user.click(within(tile("miss_rate")).getByRole("button", { name: "empty queries" }));
    const dialog = await screen.findByTestId("memory-detail");
    await within(dialog).findByText("page 1");
    await user.click(within(dialog).getByRole("button", { name: "Next" }));
    await within(dialog).findByText("page 2");
    await act(() => new Promise((r) => setTimeout(r, 400)));
    expect(within(dialog).getByText("page 2")).toBeTruthy();
    expect(lastParams().page).toBe("2");
    await user.type(within(dialog).getByRole("searchbox"), "deploy");
    await within(dialog).findByText("deploy p1");
    expect(lastParams()).toMatchObject({ page: "1", q: "deploy" });
  });

  it("CSV exports the memory endpoint, not the plain analytics one", async () => {
    const user = userEvent.setup();
    await mount();
    await user.click(screen.getByRole("button", { name: "CSV" }));
    await waitFor(() => expect(vi.mocked(apiBlob).mock.calls).toHaveLength(1));
    const [path, params] = vi.mocked(apiBlob).mock.calls[0]!;
    expect(path).toBe("/api/v1/workspaces/ws1/analytics/memory/export");
    expect(params!.format).toBe("csv");
  });
});
