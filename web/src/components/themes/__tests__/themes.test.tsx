import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn() };
});

import { api } from "@/lib/api";
import { setLanguage } from "@/i18n";
import { SchemaForm, type FieldSchema } from "@/components/extensions/schema-form";
import { hexToHsl, hslToHex, parseTokensCss } from "../color";
import { MemoryRouter, Route, Routes } from "react-router";
import { ThemeSettingsPanel } from "../theme-settings-panel";
import { ThemePreview } from "../theme-preview";
import { BUILTIN_FIELDS, PICKER_FIELD_LABEL_KEYS } from "@/core/slots/board-data";
import { CORE_TASK_FIELDS } from "@/components/extensions/schema-form/schema-form";

const apiMock = vi.mocked(api);

beforeEach(async () => {
  apiMock.mockReset();
  await setLanguage("en");
});

describe("colour conversion", () => {
  it("round-trips hex and the token triplet", () => {
    expect(hexToHsl("#ffffff")).toBe("0 0% 100%");
    expect(hexToHsl("#f00")).toBe("0 100% 50%");
    expect(hexToHsl("zzz")).toBeNull();
    for (const hex of ["#1a73e8", "#222222", "#e11d48"]) {
      expect(hslToHex(hexToHsl(hex) as string)?.toLowerCase()).toBe(hex);
    }
    expect(hslToHex("not a colour")).toBeNull();
  });
  it("reads the server's token css per mode", () => {
    const t = parseTokensCss(":root{--primary:222 47% 20%;--radius:0.5rem;}\n.dark{--primary:210 40% 96%;}\n");
    expect(t.light.primary).toBe("222 47% 20%");
    expect(t.dark.primary).toBe("210 40% 96%");
  });
});

function form(properties: Record<string, FieldSchema>, onSubmit = vi.fn().mockResolvedValue(undefined), value?: Record<string, unknown>) {
  render(<SchemaForm schema={{ type: "object", properties }} value={value} onSubmit={onSubmit} />);
  return onSubmit;
}

describe("new SchemaForm widgets", () => {
  it("color bound to a token stores H S% L% and shows #hex", async () => {
    const onSubmit = form({ accent: { type: "string", title: "Accent", "x-widget": "color", "x-token": "primary" } }, undefined, { accent: "0 0% 100%" });
    const text = screen.getByLabelText("Accent") as HTMLInputElement;
    expect(text.value).toBe("#ffffff");
    fireEvent.change(text, { target: { value: "#ff0000" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ accent: "0 100% 50%" }));
  });

  it("color flags a bad value and blocks the save instead of keeping the old one", async () => {
    const onSubmit = form({ accent: { type: "string", title: "Accent", "x-widget": "color" } }, undefined, { accent: "#ffffff" });
    fireEvent.change(screen.getByLabelText("Accent"), { target: { value: "#12" } });
    expect(screen.getByText("Use #rgb or #rrggbb")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await new Promise((r) => setTimeout(r, 50));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("a token colour out of range blocks the save", async () => {
    const onSubmit = form({ accent: { type: "string", title: "Accent", "x-widget": "color", "x-token": "primary" } }, undefined, { accent: "999 999% 999%" });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await new Promise((r) => setTimeout(r, 50));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("layout-picker is a radio group over the enum", async () => {
    const onSubmit = form({ layout: { type: "string", title: "Layout", enum: ["compact", "wide"], "x-widget": "layout-picker", "x-options": { wide: "Wide" } } });
    fireEvent.click(screen.getByRole("radio", { name: "Wide" }));
    expect(screen.getByRole("radio", { name: "Wide" }).getAttribute("aria-checked")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ layout: "wide" }));
  });

  it("field-picker adds a field and reorders it with the keyboard-reachable buttons", async () => {
    const onSubmit = form(
      { card_fields: { type: "array", title: "Card fields", items: { type: "string" }, "x-widget": "field-picker" } },
      undefined,
      { card_fields: ["status", "priority"] },
    );
    fireEvent.click(screen.getByRole("button", { name: "Move up: Priority" }));
    fireEvent.change(screen.getByLabelText("Add to Card fields"), { target: { value: "due_date" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ card_fields: ["priority", "status", "due_date"] }));
  });

  it("font-select stores the chosen stack and clears back to the theme default", async () => {
    const onSubmit = form({ font: { type: "string", title: "Font", enum: ["serif", "mono"], "x-options": { mono: "Monospace" }, "x-widget": "font-select" } });
    fireEvent.change(screen.getByLabelText("Font"), { target: { value: "mono" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ font: "mono" }));
    fireEvent.change(screen.getByLabelText("Font"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenLastCalledWith({}));
  });

  it("widget-picker offers the core widgets, adds one and removes another", async () => {
    const onSubmit = form(
      { widgets: { type: "array", title: "Widgets", items: { type: "string" }, "x-widget": "widget-picker" } },
      undefined,
      { widgets: ["tasks", "activity"] },
    );
    fireEvent.click(screen.getByRole("button", { name: "Remove: activity" }));
    expect(screen.getByLabelText("Add to Widgets")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ widgets: ["tasks"] }));
  });

  it("order-list only reorders and offers no add or remove", () => {
    form({ order: { type: "array", title: "Order", items: { type: "string" }, "x-widget": "order-list" } }, undefined, { order: ["a", "b"] });
    expect(screen.getByRole("button", { name: "Move down: a" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Remove:/ })).toBeNull();
  });

  it("image rejects a file over the limit", async () => {
    form({ logo: { type: "string", title: "Logo", "x-widget": "image" } });
    const big = new File([new Uint8Array(300 * 1024)], "big.png", { type: "image/png" });
    fireEvent.change(document.querySelector("input[type=file]") as HTMLInputElement, { target: { files: [big] } });
    expect(await screen.findByText("The image is over 200 KB")).toBeTruthy();
  });
});

const themes = {
  default: "mesh-default",
  builtin_available: [],
  themes: [
    {
      id: "graphite", name: "Graphite", version: "1.0.0", publisher: "x", description: "Dark dense", source: "builtin", trust_level: "builtin",
      modes: ["light", "dark"], has_css: false, has_views: false, contrast_warnings: [],
      tokens: { light: { primary: "222 47% 20%" }, dark: { primary: "210 40% 96%" } },
      settings: {
        workspace: {
          type: "object",
          properties: {
            accent: { type: "string", title: "Accent", "x-widget": "color", "x-token": "primary" },
            card_fields: { type: "array", title: "Card fields", items: { type: "string" }, "x-widget": "field-picker" },
          },
        },
      },
    },
  ],
};

function routes(selection: Record<string, unknown>, put = vi.fn().mockResolvedValue({ ...selection, theme_id: "graphite", version: 2, contrast_warnings: [{ message: "primary on background: 2.1" }] })) {
  apiMock.mockImplementation(async (path: string, opts?: { method?: string; body?: unknown }) => {
    if (path === "/api/v1/themes") return themes;
    if (path.startsWith("/api/v1/theme")) return { theme_id: "mesh-default", tokens_css: "" };
    if (path === "/api/v1/workspaces/ws1/theme") return opts?.method === "PUT" ? put(opts.body) : selection;
    throw new Error(`unexpected ${path}`);
  });
  return put;
}
const base = { theme_id: null, settings: {}, version: 1, contrast_warnings: [] };

describe("ThemeSettingsPanel (workspace)", () => {
  it("choose theme, edit a field, preview follows, save sends theme + full settings, contrast shown but not blocking", async () => {
    const put = routes(base);
    render(<ThemeSettingsPanel level="workspace" workspaceId="ws1" />);
    fireEvent.click(await screen.findByRole("radio", { name: "Graphite" }));
    const accent = await screen.findByLabelText("Accent");
    fireEvent.change(accent, { target: { value: "#ff0000" } });
    const preview = screen.getByRole("img", { name: "Theme preview" });
    expect(preview.style.getPropertyValue("--primary")).toBe("0 100% 50%");
    fireEvent.click(screen.getByRole("button", { name: "Save theme" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith({ theme_id: "graphite", settings: expect.objectContaining({ accent: "0 100% 50%" }) }));
    expect(await screen.findByText("Low contrast in this theme")).toBeTruthy();
  });

  it("after a save the runtime theme of the workspace is re-fetched, so the board picks up the new settings", async () => {
    routes(base);
    render(
      <MemoryRouter initialEntries={["/w/acme/settings"]}>
        <Routes>
          <Route path="/w/:wsSlug/settings" element={<ThemeSettingsPanel level="workspace" workspaceId="ws1" />} />
        </Routes>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole("radio", { name: "Graphite" }));
    fireEvent.click(await screen.findByRole("button", { name: "Save theme" }));
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/v1/theme?workspace=acme"));
  });

  it("save is disabled until something changes; reset goes back to the instance theme", async () => {
    const put = routes({ ...base, theme_id: "graphite", settings: { accent: "0 0% 100%" } });
    render(<ThemeSettingsPanel level="workspace" workspaceId="ws1" />);
    await screen.findByLabelText("Accent");
    expect((screen.getByRole("button", { name: "Save theme" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Reset to the instance theme" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith({ theme_id: null, settings: {} }));
  });

  it("role still loading: no read-only banner, placeholder instead", () => {
    routes(base);
    render(<ThemeSettingsPanel level="workspace" workspaceId="ws1" readOnly roleLoading />);
    expect(screen.queryByText("Only workspace owners and admins can change the theme.")).toBeNull();
    expect(screen.getByTestId("theme-role-loading")).toBeTruthy();
  });

  it("role lookup failed: no read-only banner either (the role is unknown, not lacking)", async () => {
    routes(base);
    render(
      <MemoryRouter initialEntries={["/w/acme/settings"]}>
        <Routes>
          <Route path="/w/:wsSlug/settings" element={<ThemeSettingsPanel level="workspace" workspaceId="ws1" readOnly roleUnknown />} />
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByRole("radiogroup");
    expect(screen.queryByText("Only workspace owners and admins can change the theme.")).toBeNull();
  });

  it("read-only: cards are disabled, no form, no save", async () => {
    routes(base);
    render(
      <MemoryRouter initialEntries={["/w/acme/settings"]}>
        <Routes>
          <Route path="/w/:wsSlug/settings" element={<ThemeSettingsPanel level="workspace" workspaceId="ws1" readOnly />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText(/Only workspace owners and admins/)).toBeTruthy();
    expect((screen.getByRole("radio", { name: "Graphite" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "Save theme" })).toBeNull();
  });

  it("read-only: marks the theme in force, taken from the resolved theme (a member cannot read the saved selection)", async () => {
    routes(base);
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return themes;
      if (path === "/api/v1/theme?workspace=acme") return { theme_id: "graphite", tokens_css: "" };
      if (path.startsWith("/api/v1/theme")) return { theme_id: "mesh-default", tokens_css: "" };
      throw new Error(`unexpected ${path}`);
    });
    render(
      <MemoryRouter initialEntries={["/w/acme/settings"]}>
        <Routes>
          <Route path="/w/:wsSlug/settings" element={<ThemeSettingsPanel level="workspace" workspaceId="ws1" readOnly />} />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByRole("radio", { name: "Graphite" }).getAttribute("aria-checked")).toBe("true"));
    expect(screen.getAllByRole("radio").filter((r) => r.getAttribute("aria-checked") === "true")).toHaveLength(1);
    expect(apiMock).not.toHaveBeenCalledWith("/api/v1/workspaces/ws1/theme", expect.anything());
  });

  it("read-only: a resolved theme that is not installed (the built-in default) marks the base card", async () => {
    routes(base);
    render(
      <MemoryRouter initialEntries={["/w/acme/settings"]}>
        <Routes>
          <Route path="/w/:wsSlug/settings" element={<ThemeSettingsPanel level="workspace" workspaceId="ws1" readOnly />} />
        </Routes>
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByRole("radio", { name: /Instance theme/ }).getAttribute("aria-checked")).toBe("true"));
    expect(screen.getAllByRole("radio").filter((r) => r.getAttribute("aria-checked") === "true")).toHaveLength(1);
  });

  it("read-only: a failed resolved-theme request marks no card and says the theme is unknown", async () => {
    routes(base);
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return themes;
      throw new Error(`unexpected ${path}`);
    });
    render(
      <MemoryRouter initialEntries={["/w/acme/settings"]}>
        <Routes>
          <Route path="/w/:wsSlug/settings" element={<ThemeSettingsPanel level="workspace" workspaceId="ws1" readOnly />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText("Current theme unknown")).toBeTruthy();
    expect(screen.getAllByRole("radio").filter((r) => r.getAttribute("aria-checked") === "true")).toHaveLength(0);
  });

  it("instance level: clicking the already-selected default theme with no saved selection does not throw", async () => {
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/themes") return { ...themes, themes: [{ ...themes.themes[0], id: "mesh-default", name: "Mesh" }] };
      if (path === "/api/v1/instance/theme") return null;
      return { theme_id: "mesh-default", tokens_css: "" };
    });
    render(<ThemeSettingsPanel level="instance" />);
    const card = await screen.findByRole("radio", { name: "Mesh" });
    expect(() => fireEvent.click(card)).not.toThrow();
    expect(card.getAttribute("aria-checked")).toBe("true");
  });

  it("shows an error with retry when the list fails", async () => {
    apiMock.mockRejectedValue(new Error("boom"));
    render(<ThemeSettingsPanel level="workspace" workspaceId="ws1" />);
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });
});

describe("ThemePreview card fields", () => {
  const text = () => screen.getAllByTestId("theme-preview-fields")[0]?.textContent ?? "";

  it("lists the chosen fields in order and changes with them", () => {
    const { rerender } = render(<ThemePreview cardFields={["priority", "due_date"]} />);
    const before = text();
    rerender(<ThemePreview cardFields={["assignee", "priority"]} />);
    expect(text()).not.toBe(before);
    expect(text()).toMatch(/^Assignee.*Priority/);
  });

  it("collapses long selections so the fixed-height card never clips", () => {
    render(<ThemePreview cardFields={["assignee", "priority", "due_date", "labels", "estimate"]} />);
    expect(text()).toMatch(/\+2$/);
  });

  it("labels status and estimated_hours instead of showing the raw key", () => {
    render(<ThemePreview cardFields={["status", "estimated_hours"]} />);
    expect(text()).toBe("StatusEstimate");
  });

  it("every field-picker key resolves to a label", () => {
    for (const { value } of CORE_TASK_FIELDS) {
      const known = BUILTIN_FIELDS.some((f) => f.key === value) || value in PICKER_FIELD_LABEL_KEYS;
      expect(known, value).toBe(true);
    }
  });

  it("keeps the generic sample card when no fields are given", () => {
    render(<ThemePreview />);
    expect(screen.queryByTestId("theme-preview-fields")).toBeNull();
    render(<ThemePreview cardFields={[]} />);
    expect(screen.queryByTestId("theme-preview-fields")).toBeNull();
  });
});
