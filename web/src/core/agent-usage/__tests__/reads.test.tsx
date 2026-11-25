import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { clearAgentUsageCache } from "../data";
import { StatsBody, useCapsStore } from "../stats-body";
import { DEFAULT_STATE } from "../state";
import { AgentUsageWidget } from "@/core/dashboard/widgets/agent-usage/view";
import type { ST } from "../format";
import { prodUsageFixture, usageFixture } from "./fixtures";

const st: ST = (_key, text, opts) => text.replace(/\{\{(\w+)\}\}/g, (_, k) => String(opts?.[k] ?? k));
const user = (id: string) => ({ id }) as NonNullable<ReturnType<typeof useAuthStore.getState>["user"]>;
const ws = { id: "ws", slug: "test" } as NonNullable<ReturnType<typeof useWorkspaceStore.getState>["currentWorkspace"]>;

beforeEach(() => {
  clearAgentUsageCache();
  useAuthStore.setState({ user: user("viewer") });
  useWorkspaceStore.setState({ currentWorkspace: ws });
  useCapsStore.setState({ byWs: {} });
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("real statistics reads at the HTTP boundary", () => {
  // The API folds the rest into «Others» without a count: one more read at the largest top counts them,
  // and its time is the «Updated» stamp under the table.
  it("counts «Others» from the full read and stamps when the answer arrived", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-19T13:40:00Z"));
    // mock: external boundary — browser HTTP, keeping the API helper and data hook real.
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const top = Number(new URL(String(input), "http://x").searchParams.get("top"));
      return new Response(JSON.stringify(prodUsageFixture(top)));
    });
    vi.stubGlobal("fetch", fetcher);
    useCapsStore.getState().set("ws", usageFixture().capabilities);
    try {
      render(<StatsBody state={{ ...DEFAULT_STATE, n: 5 }} onChange={vi.fn()} mode="page" links={{}} st={st} />);
      await waitFor(() => expect(screen.getByTestId("usage-sort-caption").textContent).toContain("Showing 5 of 8"));
      expect(document.querySelector('tr[data-row="__other__"] td')!.textContent).toBe("Others (3)");
      expect(fetcher.mock.calls.map(([u]) => new URL(String(u), "http://x").searchParams.get("top")).sort()).toEqual(["20", "5"]);
      expect(screen.getByTestId("usage-updated").textContent).toBe("Updated 16:40 MSK");
    } finally {
      vi.useRealTimers();
    }
  });

  it("asks nothing more when the answer has no «Others»", async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify(prodUsageFixture(20))));
    vi.stubGlobal("fetch", fetcher);
    useCapsStore.getState().set("ws", usageFixture().capabilities);
    render(<StatsBody state={DEFAULT_STATE} onChange={vi.fn()} mode="page" links={{}} st={st} />);
    await screen.findByTestId("usage-total-row");
    expect(document.querySelector('tr[data-row="__other__"]')).toBeNull();
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("retries a failed request and shares an identical pending query between views", async () => {
    let fail = true;
    // mock: external boundary — browser HTTP, keeping the API helper and data hook real.
    const fetcher = vi.fn(async () => {
      if (fail) return new Response(JSON.stringify({ message: "Unavailable" }), { status: 503 });
      return new Response(JSON.stringify(usageFixture()));
    });
    vi.stubGlobal("fetch", fetcher);
    useCapsStore.getState().set("ws", usageFixture().capabilities);
    const props = { state: DEFAULT_STATE, onChange: vi.fn(), mode: "page" as const, links: {}, st };
    const first = render(<StatsBody {...props} />);
    expect(screen.getByTestId("stats-loading")).toBeTruthy();
    await screen.findByRole("alert");
    expect(fetcher).toHaveBeenCalledTimes(1);
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByTestId("usage-total-row");
    first.unmount();
    render(<><StatsBody {...props} /><StatsBody {...props} /></>);
    await waitFor(() => expect(screen.getAllByTestId("usage-total-row")).toHaveLength(2));
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("discovers member capabilities before requesting a pasted admin-only slice", async () => {
    const data = usageFixture();
    data.capabilities = { cost: false, quota: false, group_by: ["model", "agent", "project"] };
    // mock: external boundary — HTTP response of the permissions-bearing endpoint.
    const fetcher = vi.fn(async (_url: RequestInfo | URL) => new Response(JSON.stringify(data)));
    vi.stubGlobal("fetch", fetcher);
    const change = vi.fn();
    render(<StatsBody state={{ ...DEFAULT_STATE, m: "quota", g: "account" }} onChange={change} mode="page" links={{}} st={st} />);
    await waitFor(() => expect(change).toHaveBeenCalled());
    expect(fetcher.mock.calls.every(([url]) => !String(url).includes("/quota"))).toBe(true);
    expect(String(fetcher.mock.calls[0]![0])).toContain("metric=tokens");
    expect(change.mock.calls[change.mock.calls.length - 1]![0]).toMatchObject({ m: "tokens", g: "model" });
    act(() => useAuthStore.setState({ user: user("other-viewer") }));
    expect(useCapsStore.getState().byWs).toEqual({});
  });

  it("keeps an embedded project filter after row drilldown and omits quota", async () => {
    // mock: external boundary — HTTP; every subsequent request is inspected for the pinned project.
    const fetcher = vi.fn(async (_url: RequestInfo | URL) => new Response(JSON.stringify(usageFixture())));
    vi.stubGlobal("fetch", fetcher);
    useCapsStore.getState().set("ws", usageFixture().capabilities);
    const view = render(<AgentUsageWidget settings={{ m: "quota" }} noQuota fixedFilter={{ project: ["pinned"] }} st={st} />);
    await screen.findByTestId("usage-table");
    fireEvent.click(screen.getAllByRole("button", { name: "Only this" })[0]!);
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    for (const [url] of fetcher.mock.calls) {
      const query = new URL(String(url), "http://localhost");
      expect(query.pathname).toContain("/usage");
      expect(query.searchParams.get("project_id")).toBe("pinned");
      expect(query.searchParams.get("metric")).toBe("cost");
    }
    view.rerender(<AgentUsageWidget settings={{ m: "turns", p: "30d" }} noQuota fixedFilter={{ project: ["pinned"] }} st={st} />);
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
    const edited = new URL(String(fetcher.mock.calls[2]![0]), "http://localhost");
    expect(edited.searchParams.get("metric")).toBe("turns");
    expect(edited.searchParams.get("period")).toBe("30d");
    expect(edited.searchParams.get("model")).toBeNull();
  });

  it("reads and filters the workspace breakdown through the signed-in user's endpoint", async () => {
    const data = usageFixture();
    data.query = { ...data.query, metric: "cost", group_by: "workspace" };
    data.rows = [
      { key: "ws-a", label: "Alpha", value: 100, turns: 1, turns_unknown: 0, link: { kind: "workspace", id: "ws-a" } },
      { key: "ws-b", label: "Beta", value: 200, turns: 1, turns_unknown: 0, link: { kind: "workspace", id: "ws-b" } },
    ];
    data.series = [{ key: "ws-a", label: "Alpha", values: [100] }, { key: "ws-b", label: "Beta", values: [200] }];
    data.totals = { value: 300, turns: 2, turns_unknown: 0, tokens_in: 250, tokens_out: 50 };
    data.capabilities = { ...data.capabilities, group_by: [...data.capabilities.group_by, "workspace"], quota: false };
    // mock: external boundary — browser HTTP; the real hook chooses and caches the route.
    const fetcher = vi.fn(async (_url: RequestInfo | URL) => new Response(JSON.stringify(data)));
    vi.stubGlobal("fetch", fetcher);
    render(<AgentUsageWidget settings={{ m: "cost", g: "workspace" }} st={st} />);
    await screen.findByTestId("usage-table");
    expect(screen.getAllByText("Alpha").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Beta").length).toBeGreaterThan(0);
    expect(fetcher.mock.calls.every(([url]) => new URL(String(url), "http://localhost").pathname === "/api/v1/me/analytics/usage")).toBe(true);
    const before = fetcher.mock.calls.length;
    let url = new URL(String(fetcher.mock.calls[before - 1]![0]), "http://localhost");
    expect(url.searchParams.get("group_by")).toBe("workspace");
    expect(url.searchParams.get("metric")).toBe("cost");
    // The table sorts by value, so Beta leads; filter on Alpha's own row.
    fireEvent.click(within(document.querySelector('tr[data-row="ws-a"]') as HTMLElement).getByRole("button", { name: "Only this" }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(before + 1));
    url = new URL(String(fetcher.mock.calls[before]![0]), "http://localhost");
    expect(url.pathname).toBe("/api/v1/me/analytics/usage");
    expect(url.searchParams.get("workspace_id")).toBe("ws-a");
  });
});
