/**
 * Task #06d0c6d8: /p/keep drew Lab's cards. The project pages (board, list,
 * calendar, timeline) read `currentProject` from the store, not the URL, and
 * AppLayout only re-pointed the store in an effect — so after Lab -> Keep the
 * page rendered with Lab as the project (and fetched Lab's tasks) under a Keep
 * URL. The page must never render while the store disagrees with the URL.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(() => Promise.resolve({})),
  getAccessToken: vi.fn(() => null),
}));

import { AppLayout } from "@/components/layout/app-layout";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";
import { useTaskStore } from "@/stores/task";

const WS = { id: "ws-a", name: "Acme", slug: "acme" } as never;
const LAB = { id: "p-lab", name: "Lab", slug: "lab" } as never;
const KEEP = { id: "p-keep", name: "Keep", slug: "keep" } as never;

// What a project page sees on every render: the project it would fetch tasks for.
const seen: Array<string | undefined> = [];
function ProjectPage() {
  const slug = useProjectStore((s) => s.currentProject?.slug);
  seen.push(slug);
  return <div data-testid="project-page">{slug ?? "not-found"}</div>;
}

function renderAt(path: string) {
  const router = createMemoryRouter(
    [
      {
        element: <AppLayout />,
        children: [{ path: "w/:wsSlug/p/:projectSlug", element: <ProjectPage /> }],
      },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  seen.length = 0;
  window.matchMedia = vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }) as unknown as typeof window.matchMedia;
  useAuthStore.setState({ isAuthenticated: true, isLoading: false, user: { id: "u1" } as never });
  useWorkspaceStore.setState({
    workspaces: [WS],
    currentWorkspace: WS,
    fetchWorkspaces: vi.fn().mockResolvedValue(undefined),
  });
});

describe("AppLayout — project comes from the URL", () => {
  it("never renders the project page with the previous project current while projects are still loading (Lab left in the store -> /p/keep)", async () => {
    // Lab stays current from the previous page (a /t/:id deep link sets the
    // task's project; a Lab board does too). The project list of this URL has
    // not arrived yet, so the slug effect cannot re-point the store — and the
    // page used to render and fetch Lab's tasks under the Keep URL.
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    useProjectStore.setState({
      projects: [],
      currentProject: LAB,
      fetchProjects: vi.fn(async () => {
        await gate;
        useProjectStore.setState({ projects: [LAB, KEEP] });
      }),
    });

    renderAt("/w/acme/p/keep");
    await screen.findByTestId("project-switching").catch(() => undefined);
    expect(seen).not.toContain("lab");

    await act(async () => release());
    await waitFor(() => expect(screen.getByTestId("project-page")).toHaveTextContent("keep"));
    expect(seen).not.toContain("lab");
  });

  it("unknown project slug: previous project is dropped, page says not-found instead of drawing Lab", async () => {
    useProjectStore.setState({
      projects: [LAB, KEEP],
      currentProject: LAB,
      fetchProjects: vi.fn().mockResolvedValue(undefined),
    });

    renderAt("/w/acme/p/nope");

    await waitFor(() => expect(screen.getByTestId("project-page")).toHaveTextContent("not-found"));
    expect(seen).not.toContain("lab");
  });

  it("same slug in another workspace is not 'the project of this URL'", async () => {
    const OTHER_WS_KEEP = { id: "p-keep-other", workspace_id: "ws-other", name: "Keep", slug: "keep" } as never;
    const MY_KEEP = { id: "p-keep", workspace_id: "ws-a", name: "Keep", slug: "keep" } as never;
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    useProjectStore.setState({
      projects: [],
      currentProject: OTHER_WS_KEEP,
      fetchProjects: vi.fn(async () => {
        await gate;
        useProjectStore.setState({ projects: [MY_KEEP] });
      }),
    });

    renderAt("/w/acme/p/keep");
    expect(seen).not.toContain("keep");

    await act(async () => release());
    await waitFor(() => expect(screen.getByTestId("project-page")).toHaveTextContent("keep"));
    expect(useProjectStore.getState().currentProject?.id).toBe("p-keep");
  });

  it("previous project's tasks are dropped when the project changes, not shown on the new page's first render", async () => {
    useProjectStore.setState({
      projects: [LAB, KEEP],
      currentProject: null,
      fetchProjects: vi.fn().mockResolvedValue(undefined),
    });
    const router = renderAt("/w/acme/p/lab");
    await waitFor(() => expect(screen.getByTestId("project-page")).toHaveTextContent("lab"));
    useTaskStore.setState({
      tasks: [{ id: "l1", project_id: "p-lab" } as never],
      tasksByStatus: { "lab-prog": [{ id: "l1", project_id: "p-lab" } as never] },
    });

    await act(async () => {
      await router.navigate("/w/acme/p/keep");
    });

    await waitFor(() => expect(screen.getByTestId("project-page")).toHaveTextContent("keep"));
    expect(useTaskStore.getState().tasks).toEqual([]);
    expect(useTaskStore.getState().tasksByStatus).toEqual({});
  });

  it("positive control: URL and store agree -> page renders at once", async () => {
    useProjectStore.setState({
      projects: [LAB, KEEP],
      currentProject: KEEP,
      fetchProjects: vi.fn().mockResolvedValue(undefined),
    });

    renderAt("/w/acme/p/keep");

    await waitFor(() => expect(screen.getByTestId("project-page")).toHaveTextContent("keep"));
  });
});
