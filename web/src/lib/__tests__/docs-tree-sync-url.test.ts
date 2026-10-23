import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadServerExpanded, saveServerExpanded } from "@/lib/docs-tree-sync";

// Regression: the sync module called /me/ui-state/... without the /api/v1
// prefix. In production that hit the SPA fallback (GET 200 text/html, PUT 405), so
// the server copy never worked. The component test mocks this module, so the
// real URL is asserted here against the true boundary (fetch).

const PROJECT = "e93b8e1a-ee44-4399-8e8a-0bdda460b4a0";
const EXPECTED = `/api/v1/me/ui-state/docs.tree%3A${PROJECT}`;

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ state: { expanded: ["a", 7, "b"] } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
});
afterEach(() => vi.unstubAllGlobals());

const calls = () => (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls;

describe("docs-tree-sync URLs", () => {
  it("GETs the /api/v1 ui-state route", async () => {
    const res = await loadServerExpanded(PROJECT);
    expect(String(calls()[0]?.[0])).toBe(EXPECTED);
    expect(res).toEqual({ kind: "ok", expanded: ["a", "b"] });
  });

  it("PUTs the /api/v1 ui-state route", async () => {
    await saveServerExpanded(PROJECT, ["a"]);
    expect(String(calls()[0]?.[0])).toBe(EXPECTED);
    expect(calls()[0]?.[1].method).toBe("PUT");
  });
});
