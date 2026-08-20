import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ViewTabBar } from "@/components/view-tab-bar";
import type { ProjectViewTab } from "@/types";

const navigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>(
    "react-router",
  );
  return { ...actual, useNavigate: () => navigate };
});

const fetchViews = vi.fn();
const createView = vi.fn();
vi.mock("@/stores/saved-view-store", () => ({
  useSavedViewStore: () => ({
    views: [],
    fetchViews,
    createView,
    applyView: vi.fn(),
    updateView: vi.fn(),
    deleteView: vi.fn(),
    currentViewState: { filters: {} },
    requestResetFilters: vi.fn(),
  }),
}));

function renderBar(currentView: ProjectViewTab, projectId?: string) {
  return render(
    <MemoryRouter>
      <ViewTabBar
        currentView={currentView}
        wsSlug="acme"
        projectSlug="mesh"
        projectId={projectId}
      />
    </MemoryRouter>,
  );
}

function openKebab() {
  fireEvent.click(screen.getByRole("button", { name: /more views/i }));
  return screen.getByRole("menuitem", { name: /timeline/i }).parentElement!;
}

describe("ViewTabBar", () => {
  beforeEach(() => {
    navigate.mockReset();
  });

  it("shows Board, List, Calendar and Docs in the strip, not Timeline", () => {
    renderBar("board");
    const strip = screen.getByRole("button", { name: "Board" }).parentElement!;
    const labels = within(strip)
      .getAllByRole("button")
      .map((b) => b.textContent?.trim());
    // Trailing empties are the kebab trigger (DropdownMenuTrigger ignores
    // asChild and wraps the button it is given).
    expect(labels).toEqual(["Board", "List", "Calendar", "Docs", "", ""]);
  });

  it("navigates to the docs route from the Docs tab", () => {
    renderBar("board");
    fireEvent.click(screen.getByRole("button", { name: "Docs" }));
    expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/docs");
  });

  it("marks the Docs tab active on the docs view", () => {
    renderBar("docs");
    expect(screen.getByRole("button", { name: "Docs" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("offers Timeline and Updates under Formats in the single kebab", () => {
    // "list" rather than "board": the board tab adds a Reset filters item.
    renderBar("list", "project-1");
    const menu = openKebab();
    const formats = within(menu).getAllByRole("menuitem");
    expect(formats.map((i) => i.textContent?.trim())).toEqual([
      "Timeline",
      "Updates",
      "Save current view",
    ]);
    // One kebab only.
    expect(screen.getAllByRole("button", { name: /more views/i })).toHaveLength(1);
  });

  it("navigates to timeline and updates from the kebab", () => {
    renderBar("board");
    openKebab();
    fireEvent.click(screen.getByRole("menuitem", { name: /timeline/i }));
    expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/timeline");

    openKebab();
    fireEvent.click(screen.getByRole("menuitem", { name: /updates/i }));
    expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/updates");
  });

  it("marks Timeline active in the kebab when it is the current view", () => {
    renderBar("timeline");
    openKebab();
    expect(
      screen.getByRole("menuitem", { name: /timeline/i }),
    ).toHaveAttribute("aria-current", "page");
  });

  // Docs is not in the server's saved-view enum, so it must not be savable.
  it("hides Save current view on the docs tab", () => {
    renderBar("docs", "project-1");
    openKebab();
    expect(
      screen.queryByRole("menuitem", { name: /save current view/i }),
    ).toBeNull();
  });

  it("keeps the Formats group when there is no project id", () => {
    renderBar("board");
    const menu = openKebab();
    expect(within(menu).getAllByRole("menuitem")).toHaveLength(2);
  });
});
