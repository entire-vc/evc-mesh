import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ViewTabBar } from "@/components/view-tab-bar";
import { BUILTIN_PROJECT_TABS, projectTabsWith } from "@/lib/project-view-tabs";
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
  return screen.queryByRole("menu");
}

describe("ViewTabBar", () => {
  beforeEach(() => {
    navigate.mockReset();
    localStorage.clear();
  });

  it("shows all six built-in tabs in the strip", () => {
    renderBar("board");
    const strip = screen.getByRole("button", { name: "Board" }).parentElement!;
    const labels = within(strip)
      .getAllByRole("button")
      .map((b) => b.textContent?.trim());
    // One trailing empty: the kebab trigger itself.
    expect(labels).toEqual([
      "Board",
      "List",
      "Dependencies",
      "Calendar",
      "Docs",
      "Updates",
      "",
    ]);
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

  it("navigates to timeline and updates from the strip", () => {
    renderBar("board");
    fireEvent.click(screen.getByRole("button", { name: "Dependencies" }));
    expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/timeline");
    fireEvent.click(screen.getByRole("button", { name: "Updates" }));
    expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/updates");
  });

  it("marks Timeline and Updates active in the strip", () => {
    renderBar("timeline");
    expect(screen.getByRole("button", { name: "Dependencies" })).toHaveAttribute("aria-current", "page");
  });

  it("keeps only the saved-view items in the kebab when every tab fits", () => {
    renderBar("list", "project-1");
    openKebab();
    const items = screen.getAllByRole("menuitem");
    expect(items.map((i) => i.textContent?.trim())).toEqual(["Save current view"]);
    expect(screen.getAllByRole("button", { name: /more views/i })).toHaveLength(1);
  });

  // Docs is not in the server's saved-view enum, so it must not be savable.
  it("hides Save current view on the docs tab", () => {
    renderBar("docs", "project-1");
    openKebab();
    expect(
      screen.queryByRole("menuitem", { name: /save current view/i }),
    ).toBeNull();
  });

  it("marks an extension tab active and offers no Save current view there", () => {
    render(
      <MemoryRouter>
        <ViewTabBar
          currentView="ext:gitlab"
          wsSlug="acme"
          projectSlug="mesh"
          projectId="project-1"
          tabs={projectTabsWith([{ extensionId: "gitlab", title: "GitLab" }])}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole("button", { name: "GitLab" })).toHaveAttribute("aria-current", "page");
    fireEvent.click(screen.getByRole("button", { name: /more views/i }));
    expect(screen.queryByRole("menuitem", { name: /save current view/i })).toBeNull();
  });

  it("hides Save current view on the updates tab", () => {
    renderBar("updates", "project-1");
    openKebab();
    expect(
      screen.queryByRole("menuitem", { name: /save current view/i }),
    ).toBeNull();
  });

  // ---------------------------------------------------------------------
  // Overflow: jsdom has no layout, so tab widths and the strip's room are
  // stubbed. Every tab is 80px; the "…" trigger box is `kebab` px wide (40 by
  // default, 49 once the mobile touch target and its divider are counted), and
  // the strip reserves that plus the trigger's 4px left margin (44 / 53).
  // ---------------------------------------------------------------------

  describe("overflow", () => {
    let room = 0;
    let kebab = 40;
    const offsetWidth = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "offsetWidth",
    );
    const clientWidth = Object.getOwnPropertyDescriptor(
      Element.prototype,
      "clientWidth",
    );

    beforeEach(() => {
      kebab = 40;
      Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
        configurable: true,
        get(this: HTMLElement) {
          return this.classList.contains("border-l") ? kebab : 80;
        },
      });
      Object.defineProperty(Element.prototype, "clientWidth", {
        configurable: true,
        get: () => room,
      });
    });

    afterEach(() => {
      if (offsetWidth)
        Object.defineProperty(HTMLElement.prototype, "offsetWidth", offsetWidth);
      if (clientWidth)
        Object.defineProperty(Element.prototype, "clientWidth", clientWidth);
    });

    const stripLabels = () =>
      screen
        .getAllByRole("button")
        .filter((b) => !/more views/i.test(b.getAttribute("title") ?? ""))
        .map((b) => b.textContent?.trim());

    it("shows every built-in tab when they all fit", () => {
      room = 6 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toEqual(["Board", "List", "Dependencies", "Calendar", "Docs", "Updates"]);
    });

    it("remembers the split for this width, and a stale one is corrected by the measurement", () => {
      room = 5 * 80 + 44;
      const { unmount } = renderBar("board");
      const stored = JSON.parse(localStorage.getItem("mesh.projectTabs.split")!);
      expect(stored[1]).toEqual(["calendar"]);
      expect(stored[0]).toMatch(new RegExp(`\\|${window.innerWidth}$`));
      unmount();

      // Same width, but a viewport that now fits everything: the seed renders
      // first, the measurement then puts Calendar back.
      room = 6 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toContain("Calendar");
    });

    it("moves Calendar into the menu first when the strip is short", () => {
      room = 5 * 80 + 44;
      renderBar("board", "project-1");
      expect(stripLabels()).toEqual(["Board", "List", "Dependencies", "Docs", "Updates"]);
      fireEvent.click(screen.getByRole("button", { name: /more views/i }));
      fireEvent.click(screen.getByRole("menuitem", { name: /calendar/i }));
      expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/calendar");
    });

    it("reserves the measured trigger width, not a fixed 44px", () => {
      // 5 tabs + 44px would fit; with a 53px trigger the next tab must move out.
      kebab = 49;
      room = 5 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toEqual(["Board", "List", "Dependencies", "Docs"]);
    });

    it("drops Calendar, Updates, Docs, Dependencies, then List as the strip narrows", () => {
      room = 2 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toEqual(["Board", "List"]);
      room = 1 * 80 + 44;
    });

    it("keeps Board alone at the narrowest width", () => {
      room = 1 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toEqual(["Board"]);
    });

    it("marks the menu trigger active when the current tab overflowed", () => {
      room = 5 * 80 + 44;
      renderBar("calendar");
      const trigger = screen.getByRole("button", { name: /more views/i });
      expect(trigger.className).toContain("bg-muted");
      fireEvent.click(trigger);
      expect(
        screen.getByRole("menuitem", { name: /calendar/i }),
      ).toHaveAttribute("aria-current", "page");
    });

    it("renders tabs supplied as data, with no edit to the component", () => {
      room = 12 * 80;
      render(
        <MemoryRouter>
          <ViewTabBar
            currentView="board"
            wsSlug="acme"
            projectSlug="mesh"
            tabs={[
              ...BUILTIN_PROJECT_TABS,
              {
                id: "ext",
                labelKey: "viewTabs.tabs.docs",
                Icon: BUILTIN_PROJECT_TABS[0]!.Icon,
                path: (ws, proj) => `/w/${ws}/p/${proj}/x/ext`,
                overflowRank: 5,
              },
            ]}
          />
        </MemoryRouter>,
      );
      expect(stripLabels()).toHaveLength(7);
    });
  });

  it("nests no button inside another button", () => {
    renderBar("board", "project-1");
    openKebab();
    const nested = Array.from(document.querySelectorAll("button button"));
    expect(nested).toEqual([]);
  });
});

describe("projectTabsWith", () => {
  it("adds no tab when no extension screen is enabled", () => {
    expect(projectTabsWith([])).toBe(BUILTIN_PROJECT_TABS);
  });

  it("adds one titled tab per enabled extension screen, routed to its screen", () => {
    const tabs = projectTabsWith([{ extensionId: "gitlab", title: "GitLab" }]);
    expect(tabs).toHaveLength(BUILTIN_PROJECT_TABS.length + 1);
    const ext = tabs[tabs.length - 1]!;
    expect(ext.label).toBe("GitLab");
    expect(ext.path("acme", "mesh")).toBe("/w/acme/p/mesh/x/gitlab");
  });
});
