import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ViewTabBar } from "@/components/view-tab-bar";
import { BUILTIN_PROJECT_TABS } from "@/lib/project-view-tabs";
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
  return screen.getByRole("menuitem", { name: /dependencies/i }).parentElement!;
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
    // One trailing empty: the kebab trigger itself. There used to be two —
    // DropdownMenuTrigger ignores `asChild` and rendered its own button around
    // the one it was given, which is invalid HTML.
    expect(labels).toEqual(["Board", "List", "Calendar", "Docs", ""]);
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
      "Dependencies",
      "Updates",
      "Save current view",
    ]);
    // One kebab only.
    expect(screen.getAllByRole("button", { name: /more views/i })).toHaveLength(1);
  });

  it("navigates to timeline and updates from the kebab", () => {
    renderBar("board");
    openKebab();
    fireEvent.click(screen.getByRole("menuitem", { name: /dependencies/i }));
    expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/timeline");

    openKebab();
    fireEvent.click(screen.getByRole("menuitem", { name: /updates/i }));
    expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/updates");
  });

  it("marks Timeline active in the kebab when it is the current view", () => {
    renderBar("timeline");
    openKebab();
    expect(
      screen.getByRole("menuitem", { name: /dependencies/i }),
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
      room = 4 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toEqual(["Board", "List", "Calendar", "Docs"]);
    });

    it("moves Calendar into the menu first when the strip is short", () => {
      room = 3 * 80 + 44;
      renderBar("board", "project-1");
      expect(stripLabels()).toEqual(["Board", "List", "Docs"]);
      fireEvent.click(screen.getByRole("button", { name: /more views/i }));
      fireEvent.click(screen.getByRole("menuitem", { name: /calendar/i }));
      expect(navigate).toHaveBeenCalledWith("/w/acme/p/mesh/calendar");
    });

    it("reserves the measured trigger width, not a fixed 44px", () => {
      // 3 tabs + 44px would fit; with a 53px trigger the third tab must move out.
      kebab = 49;
      room = 3 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toEqual(["Board", "List"]);
    });

    it("drops Docs, then List, as the strip narrows further", () => {
      room = 2 * 80 + 44;
      renderBar("board");
      expect(stripLabels()).toEqual(["Board", "List"]);
    });

    it("marks the menu trigger active when the current tab overflowed", () => {
      room = 3 * 80 + 44;
      renderBar("calendar");
      const trigger = screen.getByRole("button", { name: /more views/i });
      expect(trigger.className).toContain("bg-muted");
      fireEvent.click(trigger);
      expect(
        screen.getByRole("menuitem", { name: /calendar/i }),
      ).toHaveAttribute("aria-current", "page");
    });

    it("renders tabs supplied as data, with no edit to the component", () => {
      room = 10 * 80;
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
      expect(stripLabels()).toHaveLength(5);
    });
  });

  it("nests no button inside another button", () => {
    renderBar("board", "project-1");
    openKebab();
    const nested = Array.from(document.querySelectorAll("button button"));
    expect(nested).toEqual([]);
  });
});
