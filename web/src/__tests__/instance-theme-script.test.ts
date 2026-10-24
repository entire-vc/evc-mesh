import { beforeEach, describe, expect, it, vi } from "vitest";
// Raw text of the static script (see index-html.test.ts for why ?raw).
import script from "../../public/instance-theme.js?raw";

const theme = (primary: string, dark = "dark-chrome") => ({
  hash: "h-" + primary,
  tokens_css: `:root{--primary:${primary};}`,
  brand: { theme_color: primary, theme_color_dark: dark },
});
const meta = () => document.querySelector('meta[name="theme-color"]') as HTMLMetaElement;
const style = () => document.getElementById("mesh-instance-theme");
const flush = () => new Promise((r) => setTimeout(r, 0));

function run(path: string, answer: unknown) {
  window.history.pushState({}, "", path);
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      if (answer instanceof Error) throw answer;
      return new Response(JSON.stringify(answer), { status: 200 });
    }),
  );
  new Function(script)();
  return flush();
}

beforeEach(async () => {
  // Observers of earlier runs react to this class change; let them finish
  // before the head is reset.
  document.documentElement.className = "";
  await flush();
  document.head.innerHTML = '<meta name="theme-color" content="default-chrome" />';
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("public/instance-theme.js", () => {
  it("paints tokens and chrome colour on the sign-in screen, and caches the answer", async () => {
    await run("/login", theme("1 2% 3%"));
    expect(style()?.textContent).toContain("--primary:1 2% 3%");
    expect(meta().content).toBe("1 2% 3%");
    expect(JSON.parse(localStorage.getItem("mesh-instance-theme")!).hash).toBe("h-1 2% 3%");
  });

  it("applies the cached theme at once, before the answer", () => {
    localStorage.setItem("mesh-instance-theme", JSON.stringify(theme("cached")));
    window.history.pushState({}, "", "/login");
    vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
    new Function(script)();
    expect(meta().content).toBe("cached");
    expect(style()?.textContent).toContain("cached");
  });

  it("paints the instance tokens on the app's static shell too (no workspace cache yet)", async () => {
    await run("/board", theme("b"));
    expect(style()?.textContent).toContain("--primary:b");
    expect(meta().content).toBe("b");
  });

  it("inside /w/<slug>/ the cached workspace theme paints before the first frame and the instance tokens stay out", async () => {
    localStorage.setItem(
      "mesh-theme:acme",
      JSON.stringify({ hash: "w", tokens_css: ":root{--primary:ws;}", css: ".x{color:red}" }),
    );
    await run("/w/acme/dashboard", theme("inst"));
    expect(document.getElementById("mesh-theme-tokens")?.textContent).toContain("--primary:ws");
    expect(document.getElementById("mesh-theme-css")?.textContent).toMatch(/^@layer mesh-theme \{/);
    expect(style()).toBeNull();
    expect(meta().content).toBe("inst");
  });

  it("uses the dark chrome colour in dark mode", async () => {
    document.documentElement.classList.add("dark");
    await run("/login", theme("light-c", "dark-c"));
    expect(meta().content).toBe("dark-c");
  });

  it("keeps the default look when the request fails", async () => {
    await run("/login", new Error("offline"));
    expect(style()).toBeNull();
    expect(meta().content).toBe("default-chrome");
  });
});
