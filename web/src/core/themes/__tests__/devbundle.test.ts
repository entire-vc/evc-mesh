import { afterEach, describe, expect, it, vi } from "vitest";
import { DEV_BUNDLE_KEY, devBundleUrl, watchDevBundle } from "../bundle";

afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("dev bundle (mesh-theme dev)", () => {
  it("is off unless the author set the flag", () => {
    expect(devBundleUrl()).toBeNull();
  });

  it("accepts only a local dev server", () => {
    localStorage.setItem(DEV_BUNDLE_KEY, "http://127.0.0.1:4173/views.js");
    expect(devBundleUrl()).toBe("http://127.0.0.1:4173/views.js");
    for (const bad of ["https://evil.example/views.js", "http://127.0.0.1.evil.example/views.js", "javascript:alert(1)", "http://localhost@evil.example/views.js"]) {
      localStorage.setItem(DEV_BUNDLE_KEY, bad);
      expect(devBundleUrl(), bad).toBeNull();
    }
  });

  it("is never on in a shipped (non-DEV) build, whatever localStorage says", () => {
    vi.stubEnv("DEV", false);
    localStorage.setItem(DEV_BUNDLE_KEY, "http://127.0.0.1:4173/views.js");
    expect(devBundleUrl()).toBeNull();
  });

  it("reloads on a rebuild message, not on the greeting", () => {
    const es: { onmessage?: () => void; close: () => void; url?: string } = { close: vi.fn() };
    vi.stubGlobal("EventSource", function (this: unknown, url: string) { es.url = url; return es; });
    const onChange = vi.fn();
    const stop = watchDevBundle("http://127.0.0.1:4173/views.js", onChange);
    expect(es.url).toBe("http://127.0.0.1:4173/events");
    es.onmessage!();
    expect(onChange).not.toHaveBeenCalled();
    es.onmessage!();
    expect(onChange).toHaveBeenCalledTimes(1);
    stop();
    expect(es.close).toHaveBeenCalled();
  });
});
