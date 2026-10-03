import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { ProjectDeepLinkResolver } from "@/pages/project-deep-link";
import { useAuthStore } from "@/stores/auth";

const project = { id: "project-a", workspace_id: "workspace-b", slug: "renamed-project" };
const workspace = { id: "workspace-b", slug: "renamed-workspace" };
const fetchMock = vi.fn();
function json(data: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(data), { status }));
}
function renderAt(path = "/p/project-a") {
  const router = createMemoryRouter([
    { path: "/p/:projectId", element: <ProjectDeepLinkResolver /> },
    { path: "/w/:wsSlug/p/:projectSlug", element: <div>Project board</div> },
    { path: "/login", element: <div>Login</div> },
  ], { initialEntries: [path] });
  render(<RouterProvider router={router} />);
  return router;
}
beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  useAuthStore.setState({ isAuthenticated: true, isLoading: false });
});

describe("ProjectDeepLinkResolver", () => {
  it("resolves a project using its workspace id and preserves query/hash", async () => {
    fetchMock.mockResolvedValueOnce(await json(project)).mockResolvedValueOnce(await json(workspace));
    const router = renderAt("/p/project-a?view=board#section");
    await screen.findByText("Project board");
    expect(router.state.location.pathname).toBe("/w/renamed-workspace/p/renamed-project");
    expect(router.state.location.search).toBe("?view=board");
    expect(router.state.location.hash).toBe("#section");
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/projects/project-a", "/api/v1/workspaces/workspace-b",
    ]);
    expect(router.state.historyAction).toBe("REPLACE");
  });

  it.each([403, 404])("shows not found for project HTTP %s", async (status) => {
    fetchMock.mockImplementation(() => json({ message: "Unavailable" }, status));
    renderAt();
    await screen.findByText("Project not found or you don't have access.");
    expect(screen.getByRole("heading", { name: "Project not found" })).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("shows not found when the workspace is unavailable", async () => {
    fetchMock.mockResolvedValueOnce(await json(project)).mockResolvedValueOnce(await json({ message: "Forbidden" }, 403));
    renderAt();
    await screen.findByText("Project not found or you don't have access.");
  });

  it("shows an accessible pending state until the request completes", async () => {
    fetchMock.mockReturnValue(new Promise(() => {}));
    renderAt();
    expect(screen.getByRole("status", { name: "Loading project" })).toBeVisible();
    expect(screen.queryByText("Project not found")).not.toBeInTheDocument();
  });

  it("redirects unauthenticated users to login with the full return URL", async () => {
    useAuthStore.setState({ isAuthenticated: false });
    const router = renderAt("/p/project-a?view=board#section");
    await screen.findByText("Login");
    expect(new URLSearchParams(router.state.location.search).get("redirect")).toBe("/p/project-a?view=board#section");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("ignores a late response after navigating to another project id", async () => {
    let complete!: (response: Response) => void;
    fetchMock.mockImplementation((url: string) => {
      if (url === "/api/v1/projects/project-a") return new Promise<Response>(resolve => { complete = resolve; });
      if (url === "/api/v1/projects/project-b") return json({ ...project, id: "project-b", slug: "second" });
      return json(workspace);
    });
    const router = renderAt();
    await act(() => router.navigate("/p/project-b"));
    await screen.findByText("Project board");
    await act(async () => { complete(await json(project)); });
    await waitFor(() => expect(router.state.location.pathname).toBe("/w/renamed-workspace/p/second"));
    expect(fetchMock.mock.calls.filter(([url]) => url === "/api/v1/workspaces/workspace-b")).toHaveLength(1);
  });
});
