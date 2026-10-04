import { act, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Sidebar } from "../sidebar";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useAuthStore } from "@/stores/auth";

const workspace = { id: "ws-loading", slug: "acme", name: "Acme" } as never;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  useAuthStore.setState({ user: null });
  useWorkspaceStore.setState({ workspaces: [workspace], currentWorkspace: workspace, isLoading: false });
  useProjectStore.setState({ projects: [], currentProject: null, isLoading: false, error: null });
  // HTTP boundary only; the real store, api wrapper and sidebar run unchanged.
  vi.stubGlobal("fetch", vi.fn(async () => json({})));
});
afterEach(() => vi.unstubAllGlobals());
const renderSidebar = (collapsed = false) => render(
  <MemoryRouter initialEntries={[useWorkspaceStore.getState().currentWorkspace ? "/w/acme" : "/t/missing"]}>
    <Routes>
      <Route path="/w/:wsSlug" element={<Sidebar collapsed={collapsed} />} />
      <Route path="/t/:taskId" element={<Sidebar collapsed={collapsed} />} />
    </Routes>
  </MemoryRouter>,
);

describe("Sidebar — pending navigation is not an empty project list", () => {
  it("uses a skeleton before a workspace is selected on a cold deep link", () => {
    useWorkspaceStore.setState({ currentWorkspace: null, isLoading: true });
    renderSidebar();
    expect(screen.queryByText("No projects yet")).not.toBeInTheDocument();
    expect(screen.getByTestId("sidebar-projects-loading")).toHaveAttribute("aria-busy", "true");
  });

  it.each([false, true])("leaves projects blank after loading without a workspace (collapsed=%s)", (collapsed) => {
    useWorkspaceStore.setState({ currentWorkspace: null });
    renderSidebar(collapsed);
    expect(screen.queryByTestId("sidebar-projects-loading")).not.toBeInTheDocument();
    expect(screen.queryByText("No projects yet")).not.toBeInTheDocument();
  });

  it("replaces pending projects with the actual response", async () => {
    let finish!: (response: Response) => void;
    const response = new Promise<Response>((resolve) => { finish = resolve; });
    vi.mocked(fetch).mockImplementation(async (url) => String(url).endsWith("/projects") ? response : json({}));
    const pending = useProjectStore.getState().fetchProjects("ws-loading");
    renderSidebar();
    expect(screen.queryByText("No projects yet")).not.toBeInTheDocument();
    expect(screen.getByTestId("sidebar-projects-loading")).toBeInTheDocument();
    await act(async () => {
      finish(json({ items: [{ id: "project", workspace_id: "ws-loading", name: "Loaded project", slug: "loaded" }] }));
      await pending;
    });
    expect(screen.getByText("Loaded project").closest("a")).toHaveAttribute("href", "/w/acme/p/loaded");
    expect(screen.queryByTestId("sidebar-projects-loading")).not.toBeInTheDocument();
  });

  it("positive control: shows the empty state after a successful empty response", async () => {
    vi.mocked(fetch).mockResolvedValue(json({ items: [] }));
    await useProjectStore.getState().fetchProjects("ws-loading");
    renderSidebar();
    expect(screen.getByText("No projects yet")).toBeInTheDocument();
    expect(screen.queryByTestId("sidebar-projects-loading")).not.toBeInTheDocument();
  });

  it("does not call a failed request an empty project list", async () => {
    vi.mocked(fetch).mockResolvedValue(json({ error: "Cannot load projects" }, 500));
    await useProjectStore.getState().fetchProjects("ws-loading");
    renderSidebar();
    expect(screen.queryByText("No projects yet")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Request failed");
  });

  it("shows pending navigation in the collapsed mobile rail", () => {
    useWorkspaceStore.setState({ currentWorkspace: null, isLoading: true });
    renderSidebar(true);
    expect(screen.getByTestId("sidebar-projects-loading")).toHaveAttribute("aria-busy", "true");
    expect(screen.queryByText("No projects yet")).not.toBeInTheDocument();
  });

  it("shows a project request failure in the collapsed mobile rail", async () => {
    vi.mocked(fetch).mockResolvedValue(json({ error: "Cannot load projects" }, 500));
    await useProjectStore.getState().fetchProjects("ws-loading");
    renderSidebar(true);
    expect(screen.getByRole("alert")).toHaveAccessibleName("Request failed");
    expect(screen.queryByTestId("sidebar-projects-loading")).not.toBeInTheDocument();
  });
});
