import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

// The warm-up is the whole point of the module and its kill switch is what
// makes it safe to ship, so both are pinned here: with the flag on the
// dashboard and board chunks are imported exactly once however many callers
// ask; with `prefetchNextRoute: false` (from /config.json or
// window.__MESH_FLAGS__) nothing is imported at all.

const imported = vi.hoisted(() => ({ dashboard: 0, board: 0 }));

// doMock (not mock) after every resetModules: vitest keeps a static mock's
// factory result across resets, so the counter would only ever see the first
// test's import.
async function freshModule() {
  vi.resetModules();
  vi.doMock("@/pages/dashboard", () => {
    imported.dashboard++;
    return { DashboardPage: () => null };
  });
  vi.doMock("@/pages/board", () => {
    imported.board++;
    return { BoardPage: () => null };
  });
  return import("../prefetch-next-route");
}

/** Lets the flag fetch, the idle callback and the dynamic imports all run. */
const flush = () => new Promise((r) => setTimeout(r, 20));

describe("prefetchNextRouteOnIdle", () => {
  beforeEach(() => {
    imported.dashboard = 0;
    imported.board = 0;
    delete (window as unknown as { __MESH_FLAGS__?: unknown }).__MESH_FLAGS__;
    vi.spyOn(window, "fetch").mockResolvedValue(new Response(null, { status: 404 }));
    // jsdom has no requestIdleCallback; run the callback as soon as asked.
    window.requestIdleCallback = ((cb: IdleRequestCallback) =>
      window.setTimeout(() => cb({ didTimeout: false, timeRemaining: () => 50 }), 0)) as typeof window.requestIdleCallback;
  });

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(navigator, "connection", { value: undefined, configurable: true });
  });

  it("warms the dashboard and board chunks when the flag is on (the default)", async () => {
    const { prefetchNextRouteOnIdle } = await freshModule();
    prefetchNextRouteOnIdle();
    await flush();
    expect(imported.dashboard).toBe(1);
    expect(imported.board).toBe(1);
  });

  it("does nothing when the kill switch is off", async () => {
    window.__MESH_FLAGS__ = { prefetchNextRoute: false };
    const { prefetchNextRouteOnIdle } = await freshModule();
    prefetchNextRouteOnIdle();
    await flush();
    expect(imported.dashboard).toBe(0);
    expect(imported.board).toBe(0);
  });

  it("does nothing when /config.json turns the kill switch off", async () => {
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ prefetchNextRoute: false }), { status: 200 }),
    );
    const { prefetchNextRouteOnIdle } = await freshModule();
    prefetchNextRouteOnIdle();
    await flush();
    expect(imported.dashboard).toBe(0);
    expect(imported.board).toBe(0);
  });

  it("imports once per page load however many callers ask (login submit + AppLayout)", async () => {
    const { prefetchNextRouteOnIdle } = await freshModule();
    prefetchNextRouteOnIdle();
    prefetchNextRouteOnIdle();
    await flush();
    prefetchNextRouteOnIdle();
    await flush();
    expect(imported.dashboard).toBe(1);
    expect(imported.board).toBe(1);
  });

  it("does nothing on a data-saver connection", async () => {
    Object.defineProperty(navigator, "connection", {
      value: { saveData: true },
      configurable: true,
    });
    const { prefetchNextRouteOnIdle } = await freshModule();
    prefetchNextRouteOnIdle();
    await flush();
    expect(imported.dashboard).toBe(0);
    expect(imported.board).toBe(0);
  });
});
