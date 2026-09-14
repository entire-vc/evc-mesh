/**
 * Regression for the follow-up to #14db79fd (task #b1f0ef72): a document
 * deep link (`/d/:docId`, returned in `Document.url` since #14db79fd) opened
 * the activity feed instead of the document. Root cause: AppLayout's
 * `isDeepLinkRoute` escape hatch (app-layout.tsx) — the one thing that stops
 * "no :wsSlug yet" from being read as "go to the first workspace's activity
 * page" — only listed `/t/` and `/tasks/` (the task deep link). `/d/` was
 * added later (document-deep-link.tsx) without updating this check, so
 * AppLayout redirected to `/w/<slug>/activity` before DocumentDeepLinkResolver
 * ever got to mount and resolve the real target.
 *
 * These tests pin both the fixed case (`/d/`) and the case that already
 * worked (`/t/`) as a positive control — a change that "fixed" `/d/` by
 * accident while breaking `/t/` would be just as wrong.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
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

function renderDeepLink(initialPath: string) {
  const router = createMemoryRouter(
    [
      { path: "/login", element: <div data-testid="login-page">Login</div> },
      {
        element: <AppLayout />,
        children: [
          { path: "w/:wsSlug/activity", element: <div data-testid="activity-page">Activity</div> },
          { path: "t/:taskId", element: <div data-testid="task-deep-link">TaskDeepLink</div> },
          { path: "d/:docId", element: <div data-testid="doc-deep-link">DocDeepLink</div> },
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
  useWorkspaceStore.setState({
    workspaces: [WORKSPACE_A],
    currentWorkspace: null,
    fetchWorkspaces: vi.fn().mockResolvedValue(undefined),
  });
  useProjectStore.setState({
    projects: [],
    currentProject: null,
    fetchProjects: vi.fn().mockResolvedValue(undefined),
  });
});

describe("AppLayout — deep-link routes are not redirected to activity", () => {
  it("lets /d/:docId render its own resolver instead of bouncing to /w/<slug>/activity (the bug)", async () => {
    const router = renderDeepLink("/d/some-doc-id");

    await screen.findByTestId("doc-deep-link");
    expect(screen.queryByTestId("activity-page")).not.toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/d/some-doc-id");
  });

  it("positive control: /t/:taskId already worked and must keep working", async () => {
    const router = renderDeepLink("/t/some-task-id");

    await screen.findByTestId("task-deep-link");
    expect(screen.queryByTestId("activity-page")).not.toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/t/some-task-id");
  });
});
