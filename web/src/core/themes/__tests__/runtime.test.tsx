import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { Slot, registerSlotView, DEFAULT_THEME_ID } from "@/core/slots/registry";
import { registerThemeViews, loadViewsBundle, trustedThemeAsset } from "../bundle";
import { satisfies } from "../versions";
import { useThemeStore } from "../store";
import { applyWorkspaceTheme } from "../runtime";
import { THEME_CACHE_PREFIX } from "../cache";
import { DEV_BUNDLE_KEY, loadDevViewsBundle } from "../bundle";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
// `mesh-theme dev` loads the author's bundle over the network; the rest of the module stays real.
vi.mock("../bundle", async (orig) => ({ ...(await orig<typeof import("../bundle")>()), loadDevViewsBundle: vi.fn(async () => ({ views: {} })) }));
import { api } from "@/lib/api";

const ctx = {
  slot: "board.card", contractVersion: "1.0.0", locale: "en", mode: "light" as const,
  isMobile: false, workspace: null,
};
const props = { data: {}, actions: {}, themeSettings: {}, ctx };
const issues = () => (window as unknown as { __meshThemeIssues?: { code: string; slot?: string }[] }).__meshThemeIssues ?? [];

beforeEach(() => {
  (window as unknown as { __meshThemeIssues: unknown[] }).__meshThemeIssues = [];
  useThemeStore.getState().set({ viewsThemeId: DEFAULT_THEME_ID, settings: {} });
  registerSlotView("board.card", DEFAULT_THEME_ID, () => <div>default card</div>);
  document.head.innerHTML = "";
  localStorage.clear();
  vi.mocked(api).mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
  vi.spyOn(console, "error").mockImplementation(() => {});
});

describe("slot error boundary", () => {
  it("a theme view that throws is replaced by the default view; page stays alive and the crash is logged", () => {
    registerThemeViews("boom", {
      views: { "board.card": { contract: "^1", view: () => { throw new Error("kaboom"); } } },
    });
    render(<div><Slot name="board.card" themeId="boom" {...props} /><p>rest of page</p></div>);
    expect(screen.getByText("default card")).toBeTruthy();
    expect(screen.getByText("rest of page")).toBeTruthy();
    expect(issues().some((i) => i.code === "view-crashed" && i.slot === "board.card")).toBe(true);
  });

  it("after a crash, a different theme's view gets its own chance (the boundary resets)", () => {
    registerThemeViews("boom", {
      views: { "board.card": { contract: "^1", view: () => { throw new Error("kaboom"); } } },
    });
    registerThemeViews("ok", { views: { "board.card": { contract: "^1", view: () => <div>themed card</div> } } });
    const { rerender } = render(<Slot name="board.card" themeId="boom" {...props} />);
    expect(screen.getByText("default card")).toBeTruthy();
    rerender(<Slot name="board.card" themeId="ok" {...props} />);
    expect(screen.getByText("themed card")).toBeTruthy();
  });

  it("a newer bundle of the same theme that no longer satisfies a slot drops the old view", () => {
    registerThemeViews("evolving", { views: { "board.card": { contract: "^1", view: () => <div>old themed card</div> } } });
    registerThemeViews("evolving", { views: { "board.card": { contract: "^2", view: () => <div>new themed card</div> } } });
    render(<Slot name="board.card" themeId="evolving" {...props} />);
    expect(screen.getByText("default card")).toBeTruthy();
    expect(screen.queryByText("old themed card")).toBeNull();
  });

  it("a healthy theme view is drawn", () => {
    registerThemeViews("ok", { views: { "board.card": { contract: "^1", view: () => <div>themed card</div> } } });
    render(<Slot name="board.card" themeId="ok" {...props} />);
    expect(screen.getByText("themed card")).toBeTruthy();
  });
});

describe("contract versions", () => {
  it("board.card ^2 against core 1.x: not registered, default view works, warning logged", () => {
    const reg = registerThemeViews("future", {
      views: { "board.card": { contract: "^2", view: () => <div>v2 card</div> } },
    });
    expect(reg).toEqual([]);
    render(<Slot name="board.card" themeId="future" {...props} />);
    expect(screen.getByText("default card")).toBeTruthy();
    expect(screen.queryByText("v2 card")).toBeNull();
    expect(issues().some((i) => i.code === "contract-mismatch" && i.slot === "board.card")).toBe(true);
  });

  it("one bad slot does not take the theme's other slots down", () => {
    const reg = registerThemeViews("mixed", {
      views: {
        "board.card": { contract: "^2", view: () => null },
        "task.row": { contract: "^1", view: () => null },
      },
    });
    expect(reg).toEqual(["task.row"]);
  });

  it("satisfies: ^1 vs 1.0.0 yes, ^2 no, junk no", () => {
    expect(satisfies("^1", "1.4.2")).toBe(true);
    expect(satisfies("^2", "1.0.0")).toBe(false);
    expect(satisfies(">=1", "1.0.0")).toBe(false);
    expect(satisfies("^1", "x")).toBe(false);
    expect(satisfies("^1", "2.0.0", [1])).toBe(true); // previous major kept one release
  });
});

describe("bundle loader", () => {
  it("refuses a URL that is not a content-addressed asset", async () => {
    await expect(loadViewsBundle("https://evil.example/x.js")).rejects.toThrow(/content-addressed/);
  });

  it("refuses a hash-named bundle on another origin, and other paths on this one", async () => {
    const h = "a".repeat(64);
    await expect(loadViewsBundle(`https://evil.example/api/v1/theme/assets/${h}.js`)).rejects.toThrow(/this origin/);
    await expect(loadViewsBundle(`//evil.example/api/v1/theme/assets/${h}.js`)).rejects.toThrow(/this origin/);
    await expect(loadViewsBundle(`/uploads/${h}.js`)).rejects.toThrow(/this origin/);
    expect(trustedThemeAsset(`/api/v1/theme/assets/${h}.js`, "js")?.hash).toBe(h);
    expect(trustedThemeAsset(`/api/v1/theme/assets/${h}.js`, "css")).toBeNull();
  });
});

const answer = (id: string, color: string, views = "") => ({
  theme_id: id, tokens_css: `:root{--primary:${color};}`, theme_css_url: "", views_url: views, settings: {}, hash: "h-" + id,
});

describe("applyWorkspaceTheme", () => {
  it("writes tokens style and caches them per slug", async () => {
    vi.mocked(api).mockResolvedValue(answer("a", "1 2% 3%"));
    await applyWorkspaceTheme("alpha");
    expect(document.getElementById("mesh-theme-tokens")?.textContent).toContain("1 2% 3%");
    expect(JSON.parse(localStorage.getItem(THEME_CACHE_PREFIX + "alpha")!).hash).toBe("h-a");
  });

  it("switching to a workspace seen before paints its cached theme before the network answers (no flash)", async () => {
    vi.mocked(api).mockResolvedValueOnce(answer("a", "1 2% 3%")).mockResolvedValueOnce(answer("b", "4 5% 6%"));
    await applyWorkspaceTheme("alpha");
    await applyWorkspaceTheme("beta");
    expect(document.getElementById("mesh-theme-tokens")?.textContent).toContain("4 5% 6%");

    let release!: (v: unknown) => void;
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => { release = r; }));
    const pending = applyWorkspaceTheme("alpha");
    // network still pending: the frame already carries alpha's tokens
    expect(document.getElementById("mesh-theme-tokens")?.textContent).toContain("1 2% 3%");
    release(answer("a", "1 2% 3%"));
    await pending;
    expect(document.querySelectorAll("#mesh-theme-tokens").length).toBe(1);
  });

  it("a bundle that fails to load keeps tokens and falls back to default views", async () => {
    vi.mocked(api).mockResolvedValue(answer("c", "7 8% 9%", "/api/v1/theme/assets/not-a-hash.js"));
    await act(async () => { await applyWorkspaceTheme("gamma"); });
    expect(document.getElementById("mesh-theme-tokens")?.textContent).toContain("7 8% 9%");
    expect(useThemeStore.getState().viewsThemeId).toBe(DEFAULT_THEME_ID);
    expect(issues().some((i) => i.code === "bundle-load-failed")).toBe(true);
  });

  it("a stale answer (workspace switched meanwhile) is dropped", async () => {
    vi.mocked(api).mockResolvedValue(answer("a", "1 2% 3%"));
    await applyWorkspaceTheme("alpha", () => true);
    expect(document.getElementById("mesh-theme-tokens")).toBeNull();
  });

  it("dev bundle: the rebuild watcher starts only for a live request, never after a stale one", async () => {
    const opened: string[] = [];
    vi.stubGlobal("EventSource", function (this: unknown, url: string) { opened.push(url); return { close: vi.fn() }; });
    localStorage.setItem(DEV_BUNDLE_KEY, "http://127.0.0.1:4173/views.js");
    vi.mocked(api).mockResolvedValue(answer("a", "1 2% 3%"));
    // the workspace changes while the bundle is loading
    let stale = false;
    vi.mocked(loadDevViewsBundle).mockImplementationOnce(async () => { stale = true; return { views: {} }; });
    await applyWorkspaceTheme("alpha", () => stale);
    expect(opened).toEqual([]);
    await applyWorkspaceTheme("alpha");
    expect(opened).toEqual(["http://127.0.0.1:4173/events"]);
    vi.unstubAllGlobals();
  });

  it("theme CSS goes into the mesh-theme layer", async () => {
    vi.mocked(api).mockResolvedValue({ ...answer("d", "1 1% 1%"), theme_css_url: `/api/v1/theme/assets/${"b".repeat(64)}.css` });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(".x{color:red}")));
    await applyWorkspaceTheme("delta");
    expect(document.getElementById("mesh-theme-css")?.textContent).toMatch(/^@layer mesh-theme \{/);
    vi.unstubAllGlobals();
  });
});

describe("token specificity", () => {
  it("rewrites :root and .dark so brandkit.css cannot outrank them", async () => {
    const { strengthenTokens } = await import("../apply");
    expect(strengthenTokens(":root{--a:1;}\n.dark{--a:2;}")).toBe("html:root{--a:1;}\nhtml.dark{--a:2;}");
  });
});
