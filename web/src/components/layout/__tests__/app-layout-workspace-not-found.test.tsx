/**
 * Task #f0359288 (parent #93644be7, subtask 3 of 5): workspace deletion now
 * frees the slug for reuse (rename-on-delete, #c164a5df), so an old
 * bookmark/deep-link to a deleted workspace's slug ("/w/<slug>/...") stops
 * matching anything in the live workspace list. AppLayout's slug-resolution
 * effect (app-layout.tsx) is the ONE place `:wsSlug` gets resolved for every
 * nested route (App.tsx nests all `w/:wsSlug/*` routes under a single
 * <AppLayout/>) — before this task it silently left `currentWorkspace`
 * whatever it was already set to and rendered the ordinary shell (sidebar +
 * whatever page) around it, so a stale link either showed blank content (no
 * currentWorkspace yet) or, worse, a DIFFERENT still-current workspace's
 * data under the dead slug's URL.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(() => Promise.resolve({})),
  getAccessToken: vi.fn(() => null),
}));

import { AppLayout } from "@/components/layout/app-layout";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import { useProjectStore } from "@/stores/project";

const WORKSPACE_A = { id: "ws-a", name: "Acme", slug: "acme" } as never;
const WORKSPACE_B = { id: "ws-b", name: "Bravo", slug: "bravo" } as never;

function renderWithRoute(initialPath: string) {
  const router = createMemoryRouter(
    [
      { path: "/login", element: <div data-testid="login-page">Login</div> },
      {
        element: <AppLayout />,
        children: [
          { path: "w/:wsSlug/activity", element: <div data-testid="activity-page">Activity</div> },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  // jsdom has no matchMedia; app-layout.tsx's unconditional hooks need it
  // regardless of which render branch is ultimately taken.
  window.matchMedia = vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }) as unknown as typeof window.matchMedia;

  useAuthStore.setState({
    isAuthenticated: true,
    isLoading: false,
    user: { id: "u1" } as never,
  });
  useProjectStore.setState({
    projects: [],
    currentProject: null,
    fetchProjects: vi.fn().mockResolvedValue(undefined),
  });
});

describe("AppLayout — deleted/unknown workspace slug", () => {
  it("shows an explicit not-found screen for a slug that isn't in the live workspace list, not a blank shell", async () => {
    useWorkspaceStore.setState({
      workspaces: [WORKSPACE_A],
      currentWorkspace: null,
      fetchWorkspaces: vi.fn().mockResolvedValue(undefined),
    });

    renderWithRoute("/w/deleted-ws/activity");

    await screen.findByText("Workspace not found");
    expect(screen.getByText(/deleted-ws/)).toBeInTheDocument();
    expect(screen.queryByTestId("activity-page")).not.toBeInTheDocument();
    // Not the generic "create your first workspace" screen either — that's
    // a different state (zero workspaces at all), and showing it here would
    // misleadingly suggest the account has no workspace, when in fact it
    // has ACME and just followed a dead link.
    expect(screen.queryByText(/Create your first workspace/i)).not.toBeInTheDocument();
  });

  it("does NOT keep showing a previously-current workspace's page when navigating to a different, dead slug (the actual bug: stale content under a foreign URL, not just a blank one)", async () => {
    useWorkspaceStore.setState({
      workspaces: [WORKSPACE_A],
      // Simulates the user having already been inside workspace A before
      // this navigation — the exact state that let the pre-fix code render
      // A's page under a URL naming a completely different, dead slug.
      currentWorkspace: WORKSPACE_A,
      fetchWorkspaces: vi.fn().mockResolvedValue(undefined),
    });

    renderWithRoute("/w/deleted-ws/activity");

    await screen.findByText("Workspace not found");
    expect(screen.queryByTestId("activity-page")).not.toBeInTheDocument();

    await waitFor(() => {
      expect(useWorkspaceStore.getState().currentWorkspace).toBeNull();
    });
  });

  it("positive control: a slug that IS live renders its page normally, not the not-found screen", async () => {
    useWorkspaceStore.setState({
      workspaces: [WORKSPACE_A, WORKSPACE_B],
      currentWorkspace: null,
      fetchWorkspaces: vi.fn().mockResolvedValue(undefined),
    });

    renderWithRoute("/w/bravo/activity");

    await waitFor(() => {
      expect(screen.getByTestId("activity-page")).toBeInTheDocument();
    });
    expect(screen.queryByText("Workspace not found")).not.toBeInTheDocument();
  });
});
