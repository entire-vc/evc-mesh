import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { Sidebar } from "../sidebar";
import { setLanguage } from "@/i18n";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useAuthStore } from "@/stores/auth";

const workspace = { id: "ws1", slug: "acme", name: "Acme" } as never;

beforeEach(async () => {
  await setLanguage("en");
  useAuthStore.setState({ user: null });
  useWorkspaceStore.setState({ workspaces: [workspace], currentWorkspace: workspace, isLoading: false });
  useProjectStore.setState({ projects: [], currentProject: null, isLoading: false, error: null });
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })));
});
afterEach(() => vi.unstubAllGlobals());

const renderAt = (path: string, collapsed: boolean) =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/w/:wsSlug/*" element={<Sidebar collapsed={collapsed} />} />
      </Routes>
    </MemoryRouter>,
  );

describe("Sidebar — Extensions item replaces Integrations", () => {
  it.each([false, true])("links to /extensions with an accessible name (collapsed=%s)", (collapsed) => {
    renderAt("/w/acme/dashboard", collapsed);
    const link = screen.getByRole("link", { name: "Extensions" });
    expect(link).toHaveAttribute("href", "/w/acme/extensions");
    expect(screen.queryByRole("link", { name: "Integrations" })).not.toBeInTheDocument();
  });

  it.each([false, true])("marks the item active on the extensions route (collapsed=%s)", (collapsed) => {
    renderAt("/w/acme/extensions", collapsed);
    expect(screen.getByRole("link", { name: "Extensions" }).className).toContain("bg-sidebar-accent");
  });
});
