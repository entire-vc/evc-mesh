import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: apiMock }));

import { setLanguage } from "@/i18n";
import { WorkspaceThemeLink } from "@/components/themes/workspace-theme-link";

const mount = () => render(<MemoryRouter><WorkspaceThemeLink wsSlug="acme" /></MemoryRouter>);

beforeEach(async () => {
  await setLanguage("en");
  apiMock.mockReset();
});

describe("WorkspaceThemeLink", () => {
  it("points to the active theme in Extensions, by name", async () => {
    apiMock.mockImplementation(async (path: string) => {
      if (path === "/api/v1/theme?workspace=acme") return { theme_id: "graphite" };
      if (path === "/api/v1/themes") return { default: "mesh-default", themes: [{ id: "graphite", name: "Graphite" }], builtin_available: [] };
      throw new Error(path);
    });
    mount();
    const link = await screen.findByRole("link", { name: "Theme is in Extensions → Graphite" });
    expect(link.getAttribute("href")).toBe("/w/acme/extensions/graphite");
  });

  it("falls back to the themes in the catalog when the active theme is not known", async () => {
    apiMock.mockRejectedValue(new Error("down"));
    mount();
    const link = await screen.findByRole("link", { name: "Themes are in Extensions" });
    expect(link.getAttribute("href")).toBe("/w/acme/extensions?kind=theme");
  });
});
