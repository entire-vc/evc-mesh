import { describe, it, expect, beforeEach, vi } from "vitest";

// Each test needs a fresh module instance because loadRuntimeFlags() caches
// its result in a module-scope variable — importing fresh is the only way
// to re-trigger the fetch.
async function freshModule() {
  vi.resetModules();
  return import("../runtime-flags");
}

describe("runtime-flags — the perf·Б1 prefetch kill switch", () => {
  beforeEach(() => {
    delete (window as unknown as { __MESH_FLAGS__?: unknown }).__MESH_FLAGS__;
    vi.restoreAllMocks();
  });

  it("defaults prefetchNextRoute to true when /config.json 404s", async () => {
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(null, { status: 404 }),
    );
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(true);
  });

  it("defaults prefetchNextRoute to true when the fetch throws (offline)", async () => {
    vi.spyOn(window, "fetch").mockRejectedValue(new TypeError("network error"));
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(true);
  });

  it("honors /config.json disabling the flag", async () => {
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ prefetchNextRoute: false }), {
        status: 200,
      }),
    );
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(false);
  });

  it("a pre-set window.__MESH_FLAGS__ wins over /config.json", async () => {
    window.__MESH_FLAGS__ = { prefetchNextRoute: false };
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ prefetchNextRoute: true }), {
        status: 200,
      }),
    );
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(false);
  });

  it("treats the string \"false\" in /config.json as disabled (fail-open typo guard)", async () => {
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ prefetchNextRoute: "false" }), {
        status: 200,
      }),
    );
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(false);
  });

  it.each(["0", "off", "FALSE", " False "])(
    "treats config value %j as disabled",
    async (raw) => {
      vi.spyOn(window, "fetch").mockResolvedValue(
        new Response(JSON.stringify({ prefetchNextRoute: raw }), {
          status: 200,
        }),
      );
      const { loadRuntimeFlags } = await freshModule();
      const flags = await loadRuntimeFlags();
      expect(flags.prefetchNextRoute).toBe(false);
    },
  );

  it.each(["true", "1", "on", "ON"])(
    "treats config value %j as enabled",
    async (raw) => {
      vi.spyOn(window, "fetch").mockResolvedValue(
        new Response(JSON.stringify({ prefetchNextRoute: raw }), {
          status: 200,
        }),
      );
      const { loadRuntimeFlags } = await freshModule();
      const flags = await loadRuntimeFlags();
      expect(flags.prefetchNextRoute).toBe(true);
    },
  );

  it("warns and falls back to the default for an unrecognized value", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(window, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ prefetchNextRoute: "yes-please" }), {
        status: 200,
      }),
    );
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(true);
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining("prefetchNextRoute"),
    );
  });

  it("normalizes a string value on window.__MESH_FLAGS__ the same way as /config.json", async () => {
    window.__MESH_FLAGS__ = { prefetchNextRoute: "false" as unknown as boolean };
    vi.spyOn(window, "fetch").mockResolvedValue(new Response(null, { status: 404 }));
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(false);
  });

  it("warns and falls back to the default for an unrecognized window.__MESH_FLAGS__ value", async () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    window.__MESH_FLAGS__ = { prefetchNextRoute: "yes-please" as unknown as boolean };
    vi.spyOn(window, "fetch").mockResolvedValue(new Response(null, { status: 404 }));
    const { loadRuntimeFlags } = await freshModule();
    const flags = await loadRuntimeFlags();
    expect(flags.prefetchNextRoute).toBe(true);
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining("prefetchNextRoute"),
    );
  });

  it("only fetches once across repeated calls", async () => {
    const fetchSpy = vi
      .spyOn(window, "fetch")
      .mockResolvedValue(new Response(null, { status: 404 }));
    const { loadRuntimeFlags } = await freshModule();
    await loadRuntimeFlags();
    await loadRuntimeFlags();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });
});
