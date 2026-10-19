import { describe, expect, it } from "vitest";
import { contextMessage, frameSrc, isSafeAppPath, parseFrameMessage } from "../extension-frame-protocol";

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
      type: "mesh.context", version: 1, token: "a.b.c", expires_at: "t", workspace_id: "w", workspace_slug: "s", project_id: null, task_id: null, locale: "ru",
    });
  });
});
