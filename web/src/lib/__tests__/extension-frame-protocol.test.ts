import { describe, expect, it } from "vitest";
import {
  MAX_SECTION_LENGTH,
  MIN_REFRESH_DELAY_MS,
  contextErrorMessage,
  contextMessage,
  frameSrc,
  isRevocation,
  isSafeAppPath,
  normalizeSection,
  parseFrameMessage,
  refreshDelayMs,
  revokedMessage,
} from "../extension-frame-protocol";

const FRAME = {};
const ORIGIN = "https://hello.example.com";
const expected = { origin: ORIGIN, frameWindow: FRAME };
const msg = (data: unknown, over: Partial<{ origin: string; source: unknown }> = {}) => ({
  origin: ORIGIN,
  source: FRAME,
  data,
  ...over,
});

describe("parseFrameMessage", () => {
  it("accepts the four commands from the frame's own origin", () => {
    expect(parseFrameMessage(msg({ type: "mesh.ready" }), expected)).toEqual({ type: "ready" });
    expect(parseFrameMessage(msg({ type: "mesh.resize", height: 480.4 }), expected)).toEqual({ type: "resize", height: 480 });
    expect(parseFrameMessage(msg({ type: "mesh.navigate", path: "/w/acme/dashboard" }), expected)).toEqual({ type: "navigate", path: "/w/acme/dashboard" });
    expect(parseFrameMessage(msg({ type: "mesh.toast", level: "success", message: " Saved " }), expected)).toEqual({ type: "toast", level: "success", message: "Saved" });
  });

  it("red control: a foreign origin cannot navigate", () => {
    const evil = msg({ type: "mesh.navigate", path: "/w/acme/dashboard" }, { origin: "https://evil.example" });
    expect(parseFrameMessage(evil, expected)).toBeNull();
  });

  it("red control: right origin but not the iframe's window is dropped", () => {
    expect(parseFrameMessage(msg({ type: "mesh.toast", message: "x" }, { source: {} }), expected)).toBeNull();
    expect(parseFrameMessage(msg({ type: "mesh.toast", message: "x" }), { origin: ORIGIN, frameWindow: null })).toBeNull();
  });

  it("drops unknown types, non-objects and bad payloads", () => {
    for (const data of [null, "mesh.ready", 7, { type: "mesh.eval" }, { type: "mesh.toast" }, { type: "mesh.toast", message: "   " }, { type: "mesh.resize", height: "tall" }]) {
      expect(parseFrameMessage(msg(data), expected)).toBeNull();
    }
  });

  it("clamps the height and the toast text, defaults the level", () => {
    expect(parseFrameMessage(msg({ type: "mesh.resize", height: 1e9 }), expected)).toEqual({ type: "resize", height: 4000 });
    expect(parseFrameMessage(msg({ type: "mesh.resize", height: -5 }), expected)).toEqual({ type: "resize", height: 120 });
    const t = parseFrameMessage(msg({ type: "mesh.toast", level: "bogus", message: "a".repeat(1000) }), expected);
    expect(t).toMatchObject({ type: "toast", level: "info" });
    expect(t && t.type === "toast" && t.message.length).toBe(300);
  });
});

describe("isSafeAppPath", () => {
  it("allows in-app paths only", () => {
    expect(isSafeAppPath("/w/acme/p/x/t/1?tab=comments#c")).toBe(true);
    for (const bad of ["", "w/acme", "//evil.example", "https://evil.example", "/\\evil.example", "javascript:alert(1)", "/a\nb", "/\\/evil.example"]) {
      expect(isSafeAppPath(bad), bad).toBe(false);
    }
  });
});

describe("context and src", () => {
  it("puts the token on the address and in mesh.context", () => {
    expect(frameSrc("https://svc.example/mesh/ui/hello", "a.b.c")).toBe("https://svc.example/mesh/ui/hello?token=a.b.c");
    expect(contextMessage({ token: "a.b.c", expiresAt: "t", workspaceId: "w", workspaceSlug: "s", locale: "ru" })).toEqual({
      type: "mesh.context", version: 1, token: "a.b.c", expires_at: "t", workspace_id: "w", workspace_slug: "s", project_id: null, task_id: null, locale: "ru", path: "",
    });
  });
});

describe("sections (deep links into a screen)", () => {
  it("keeps a sub-path and a hash, drops a trailing slash", () => {
    expect(normalizeSection("/memory/search#recent")).toBe("/memory/search#recent");
    expect(normalizeSection("/memory/")).toBe("/memory");
    expect(normalizeSection("/models/gpt%20x")).toBe("/models/gpt%20x");
    expect(normalizeSection("#only-hash")).toBe("#only-hash");
    expect(normalizeSection("")).toBe("");
    expect(normalizeSection("/")).toBe("");
    expect(normalizeSection("/a#")).toBe("/a");
    expect(normalizeSection("/a#sec/ti%20on")).toBe("/a#sec/ti%20on");
  });

  it("red control: refuses anything that could leave the screen's path or carry a query", () => {
    for (const bad of [
      "memory",
      "//evil.example/x",
      "//",
      "/a//",
      "//#x",
      "/a//b",
      "/a/../b",
      "/..",
      "/a/%2e%2E/b",
      "/a/.%2e",
      "/..%2f..",
      "/a%2F..%2Fb",
      "/a%5c..",
      "/section%ZZ",
      "/a%2",
      "/a%",
      "/a/./b",
      "/a\\b",
      "/a?token=x",
      "/a b",
      "/a\u0000",
      "/a#x y",
      "#%ZZ",
      "#%2F",
      "/a#%5C",
      "/page#x%2fy",
      "/a#%2",
      "/a#%",
      "#?x=y",
      "/a#x?y",
      "/" + "a".repeat(MAX_SECTION_LENGTH),
      42,
      null,
    ]) {
      expect(normalizeSection(bad), String(bad)).toBeNull();
    }
  });

  it("opens the frame at the section, on the screen's own origin, token last in the query", () => {
    expect(frameSrc(`${ORIGIN}/mesh/ui/mem`, "t.k.n", "/memory/search#recent")).toBe(`${ORIGIN}/mesh/ui/mem/memory/search?token=t.k.n#recent`);
    expect(frameSrc(`${ORIGIN}/mesh/ui/mem/`, "t.k.n", "/x")).toBe(`${ORIGIN}/mesh/ui/mem/x?token=t.k.n`);
    expect(frameSrc(`${ORIGIN}/mesh/ui/mem`, "t.k.n")).toBe(`${ORIGIN}/mesh/ui/mem?token=t.k.n`);
    // A section that is not one is ignored rather than spliced in.
    expect(new URL(frameSrc(`${ORIGIN}/mesh/ui/mem`, "t", "//evil.example/x")).origin).toBe(ORIGIN);
    expect(frameSrc(`${ORIGIN}/mesh/ui/mem`, "t", "/a/../../b")).toBe(`${ORIGIN}/mesh/ui/mem?token=t`);
  });

  it("parses mesh.state into a section, push by default", () => {
    expect(parseFrameMessage(msg({ type: "mesh.state", path: "/memory#q" }), expected)).toEqual({ type: "state", section: "/memory#q", replace: false });
    expect(parseFrameMessage(msg({ type: "mesh.state", path: "/", replace: true }), expected)).toEqual({ type: "state", section: "", replace: true });
    expect(parseFrameMessage(msg({ type: "mesh.state", path: "/../admin" }), expected)).toBeNull();
    expect(parseFrameMessage(msg({ type: "mesh.state", path: "//" }), expected)).toBeNull();
    expect(parseFrameMessage(msg({ type: "mesh.state", path: "/memory" }, { origin: "https://evil.example" }), expected)).toBeNull();
  });

  it("tells the screen its section in mesh.context", () => {
    const base = { token: "t", expiresAt: "x", workspaceId: "w", workspaceSlug: "s", locale: "en" };
    expect(contextMessage({ ...base, section: "/memory#q" }).path).toBe("/memory#q");
    expect(contextMessage(base).path).toBe("");
  });
});

describe("lifecycle messages", () => {
  it("parses mesh.app-ready and mesh.app-error, capping and trimming the error text", () => {
    expect(parseFrameMessage(msg({ type: "mesh.app-ready" }), expected)).toEqual({ type: "app-ready" });
    expect(parseFrameMessage(msg({ type: "mesh.app-error", message: " Backend down " }), expected)).toEqual({ type: "app-error", message: "Backend down" });
    expect(parseFrameMessage(msg({ type: "mesh.app-error" }), expected)).toEqual({ type: "app-error", message: "" });
    const long = parseFrameMessage(msg({ type: "mesh.app-error", message: "x".repeat(1000) }), expected);
    expect(long).toEqual({ type: "app-error", message: "x".repeat(300) });
    expect(parseFrameMessage(msg({ type: "mesh.app-error", message: "x" }, { origin: "https://evil.example" }), expected)).toBeNull();
  });

  it("revokes on 403/404/409 only; 401, 5xx and network are transient", () => {
    expect([403, 404, 409].every(isRevocation)).toBe(true);
    expect([0, 401, 500, 503].some(isRevocation)).toBe(false);
    expect(contextErrorMessage(409)).toEqual({ type: "mesh.context.error", version: 1, status: 409, reason: "conflict" });
    expect(contextErrorMessage(0)).toEqual({ type: "mesh.context.error", version: 1, status: null, reason: "network" });
    expect(contextErrorMessage(401).reason).toBe("unauthorized");
    expect(contextErrorMessage(503).reason).toBe("unavailable");
    expect(revokedMessage(403)).toEqual({ type: "mesh.revoked", version: 1, reason: "forbidden" });
  });

  it("renews at 80% of the token lifetime, never sooner than the floor", () => {
    const now = Date.parse("2026-10-17T12:00:00Z");
    expect(refreshDelayMs("2026-10-17T12:01:00Z", now)).toBe(48_000);
    expect(refreshDelayMs("2026-10-17T11:59:00Z", now)).toBe(MIN_REFRESH_DELAY_MS);
    expect(refreshDelayMs("not a date", now)).toBe(MIN_REFRESH_DELAY_MS);
  });
});
