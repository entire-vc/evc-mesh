/**
 * a workspace member outside a project opened /p/<slug>/stats and got a skeleton that never resolved —
 * the tab drew its widget only once the layout bound that project, and for a project missing from the viewer's list
 * nothing ever is bound. The board on the same project says «not found»; the Statistics tab must end there too.
 * Rendered under the real AppLayout, so the slug binding and the project guard are the production ones.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";

// mock: external boundary — the HTTP client; the stores, layout and page stay real.
vi.mock("@/lib/api", () => ({
  api: vi.fn(() => Promise.resolve({})),
  getAccessToken: vi.fn(() => null),
}));

import { AppLayout } from "@/components/layout/app-layout";
import { ProjectStatsPage } from "@/pages/project-stats";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";

const WS = { id: "ws-a", name: "Acme", slug: "acme" } as never;
const WS_B = { id: "ws-b", name: "Beta", slug: "beta" } as never;
const KEEP = { id: "p-keep", name: "Keep", slug: "keep" } as never;

function renderAt(path: string) {
  const router = createMemoryRouter(
    [{ element: <AppLayout />, children: [{ path: "w/:wsSlug/p/:projectSlug/stats", element: <ProjectStatsPage /> }] }],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
}

beforeEach(() => {
  window.matchMedia = vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }) as unknown as typeof window.matchMedia;
  useAuthStore.setState({ isAuthenticated: true, isLoading: false, user: { id: "u1" } as never });
  useWorkspaceStore.setState({ workspaces: [WS, WS_B], currentWorkspace: WS, fetchWorkspaces: vi.fn().mockResolvedValue(undefined) });
});

describe("project Statistics tab for a project the viewer cannot see", () => {
  it("says the project is not found, like the board, instead of a skeleton forever", async () => {
    // The viewer's list has other projects but not mesh-dev: they are not a member of it.
    useProjectStore.setState({
      projects: [KEEP],
      currentProject: null,
      loaded: true,
      loadedWorkspaceId: "ws-a",
      isLoading: false,
      fetchProjects: vi.fn().mockResolvedValue(undefined),
    });
    renderAt("/w/acme/p/mesh-dev/stats");
    const notFound = await screen.findByTestId("project-not-found");
    expect(notFound).toHaveTextContent('Project "mesh-dev" not found');
    expect(screen.queryByTestId("project-stats-page")).toBeNull();
    expect(document.querySelector('[aria-busy="true"]')).toBeNull();
  });

  it("a viewer with no projects at all gets the same answer", async () => {
    useProjectStore.setState({
      projects: [],
      currentProject: null,
      loaded: true,
      loadedWorkspaceId: "ws-a",
      isLoading: false,
      fetchProjects: vi.fn().mockResolvedValue(undefined),
    });
    renderAt("/w/acme/p/mesh-dev/stats");
    expect(await screen.findByTestId("project-not-found")).toHaveTextContent('Project "mesh-dev" not found');
  });

  it("while the project list is still loading it waits, and decides only on the answer", async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    useProjectStore.setState({
      projects: [],
      currentProject: null,
      loaded: false,
      loadedWorkspaceId: null,
      isLoading: false,
      fetchProjects: vi.fn(async () => {
        useProjectStore.setState({ isLoading: true });
        await gate;
        useProjectStore.setState({ projects: [KEEP], isLoading: false, loaded: true, loadedWorkspaceId: "ws-a" });
      }),
    });
    renderAt("/w/acme/p/mesh-dev/stats");
    await screen.findByTestId("project-stats-page");
    expect(screen.queryByTestId("project-not-found")).toBeNull();

    await act(async () => release());
    await waitFor(() => expect(screen.getByTestId("project-not-found")).toHaveTextContent('Project "mesh-dev" not found'));
  });

  it("positive control: a project in the viewer's list is not «not found»", async () => {
    useProjectStore.setState({
      projects: [KEEP],
      currentProject: null,
      loaded: true,
      loadedWorkspaceId: "ws-a",
      isLoading: false,
      fetchProjects: vi.fn().mockResolvedValue(undefined),
    });
    renderAt("/w/acme/p/keep/stats");
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Keep"));
    expect(screen.queryByTestId("project-not-found")).toBeNull();
  });

  it("a list answered for another workspace does not decide: no «not found» right after a workspace jump", async () => {
    // The store still holds Acme's answered list; Beta's has not been asked yet.
    useProjectStore.setState({
      projects: [KEEP],
      currentProject: null,
      loaded: true,
      loadedWorkspaceId: "ws-a",
      isLoading: false,
      fetchProjects: vi.fn().mockResolvedValue(undefined),
    });
    renderAt("/w/beta/p/mesh-dev/stats");
    await screen.findByTestId("project-stats-page");
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("project-not-found")).toBeNull();
  });

  it("fetchProjects records which workspace its answer belongs to", async () => {
    const { api } = await import("@/lib/api");
    vi.mocked(api).mockResolvedValueOnce({ items: [KEEP] } as never);
    useProjectStore.setState({ fetchProjects: useProjectStore.getInitialState().fetchProjects });
    await useProjectStore.getState().fetchProjects("ws-b");
    expect(useProjectStore.getState()).toMatchObject({ loaded: true, loadedWorkspaceId: "ws-b", projects: [KEEP] });
  });
});
