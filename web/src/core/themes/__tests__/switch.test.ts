import { beforeEach, describe, expect, it, vi } from "vitest";
import { registerDefaultSlotView, resolveSlotView, resolveThemeSettings } from "@/core/slots/registry";
import { applyWorkspaceTheme } from "../runtime";
import { useModeStore } from "../mode";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
vi.mock("../bundle", async (orig) => ({
  ...(await orig<typeof import("../bundle")>()),
  loadViewsBundle: vi.fn(async () => ({ views: { "board.card": { contract: "^1", view: themedCard } } })),
}));
import { api } from "@/lib/api";

const defaultCard = () => null;
function themedCard() {
  return null;
}
const ok = (slug: string, views: string) => ({
  theme_id: "t-" + slug, tokens_css: ":root{--primary:1 1% 1%;}", theme_css_url: "", views_url: views,
  settings: { card_fields: [slug] }, hash: "h-" + slug,
});

beforeEach(() => {
  document.head.innerHTML = "";
  localStorage.clear();
  vi.mocked(api).mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
  registerDefaultSlotView("board.card", defaultCard);
});

describe("workspace switch", () => {
  it("a workspace whose theme fails to load does not inherit the previous workspace's views, settings or tokens", async () => {
    vi.mocked(api).mockResolvedValueOnce(ok("a", `/api/v1/theme/assets/${"a".repeat(64)}.js`));
    await applyWorkspaceTheme("a");
    expect(resolveSlotView("board.card")).toBe(themedCard);
    expect(resolveThemeSettings()).toEqual({ card_fields: ["a"] });
    expect(document.getElementById("mesh-theme-tokens")).not.toBeNull();

    vi.mocked(api).mockRejectedValueOnce(new Error("offline"));
    await applyWorkspaceTheme("b");
    expect(resolveSlotView("board.card")).toBe(defaultCard);
    expect(resolveThemeSettings()).toEqual({});
    expect(document.getElementById("mesh-theme-tokens")).toBeNull();
  });

  it("a one-mode theme's forced mode does not outlive it when the next workspace's theme fails to load", async () => {
    vi.mocked(api).mockResolvedValueOnce({ ...ok("a", ""), modes: ["light"] });
    await applyWorkspaceTheme("a");
    expect(useModeStore.getState().forced).toBe("light");

    vi.mocked(api).mockRejectedValueOnce(new Error("offline"));
    await applyWorkspaceTheme("b");
    expect(useModeStore.getState().forced).toBeNull();

    // Back to "a": its cached modes force the mode again before the answer arrives.
    vi.mocked(api).mockReturnValueOnce(new Promise(() => {}));
    void applyWorkspaceTheme("a");
    expect(useModeStore.getState().forced).toBe("light");
  });

  it("a cache-miss switch drops the previous workspace's styles while its own answer is pending", async () => {
    vi.mocked(api).mockResolvedValueOnce(ok("a", ""));
    await applyWorkspaceTheme("a");
    expect(document.getElementById("mesh-theme-tokens")).not.toBeNull();

    vi.mocked(api).mockReturnValueOnce(new Promise(() => {})); // never answers
    void applyWorkspaceTheme("b");
    expect(document.getElementById("mesh-theme-tokens")).toBeNull();
  });

  it("a failed workspace request on a cache miss keeps the instance theme under the shell", async () => {
    localStorage.setItem("mesh-instance-theme", JSON.stringify({ tokens_css: ":root{--primary:9 9% 9%;}" }));
    const pre = document.createElement("style");
    pre.id = "mesh-instance-theme";
    document.head.appendChild(pre);
    vi.mocked(api).mockRejectedValueOnce(new Error("offline"));
    await applyWorkspaceTheme("fresh");
    expect(document.getElementById("mesh-instance-theme")).not.toBeNull();
    expect(document.getElementById("mesh-theme-tokens")).toBeNull();
  });

  it("a workspace on mesh-default (empty tokens_css) drops the instance theme's tokens", async () => {
    const pre = document.createElement("style");
    pre.id = "mesh-instance-theme";
    document.head.appendChild(pre);
    vi.mocked(api).mockResolvedValueOnce({ ...ok("d", ""), tokens_css: "" });
    await applyWorkspaceTheme("d");
    expect(document.getElementById("mesh-instance-theme")).toBeNull();
    expect(document.getElementById("mesh-theme-tokens")).toBeNull();
  });

  it("re-applying the same workspace keeps its views", async () => {
    vi.mocked(api).mockResolvedValue(ok("a", `/api/v1/theme/assets/${"a".repeat(64)}.js`));
    await applyWorkspaceTheme("a");
    await applyWorkspaceTheme("a");
    expect(resolveSlotView("board.card")).toBe(themedCard);
  });

  it("a failure of a request that a newer switch already replaced does not touch the page", async () => {
    vi.mocked(api).mockResolvedValueOnce(ok("a", "")); // theme of A, painted
    await applyWorkspaceTheme("a");
    let fail!: (e: Error) => void;
    vi.mocked(api).mockImplementationOnce(() => new Promise((_, rej) => { fail = rej; }));
    let stale = false;
    const old = applyWorkspaceTheme("b", () => stale);
    stale = true; // user moved on to another workspace whose tokens are now painted
    document.head.innerHTML = '<style id="mesh-theme-tokens">html:root{--primary:9 9% 9%;}</style>';
    fail(new Error("offline"));
    await old;
    expect(document.getElementById("mesh-theme-tokens")?.textContent).toContain("9 9% 9%");
  });

  it("a malformed answer of a replaced request does not clear the current workspace's styles", async () => {
    let answer!: (v: unknown) => void;
    vi.mocked(api).mockImplementationOnce(() => new Promise((res) => { answer = res; }));
    let stale = false;
    const old = applyWorkspaceTheme("b", () => stale);
    stale = true;
    document.head.innerHTML = '<style id="mesh-theme-tokens">html:root{--primary:9 9% 9%;}</style>';
    answer({ theme_id: "x" }); // no tokens_css
    await old;
    expect(document.getElementById("mesh-theme-tokens")?.textContent).toContain("9 9% 9%");
  });

  it("a bundle that fails after its request was replaced does not drop the current workspace's views", async () => {
    const { loadViewsBundle } = await import("../bundle");
    vi.mocked(api).mockResolvedValueOnce(ok("a", `/api/v1/theme/assets/${"a".repeat(64)}.js`));
    await applyWorkspaceTheme("a");
    expect(resolveSlotView("board.card")).toBe(themedCard);
    let fail!: (e: Error) => void;
    vi.mocked(loadViewsBundle).mockImplementationOnce(() => new Promise((_, rej) => { fail = rej; }));
    vi.mocked(api).mockResolvedValueOnce(ok("a", `/api/v1/theme/assets/${"b".repeat(64)}.js`));
    let stale = false;
    const old = applyWorkspaceTheme("a", () => stale);
    await vi.waitFor(() => expect(fail).toBeTypeOf("function"));
    stale = true;
    fail(new Error("boom"));
    await old;
    expect(resolveSlotView("board.card")).toBe(themedCard);
  });
});
