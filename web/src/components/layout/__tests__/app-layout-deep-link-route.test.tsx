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
 *
 * `/a/:artifactId` (the artifact's own page, #eb6fde4e) joined the list the
 * same way — caught by the e2e suite in CI, where the page opened Activity
 * instead of the artifact.
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
import { ProjectDeepLinkResolver } from "@/pages/project-deep-link";
import { api } from "@/lib/api";

const WORKSPACE_A = { id: "ws-a", name: "Acme", slug: "acme" } as never;

function renderDeepLink(initialPath: string, resolveProject = false) {
  const router = createMemoryRouter(
    [
      { path: "/login", element: <div data-testid="login-page">Login</div> },
      {
        element: <AppLayout />,
        children: [
          { path: "w/:wsSlug/activity", element: <div data-testid="activity-page">Activity</div> },
          { path: "t/:taskId", element: <div data-testid="task-deep-link">TaskDeepLink</div> },
          { path: "a/:artifactId", element: <div data-testid="artifact-view">ArtifactView</div> },
          { path: "d/:docId", element: <div data-testid="doc-deep-link">DocDeepLink</div> },
          { path: "p/:projectId", element: resolveProject ? <ProjectDeepLinkResolver /> : <div data-testid="project-deep-link">ProjectDeepLink</div> },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  vi.mocked(api).mockReset().mockResolvedValue({});
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
  it("keeps workspace onboarding for ordinary routes with no workspaces", async () => {
    useWorkspaceStore.setState({ workspaces: [] });
    renderDeepLink("/w/acme/activity");
    await screen.findByText("Create your first workspace to get started.");
    expect(screen.queryByTestId("activity-page")).not.toBeInTheDocument();
  });

  it("shows project not found when the authenticated user has no workspaces", async () => {
    useWorkspaceStore.setState({ workspaces: [] });
    vi.mocked(api).mockRejectedValue(new Error("Not found"));
    const path = "/p/00000000-0000-4000-8000-000000000000";
    const router = renderDeepLink(path, true);

    await screen.findByText("Project not found or you don't have access.");
    expect(screen.getByRole("heading", { name: "Project not found" })).toBeVisible();
    expect(router.state.location.pathname).toBe(path);
  });

  it("lets /p/:projectId render the project resolver", async () => {
    const router = renderDeepLink("/p/00000000-0000-4000-8000-000000000000");

    await screen.findByTestId("project-deep-link");
    expect(screen.queryByTestId("activity-page")).not.toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/p/00000000-0000-4000-8000-000000000000");
  });

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

  it("lets /a/:artifactId render its page instead of bouncing to /w/<slug>/activity (#eb6fde4e)", async () => {
    const router = renderDeepLink("/a/some-artifact-id");

    await screen.findByTestId("artifact-view");
    expect(screen.queryByTestId("activity-page")).not.toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/a/some-artifact-id");
  });
});
