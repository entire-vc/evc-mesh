import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", () => ({ api: apiMock }));

import { useDashboardsStore } from "../dashboards";
import type { Dashboard } from "@/lib/dashboards";

const d = (id: string, name: string): Dashboard => ({
  id, name, icon: "", placement: "top", position: 0, is_default: false, virtual: false, shared: true, hidden: false, can_edit: true, can_manage: true,
});

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((r, j) => { resolve = r; reject = j; });
  return { promise, resolve, reject };
}

const paths = () => apiMock.mock.calls.map((c) => String(c[0]));

beforeEach(() => {
  apiMock.mockReset();
  useDashboardsStore.setState({ workspaceId: "w1", items: [d("a", "Old")], status: "ready", error: null });
});

describe("dashboards store: overlapping refreshes", () => {
  it("an older refresh answering last does not overwrite the newer list", async () => {
    const first = deferred<{ dashboards: Dashboard[] }>();
    const second = deferred<{ dashboards: Dashboard[] }>();
    apiMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const s = useDashboardsStore.getState();
    const p1 = s.load("w1", { silent: true });
    const p2 = s.load("w1", { silent: true });
    second.resolve({ dashboards: [d("a", "Renamed")] });
    await p2;
    first.resolve({ dashboards: [d("a", "Old"), d("gone", "Deleted since")] });
    await p1;
    expect(useDashboardsStore.getState().items.map((x) => `${x.id}:${x.name}`)).toEqual(["a:Renamed"]);
  });

  it("a refresh started after a reorder wins over the reorder answer arriving later", async () => {
    const put = deferred<{ dashboards: Dashboard[] }>();
    apiMock.mockReturnValueOnce(put.promise).mockResolvedValueOnce({ dashboards: [d("a", "Fresh")] });
    const s = useDashboardsStore.getState();
    const r = s.reorder({ top: ["a"], bottom: [] });
    await s.load("w1", { silent: true });
    put.resolve({ dashboards: [d("a", "Stale")] });
    await r;
    expect(useDashboardsStore.getState().items.map((x) => x.name)).toEqual(["Fresh"]);
  });
});

describe("dashboards store: a refused reorder after the menu moved on", () => {
  it("another workspace opened meanwhile: its menu stays, the old workspace is not reloaded", async () => {
    const put = deferred<{ dashboards: Dashboard[] }>();
    apiMock.mockReturnValueOnce(put.promise).mockResolvedValueOnce({ dashboards: [d("w2-dash", "W2")] });
    const s = useDashboardsStore.getState();
    const r = s.reorder({ top: ["a"], bottom: [] });
    await s.load("w2");
    put.reject(new Error("422"));
    await expect(r).rejects.toThrow("422");
    await Promise.resolve();
    const st = useDashboardsStore.getState();
    expect([st.workspaceId, st.items.map((x) => x.id)]).toEqual(["w2", ["w2-dash"]]);
    expect(paths().filter((p) => p.includes("/workspaces/w1/dashboards") && !p.endsWith("/order"))).toEqual([]);
  });

  it("the store cleared meanwhile (sign-out): it stays empty, nothing is reloaded", async () => {
    const put = deferred<{ dashboards: Dashboard[] }>();
    apiMock.mockReturnValueOnce(put.promise);
    const s = useDashboardsStore.getState();
    const r = s.reorder({ top: ["a"], bottom: [] });
    s.clear();
    put.reject(new Error("422"));
    await expect(r).rejects.toThrow("422");
    await Promise.resolve();
    const st = useDashboardsStore.getState();
    expect([st.workspaceId, st.items, st.status]).toEqual([null, [], "idle"]);
    expect(apiMock).toHaveBeenCalledTimes(1);
  });

  it("control: refused while still current, the menu rolls back and reloads its own workspace", async () => {
    const put = deferred<{ dashboards: Dashboard[] }>();
    apiMock.mockReturnValueOnce(put.promise).mockResolvedValueOnce({ dashboards: [d("a", "Server")] });
    const r = useDashboardsStore.getState().reorder({ top: ["a"], bottom: [] });
    put.reject(new Error("422"));
    await expect(r).rejects.toThrow("422");
    await vi.waitFor(() => expect(useDashboardsStore.getState().items.map((x) => x.name)).toEqual(["Server"]));
    expect(paths()[1]).toContain("/workspaces/w1/dashboards");
  });
});
