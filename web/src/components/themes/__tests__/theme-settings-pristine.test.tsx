import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

import { api } from "@/lib/api";
import { setLanguage } from "@/i18n";
import { ThemeSettingsPanel } from "../theme-settings-panel";

// Read at run time, not imported: the web image builds without internal/, and tsc -b would fail to resolve it.
const manifest = JSON.parse(
  readFileSync(resolve(import.meta.dirname, "../../../../../internal/themes/builtin/mesh-default/mesh-extension.json"), "utf8"),
) as { version: string; schemas: { instance: unknown; workspace: unknown } };

const apiMock = vi.mocked(api);

// The shipped built-in theme, as the server lists it: its schemas carry defaults (radius, font, density) and an accent without one.
const meshDefault = {
  id: "mesh-default", name: "Mesh", version: manifest.version, publisher: "x", description: "", source: "builtin", trust_level: "builtin",
  modes: ["light", "dark"], has_css: false, has_views: false, contrast_warnings: [],
  tokens: { light: { primary: "221 83% 53%" }, dark: { primary: "217 91% 60%" } },
  settings: { instance: manifest.schemas.instance, workspace: manifest.schemas.workspace },
};
const nothingSaved = { theme_id: null, settings: {}, version: 1, contrast_warnings: [] };

// The saved selection answers the second load late, after the form has mounted from the first: the order a browser sees.
function routes(selection: Record<string, unknown>) {
  let reads = 0;
  apiMock.mockImplementation(async (path: string) => {
    if (path === "/api/v1/themes") return { default: "mesh-default", builtin_available: [], themes: [meshDefault] };
    if (path === "/api/v1/instance/theme" || path === "/api/v1/workspaces/ws1/theme") {
      if (reads++ > 0) await new Promise((r) => setTimeout(r, 50));
      return structuredClone(selection);
    }
    if (path.startsWith("/api/v1/theme")) return { theme_id: "mesh-default", tokens_css: "" };
    throw new Error(`unexpected ${path}`);
  });
}

beforeEach(async () => {
  apiMock.mockReset();
  await setLanguage("en");
});
afterEach(() => {
  document.documentElement.style.removeProperty("--primary");
  document.documentElement.classList.remove("dark");
});

// as the app mounts it (main.tsx): StrictMode runs the panel's load twice
const mount = (level: "instance" | "workspace") =>
  render(
    <StrictMode>
      <ThemeSettingsPanel level={level} workspaceId="ws1" />
    </StrictMode>,
  );

// mesh-default with a secret field added to its instance settings
const instanceSchema = manifest.schemas.instance as { properties: Record<string, unknown> };
const withSecret = {
  ...meshDefault,
  settings: {
    ...meshDefault.settings,
    instance: { ...instanceSchema, properties: { ...instanceSchema.properties, fonts_token: { type: "string", title: "Fonts token", "x-secret": true } } },
  },
};

describe("ThemeSettingsPanel right after loading, no edits", () => {
  it.each([
    ["instance", nothingSaved],
    ["instance", { ...nothingSaved, theme_id: "mesh-default", settings: { radius: "1rem" } }],
    ["workspace", { ...nothingSaved, theme_id: "mesh-default" }],
    ["workspace", { ...nothingSaved, theme_id: "mesh-default", settings: { accent: "0 100% 50%" } }],
  ] as const)("%s level, saved %j: no 'Unsaved changes', Save disabled", async (level, selection) => {
    routes(selection);
    mount(level);
    await screen.findByLabelText("Accent colour");
    // both loads answered: the late one has put the saved settings back into the draft
    await waitFor(() => expect(apiMock.mock.calls.filter(([p]) => String(p).endsWith("/theme") && !String(p).startsWith("/api/v1/theme")).length).toBe(2));
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByText("Unsaved changes")).toBeNull();
    expect(screen.getByRole("button", { name: "Save theme" }).hasAttribute("disabled")).toBe(true);
  });

  it("a real edit after both loads is still an unsaved change", async () => {
    routes(nothingSaved);
    mount("instance");
    await screen.findByLabelText("Accent colour");
    await new Promise((r) => setTimeout(r, 100));
    fireEvent.click(screen.getByRole("radio", { name: "Round" }));
    expect(await screen.findByText("Unsaved changes")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save theme" }).hasAttribute("disabled")).toBe(false);
  });

  it("typing only a secret is an unsaved change", async () => {
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return { default: "mesh-default", builtin_available: [], themes: [withSecret] };
      if (path === "/api/v1/instance/theme") return { ...nothingSaved, theme_id: "mesh-default" };
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    mount("instance");
    const secret = await screen.findByLabelText("Fonts token");
    await new Promise((r) => setTimeout(r, 100));
    expect(screen.queryByText("Unsaved changes")).toBeNull();
    fireEvent.change(secret, { target: { value: "s3cr3t" } });
    expect(await screen.findByText("Unsaved changes")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save theme" }).hasAttribute("disabled")).toBe(false);
  });

  it("a new secret that reads like a mask is still an unsaved change", async () => {
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return { default: "mesh-default", builtin_available: [], themes: [withSecret] };
      if (path === "/api/v1/instance/theme") return { ...nothingSaved, theme_id: "mesh-default" };
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    mount("instance");
    const secret = await screen.findByLabelText("Fonts token");
    await new Promise((r) => setTimeout(r, 100));
    fireEvent.change(secret, { target: { value: "***" } });
    expect(await screen.findByText("Unsaved changes")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save theme" }).hasAttribute("disabled")).toBe(false);
  });

  it("a secret already saved (masked) is not an unsaved change", async () => {
    let reads = 0;
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return { default: "mesh-default", builtin_available: [], themes: [withSecret] };
      if (path === "/api/v1/instance/theme") {
        if (reads++ > 0) await new Promise((r) => setTimeout(r, 50));
        return { ...nothingSaved, theme_id: "mesh-default", settings: { fonts_token: "***" } };
      }
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    mount("instance");
    // a saved secret shows as set, not as an input
    await screen.findByText("Fonts token");
    await waitFor(() => expect(reads).toBe(2));
    await new Promise((r) => setTimeout(r, 200));
    expect(screen.queryByText("Unsaved changes")).toBeNull();
    expect(screen.getByRole("button", { name: "Save theme" }).hasAttribute("disabled")).toBe(true);
  });

  it("replacing a saved secret with a value that reads like its mask is an unsaved change", async () => {
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return { default: "mesh-default", builtin_available: [], themes: [withSecret] };
      if (path === "/api/v1/instance/theme") return { ...nothingSaved, theme_id: "mesh-default", settings: { fonts_token: "***" } };
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    mount("instance");
    fireEvent.click(await screen.findByRole("button", { name: "Change: Fonts token" }));
    await new Promise((r) => setTimeout(r, 100));
    fireEvent.change(screen.getByLabelText("Fonts token"), { target: { value: "***" } });
    expect(await screen.findByText("Unsaved changes")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save theme" }).hasAttribute("disabled")).toBe(false);
  });

  it("an accent left unset shows the theme's own accent in the swatch, not black", async () => {
    routes(nothingSaved);
    mount("instance");
    const swatch = (await screen.findByLabelText("Pick a colour: Accent colour")) as HTMLInputElement;
    // 221 83% 53% is the theme's light primary
    expect(swatch.value).toBe("#2463eb");
    const text = screen.getByLabelText("Accent colour") as HTMLInputElement;
    expect(text.value).toBe("");
    // the placeholder names the same colour as the swatch, not an unrelated example
    expect(text.placeholder).toBe("#2463eb");
  });

  it("a theme without tokens of its own (the built-in look) shows the accent in force on the page", async () => {
    document.documentElement.style.setProperty("--primary", "174 72% 30%");
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return { default: "mesh-default", builtin_available: [], themes: [{ ...meshDefault, tokens: {} }] };
      if (path === "/api/v1/instance/theme") return nothingSaved;
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    mount("instance");
    const swatch = (await screen.findByLabelText("Pick a colour: Accent colour")) as HTMLInputElement;
    expect(swatch.value).not.toBe("#000000");
    expect(swatch.value).toBe("#158479");
  });

  it("an accent left unset follows a light -> dark switch while the panel is open", async () => {
    // the built-in look: no tokens of its own, the page's --primary per mode (brandkit.css light teal-500, dark teal-accent-400)
    document.documentElement.style.setProperty("--primary", "180 100% 21%");
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return { default: "mesh-default", builtin_available: [], themes: [{ ...meshDefault, tokens: {} }] };
      if (path === "/api/v1/instance/theme") return nothingSaved;
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    mount("instance");
    const swatch = (await screen.findByLabelText("Pick a colour: Accent colour")) as HTMLInputElement;
    expect(swatch.value).toBe("#006b6b");
    // what mode.ts paint() does on a mode change: the class flips and the page's tokens follow
    act(() => {
      document.documentElement.style.setProperty("--primary", "172 66% 50%");
      document.documentElement.classList.add("dark");
    });
    await waitFor(() => expect(swatch.value).toBe("#2bd4bd"));
    expect((screen.getByLabelText("Accent colour") as HTMLInputElement).placeholder).toBe("#2bd4bd");
    expect(screen.getByRole("button", { name: "Save theme" }).hasAttribute("disabled")).toBe(true);
  });
});
