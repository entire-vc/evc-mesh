/**
 * #ddd219f4 — an archived project stayed in the sidebar's Projects list as if
 * nothing had happened. The store keeps archived projects (lookups by id still
 * need their names); the sidebar must list only live ones and move the rest
 * under "Archived", with an Unarchive action that hits the dedicated route.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
vi.mock("@/lib/mentions/inbox", () => ({
  fetchUnseenMentionCount: vi.fn().mockResolvedValue(0),
}));

import { api } from "@/lib/api";
import { Sidebar } from "@/components/layout/sidebar";
import { useProjectStore } from "@/stores/project";
import type { Project } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

function project(id: string, name: string, isArchived: boolean): Project {
  return {
    id,
    workspace_id: "ws-1",
    name,
    slug: name.toLowerCase().replace(/[^a-z0-9]+/g, "-"),
    description: "",
    icon: "",
    settings: {},
    default_assignee_type: "none",
    is_archived: isArchived,
    created_at: "2026-09-23T00:00:00Z",
    updated_at: "2026-09-23T00:00:00Z",
  } as Project;
}

function renderSidebar() {
  const router = createMemoryRouter(
    [{ path: "/w/:wsSlug/*", element: <Sidebar collapsed={false} /> }],
    { initialEntries: ["/w/acme/dashboard"] },
  );
  return render(<RouterProvider router={router} />);
}

describe("Sidebar — archived projects", () => {
  beforeEach(() => {
    mockedApi.mockReset();
    mockedApi.mockResolvedValue({});
    useProjectStore.setState({
      projects: [
        project("p-live", "Live Project", false),
        project("p-arch", "r5-ac4-live-probe", true),
      ],
      currentProject: null,
    });
  });
  afterEach(() => vi.clearAllMocks());

  it("keeps archived projects out of the Projects list and under Archived", () => {
    renderSidebar();

    expect(screen.getByText("Live Project")).toBeTruthy();
    expect(screen.queryByText("r5-ac4-live-probe")).toBeNull();

    const archived = screen.getByTestId("archived-projects");
    fireEvent.click(within(archived).getByRole("button", { name: /Archived \(1\)/ }));
    expect(within(archived).getByText("r5-ac4-live-probe")).toBeTruthy();
    expect(within(archived).queryByText("Live Project")).toBeNull();
  });

  it("Unarchive posts to the unarchive route and moves the project back", async () => {
    mockedApi.mockImplementation(async (url: string) => {
      if (url === "/api/v1/projects/p-arch/unarchive") {
        return project("p-arch", "r5-ac4-live-probe", false);
      }
      return {};
    });
    renderSidebar();

    const archived = screen.getByTestId("archived-projects");
    fireEvent.click(within(archived).getByRole("button", { name: /Archived/ }));
    fireEvent.click(screen.getByRole("button", { name: "Unarchive r5-ac4-live-probe" }));

    await waitFor(() => {
      expect(screen.queryByTestId("archived-projects")).toBeNull();
    });
    expect(mockedApi).toHaveBeenCalledWith("/api/v1/projects/p-arch/unarchive", { method: "POST" });
    expect(screen.getByText("r5-ac4-live-probe")).toBeTruthy();
  });
});
