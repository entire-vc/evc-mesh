import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Covers the localStorage -> memory + httpOnly cookie migration (Mesh task
// 0ade477e): the access token must never touch localStorage/sessionStorage,
// legacy tokens from the old scheme must be wiped on load, and refresh must
// coordinate across tabs via the Web Locks API rather than racing the
// one-shot refresh-token rotation on the server.

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

beforeEach(() => {
  localStorage.clear();
  vi.resetModules();
});
afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("legacy localStorage token migration", () => {
  it("wipes pre-existing access_token/refresh_token on module load", async () => {
    localStorage.setItem("access_token", "old-leaked-access-token");
    localStorage.setItem("refresh_token", "old-leaked-refresh-token");

    await import("@/lib/api");

    expect(localStorage.getItem("access_token")).toBeNull();
    expect(localStorage.getItem("refresh_token")).toBeNull();
  });

  it("is a no-op when nothing legacy is present", async () => {
    await import("@/lib/api");
    expect(localStorage.getItem("access_token")).toBeNull();
    expect(localStorage.getItem("refresh_token")).toBeNull();
  });
});

describe("access token storage", () => {
  it("setAccessToken/getAccessToken never touch localStorage", async () => {
    const { setAccessToken, getAccessToken, clearAccessToken } = await import("@/lib/api");

    setAccessToken("mem-only-token");
    expect(getAccessToken()).toBe("mem-only-token");
    expect(localStorage.getItem("access_token")).toBeNull();
    expect(localStorage.getItem("refresh_token")).toBeNull();

    clearAccessToken();
    expect(getAccessToken()).toBeNull();
  });
});

describe("api() credentials handling", () => {
  it("sends credentials 'omit' for a plain noAuth request", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ ok: true })));
    const { api } = await import("@/lib/api");

    await api("/api/v1/auth/config", { noAuth: true });

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    expect(fetchMock.mock.calls[0]?.[1]?.credentials).toBe("omit");
  });

  it("sends credentials 'include' when withCredentials is set on a noAuth request", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ ok: true })));
    const { api } = await import("@/lib/api");

    await api("/api/v1/auth/login", {
      method: "POST",
      body: { email: "a@b.com", password: "x" },
      noAuth: true,
      withCredentials: true,
    });

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    expect(fetchMock.mock.calls[0]?.[1]?.credentials).toBe("include");
  });

  it("sends credentials 'same-origin' for an authenticated request", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ ok: true })));
    const { api } = await import("@/lib/api");

    await api("/api/v1/tasks");

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    expect(fetchMock.mock.calls[0]?.[1]?.credentials).toBe("same-origin");
  });
});

describe("refresh coordination across tabs", () => {
  it("routes the refresh call through navigator.locks.request when available", async () => {
    const lockRequest = vi.fn((_name: string, cb: () => Promise<unknown>) => cb());
    vi.stubGlobal("navigator", { locks: { request: lockRequest } });
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({ tokens: { access_token: "new-access", expires_in: 900 } }),
      ),
    );

    const { bootstrapSession } = await import("@/lib/api");
    const token = await bootstrapSession();

    expect(token).toBe("new-access");
    expect(lockRequest).toHaveBeenCalledTimes(1);
    expect(lockRequest.mock.calls[0]?.[0]).toBe("mesh-auth-refresh");
  });

  it("falls back to an unserialized call when navigator.locks is unavailable", async () => {
    vi.stubGlobal("navigator", {});
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({ tokens: { access_token: "fallback-access", expires_in: 900 } }),
      ),
    );

    const { bootstrapSession } = await import("@/lib/api");
    const token = await bootstrapSession();

    expect(token).toBe("fallback-access");
  });

  it("a failed refresh clears the in-memory access token", async () => {
    vi.stubGlobal("navigator", {});
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({}, 401)));

    const { bootstrapSession, setAccessToken, getAccessToken } = await import("@/lib/api");
    setAccessToken("stale-token");

    await expect(bootstrapSession()).rejects.toThrow();
    expect(getAccessToken()).toBeNull();
  });

  it("refresh requests are sent with credentials 'include' and no body", async () => {
    vi.stubGlobal("navigator", {});
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({ tokens: { access_token: "x", expires_in: 900 } }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { bootstrapSession } = await import("@/lib/api");
    await bootstrapSession();

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/api/v1/auth/refresh");
    expect(init.credentials).toBe("include");
    expect(init.body).toBeUndefined();
  });
});
