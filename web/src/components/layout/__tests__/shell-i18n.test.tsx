/**
 * Shell strings (sidebar, header, app layout) come from the `shell` namespace:
 * every key the layout files reference must resolve, and switching the
 * language re-renders the navigation in Russian.
 */
import fs from "node:fs";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";

vi.mock("@/lib/api", () => ({ api: vi.fn().mockResolvedValue({}) }));
vi.mock("@/lib/mentions/inbox", () => ({
  fetchUnseenMentionCount: vi.fn().mockResolvedValue(0),
}));

import i18n, { setLanguage } from "@/i18n";
import { Sidebar } from "@/components/layout/sidebar";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";

const LAYOUT_DIR = path.resolve(__dirname, "..");
const FILES = [
  "sidebar.tsx",
  "header.tsx",
  "app-layout.tsx",
  "../../core/slots/nav-sidebar/mesh-default-view.tsx",
  "../../core/slots/nav-topbar/mesh-default-view.tsx",
];

function referencedKeys(): string[] {
  const keys = new Set<string>();
  for (const f of FILES) {
    const src = fs.readFileSync(path.join(LAYOUT_DIR, f), "utf8");
    // t("a.b") calls plus keys stored in module-level tables (titleKey/labelKey).
    for (const m of src.matchAll(/\bt\("([\w.]+)"/g)) keys.add(m[1]!);
    for (const m of src.matchAll(/\b(?:titleKey|labelKey): "([\w.]+)"/g)) keys.add(m[1]!);
  }
  return [...keys].sort();
}

describe("shell namespace", () => {
  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("every key used by the layout files exists in en and ru", () => {
    const keys = referencedKeys();
    expect(keys.length).toBeGreaterThan(40);
    const missing = keys.flatMap((k) =>
      ["en", "ru"].filter((lng) => !i18n.exists(`shell:${k}`, { lng })).map((lng) => `${lng}:${k}`),
    );
    expect(missing).toEqual([]);
  });

  describe("sidebar", () => {
    beforeEach(() => {
      useWorkspaceStore.setState({
        currentWorkspace: { id: "ws-1", slug: "acme", name: "Acme" } as never,
        workspaces: [],
        isLoading: false,
      });
      useProjectStore.setState({ projects: [], isLoading: false, error: null });
    });

    function renderSidebar() {
      const router = createMemoryRouter(
        [{ path: "/w/:wsSlug/*", element: <Sidebar collapsed={false} /> }],
        { initialEntries: ["/w/acme/dashboard"] },
      );
      return render(<RouterProvider router={router} />);
    }

    it("renders English labels by default and Russian after a language switch", async () => {
      const view = renderSidebar();
      expect(screen.getByText("Dashboard")).toBeTruthy();
      expect(screen.getByText("Triage Inbox")).toBeTruthy();
      expect(screen.getByText("No projects yet")).toBeTruthy();
      view.unmount();

      await act(async () => {
        await setLanguage("ru");
      });
      renderSidebar();
      expect(screen.getByText("Дашборд")).toBeTruthy();
      expect(screen.getByText("Входящие на разбор")).toBeTruthy();
      expect(screen.getByText("Проектов пока нет")).toBeTruthy();
      expect(screen.queryByText("Dashboard")).toBeNull();
    });
  });
});
