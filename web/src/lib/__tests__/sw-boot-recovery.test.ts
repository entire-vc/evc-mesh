// @vitest-environment node
// Cold-reopen recovery (card boot recovery): the service worker must never cache or
// replay an error response, and must retry a failed asset once past the HTTP
// cache; the boot guard in index.html must be bounded and must not touch storage.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import vm from "node:vm";
import { describe, expect, it, vi } from "vitest";

const root = resolve(__dirname, "../../..");

type Handler = (e: unknown) => void;

function loadSw(fetchImpl: (...a: unknown[]) => Promise<unknown>) {
  const handlers: Record<string, Handler> = {};
  const store = new Map<string, unknown>();
  const cache = {
    put: vi.fn(async (req: { url: string }, res: unknown) => void store.set(req.url, res)),
    addAll: vi.fn(async () => {}),
  };
  const caches = {
    open: async () => cache,
    keys: async () => [],
    match: async (req: { url: string }) => store.get(req.url),
  };
  class FakeResponse {
    static error() { return new FakeResponse(0, "error"); }
    constructor(public status: number, public type = "basic") {}
    get ok() { return this.status >= 200 && this.status < 300; }
    clone() { return this; }
  }
  const self = {
    location: { origin: "https://mesh.test" },
    addEventListener: (n: string, h: Handler) => { handlers[n] = h; },
    skipWaiting: () => {},
    clients: { claim: () => {} },
  };
  const code = readFileSync(resolve(root, "public/sw.js"), "utf8").replace("__BUILD_HASH__", "mesh-test");
  vm.runInNewContext(code, { self, caches, fetch: fetchImpl, Response: FakeResponse, URL, Promise });
  async function dispatch(url: string, mode = "cors") {
    let out: Promise<unknown> | undefined;
    const waits: Promise<unknown>[] = [];
    handlers.fetch!({
      request: { method: "GET", url, mode },
      respondWith: (p: Promise<unknown>) => { out = p; },
      waitUntil: (p: Promise<unknown>) => { waits.push(p); },
    });
    const res = await out;
    await Promise.all(waits);
    return res as FakeResponse;
  }
  return { dispatch, cache, store, FakeResponse };
}

describe("service worker", () => {
  it("does not cache a 404 and does not replay it offline", async () => {
    const fetchMock = vi.fn(async () => new (class { status = 404; type = "basic"; ok = false; clone() { return this; } })());
    const sw = loadSw(fetchMock);
    const res = await sw.dispatch("https://mesh.test/assets/missing.js");
    expect(res.status).toBe(404);
    expect(sw.cache.put).not.toHaveBeenCalled();
    // network down now: nothing stored, so a network error — not a cached 404
    const off = loadSw(async () => { throw new TypeError("offline"); });
    const r = await off.dispatch("https://mesh.test/assets/missing.js");
    expect(r.type).toBe("error");
  });

  it("retries a failed asset once with cache:'reload' and serves the fresh copy", async () => {
    const calls: unknown[][] = [];
    const ok = { status: 200, type: "basic", ok: true, clone() { return this; } };
    const bad = { status: 404, type: "basic", ok: false, clone() { return this; } };
    const sw = loadSw(async (...a) => { calls.push(a); return calls.length === 1 ? bad : ok; });
    const res = await sw.dispatch("https://mesh.test/assets/app-abc.js");
    expect(res.status).toBe(200);
    expect(calls).toHaveLength(2);
    expect((calls[1]![1] as { cache: string }).cache).toBe("reload");
    expect(sw.cache.put).toHaveBeenCalledTimes(1);
  });

  it("a rejected asset fetch is also retried once with cache:'reload'", async () => {
    const calls: unknown[][] = [];
    const ok = { status: 200, type: "basic", ok: true, clone() { return this; } };
    const sw = loadSw(async (...a) => { calls.push(a); if (calls.length === 1) throw new TypeError("net"); return ok; });
    const res = await sw.dispatch("https://mesh.test/assets/app-abc.js");
    expect(res.status).toBe(200);
    expect(calls).toHaveLength(2);
    expect((calls[1]![1] as { cache: string }).cache).toBe("reload");
  });

  it("does not retry cross-origin or non-asset failures (bounded)", async () => {
    const fetchMock = vi.fn(async () => ({ status: 500, type: "basic", ok: false, clone() { return this; } }));
    const sw = loadSw(fetchMock);
    await sw.dispatch("https://mesh.test/some/data.json");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("serves a stored success when offline", async () => {
    let up = true;
    const ok = { status: 200, type: "basic", ok: true, clone() { return this; } };
    const sw = loadSw(async () => { if (!up) throw new TypeError("offline"); return ok; });
    await sw.dispatch("https://mesh.test/");
    up = false;
    const res = await sw.dispatch("https://mesh.test/");
    expect(res.status).toBe(200);
  });
});

describe("boot guard (index.html)", () => {
  const html = readFileSync(resolve(root, "index.html"), "utf8");
  const guard = /<script id="boot-guard">([\s\S]*?)<\/script>/.exec(html)![1]!;

  function run(opts: { retried?: boolean; fetchOk?: boolean } = {}) {
    const store: Record<string, string> = opts.retried ? { "mesh-boot-retry": "1" } : {};
    const reload = vi.fn();
    const listeners: Record<string, (e: unknown) => void> = {};
    const rootEl = { textContent: "x", removeAttribute: vi.fn(), appendChild: vi.fn() };
    const created: { dataset: Record<string, string>; text?: string }[] = [];
    const win: Record<string, unknown> = {
      addEventListener: (n: string, h: (e: unknown) => void) => { listeners[n] = h; },
      fetch: vi.fn(async () => ({ status: opts.fetchOk === false ? 404 : 200 })),
    };
    const ctx = {
      window: win, location: { href: "https://mesh.test/", origin: "https://mesh.test", pathname: "/", reload },
      sessionStorage: { getItem: (k: string) => store[k] ?? null, setItem: (k: string, v: string) => { store[k] = v; }, removeItem: (k: string) => { delete store[k]; } },
      localStorage: { clear: () => { throw new Error("must not touch localStorage"); } },
      document: {
        getElementById: () => rootEl,
        createElement: () => { const el = { style: {}, setAttribute() {}, appendChild() {}, set textContent(v: string) { created.push({ dataset: {}, text: v }); } }; return el; },
      },
      fetch: win.fetch, URL, setTimeout: (fn: () => void, ms: number) => { if (ms === 3000) fn(); return 0; },
    };
    (win as { window?: unknown }).window = win;
    vm.runInNewContext(guard!, ctx);
    return { win, listeners, reload, store, created, rootEl, ctx };
  }

  it("first asset failure: one cache-bypassing refetch and one reload", async () => {
    const g = run();
    g.listeners.error!({ target: { tagName: "SCRIPT", src: "https://mesh.test/assets/index-1.js" } });
    await new Promise((r) => setTimeout(r, 0));
    expect((g.win.fetch as ReturnType<typeof vi.fn>).mock.calls[0]![1]).toMatchObject({ cache: "reload" });
    expect(g.reload).toHaveBeenCalledTimes(1);
    expect(g.store["mesh-boot-retry"]).toBe("1");
  });

  it("storage that cannot persist the flag: diagnostics, no reload", async () => {
    const g = run();
    (g.ctx.sessionStorage as { setItem: (k: string, v: string) => void }).setItem = () => { throw new Error("denied"); };
    g.listeners.error!({ target: { tagName: "SCRIPT", src: "https://mesh.test/assets/index-1.js" } });
    await new Promise((r) => setTimeout(r, 0));
    expect(g.reload).not.toHaveBeenCalled();
  });

  it("second failure in the same session shows diagnostics and does not reload", async () => {
    const g = run({ retried: true });
    g.listeners.error!({ target: { tagName: "SCRIPT", src: "https://mesh.test/assets/index-1.js?token=secret" } });
    await new Promise((r) => setTimeout(r, 0));
    expect(g.reload).not.toHaveBeenCalled();
    const texts = g.created.map((c) => c.text).join("|");
    expect(texts).toContain("asset-load-failed /assets/index-1.js HTTP 200");
    expect(texts).not.toContain("secret");
  });

  it("booting clears the retry flag; preload error reloads once per minute", () => {
    const g = run({ retried: true });
    g.win["__meshBooted"] = true;
    expect(g.store["mesh-boot-retry"]).toBeUndefined();
    const ev = { preventDefault: vi.fn() };
    g.listeners["vite:preloadError"]!(ev);
    g.listeners["vite:preloadError"]!(ev);
    expect(g.reload).toHaveBeenCalledTimes(1);
  });

  it("ignores non-asset errors and a successful boot", () => {
    const g = run();
    g.listeners.error!({ target: { tagName: "IMG", src: "https://mesh.test/a.png" } });
    g.listeners.error!({ target: { tagName: "SCRIPT", src: "https://other.test/x.js" } });
    g.win['__meshBooted'] = true;
    (g.win["__meshBootFail"] as (s: string) => void)("boot-timeout");
    expect(g.reload).not.toHaveBeenCalled();
  });
});
