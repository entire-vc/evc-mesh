/**
 * Regression check for task #7893ab16 (App.tsx: BrowserRouter -> data
 * router). AppLayout's unauthenticated-visitor redirect (`<Navigate
 * to="/login..." replace />`, app-layout.tsx:163) is unchanged code — this
 * pins that it still fires the same way once mounted under
 * createBrowserRouter/RouterProvider instead of a declarative
 * <BrowserRouter><Routes>, since that's the one thing this task's migration
 * could plausibly have broken here.
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

function renderProtectedRoute(initialPath: string) {
  const router = createMemoryRouter(
    [
      { path: "/login", element: <div data-testid="login-page">Login</div> },
      {
        element: <AppLayout />,
        children: [
          {
            path: "w/:wsSlug/p/:projectSlug",
            element: <div data-testid="board-page">Board</div>,
          },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  // jsdom has no matchMedia; app-layout.tsx and the useInstallPrompt hook it
  // calls unconditionally both use it before this test's early-return path
  // is even reached (hooks always run regardless of which branch renders).
  window.matchMedia = vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }) as unknown as typeof window.matchMedia;

  useWorkspaceStore.setState({
    workspaces: [],
    currentWorkspace: null,
    fetchWorkspaces: vi.fn().mockResolvedValue(undefined),
  });
  useProjectStore.setState({
    projects: [],
    currentProject: null,
    fetchProjects: vi.fn().mockResolvedValue(undefined),
  });
});

describe("AppLayout — auth redirect under the data router", () => {
  it("sends an unauthenticated visit to a protected route to /login, preserving the deep link", async () => {
    useAuthStore.setState({ isAuthenticated: false, isLoading: false, user: null });

    const router = renderProtectedRoute("/w/acme/p/demo");

    await screen.findByTestId("login-page");
    expect(screen.queryByTestId("board-page")).not.toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
    expect(router.state.location.search).toBe(
      `?redirect=${encodeURIComponent("/w/acme/p/demo")}`,
    );
  });

  it("does not redirect an authenticated visitor away from the protected route", async () => {
    useAuthStore.setState({
      isAuthenticated: true,
      isLoading: false,
      user: { id: "u1" } as never,
    });
    useWorkspaceStore.setState({
      workspaces: [
        { id: "ws-1", name: "Acme", slug: "acme" } as never,
      ],
      currentWorkspace: { id: "ws-1", name: "Acme", slug: "acme" } as never,
    });
    useProjectStore.setState({
      projects: [{ id: "p1", slug: "demo", name: "Demo" } as never],
    });

    renderProtectedRoute("/w/acme/p/demo");

    await waitFor(() => {
      expect(screen.getByTestId("board-page")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("login-page")).not.toBeInTheDocument();
  });
});
