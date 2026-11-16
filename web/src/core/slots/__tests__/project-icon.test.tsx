import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { Slot } from "../registry";
import "../mesh-default";
import { clearThemeIcons, registerThemeIcons } from "@/core/icons/theme-icons";
import { useThemeStore } from "@/core/themes/store";
import type { NavProject, NavSidebarData } from "../nav-sidebar/types";
import type { TopbarData } from "../nav-topbar/types";
import type { SlotContext } from "../types";

// A project's icon (emoji, icon:<name>, legacy text or empty) is drawn with EntityIcon in the
// expanded menu, on the collapsed rail and in the breadcrumbs, and follows the workspace theme.

const ctx = (slot: string): SlotContext => ({
  slot,
  contractVersion: "1.0.0",
  locale: "en",
  mode: "light",
  isMobile: false,
  workspace: { id: "ws1", slug: "acme", name: "Acme" },
});

const project = (icon: string, over: Partial<NavProject> = {}): NavProject => ({
  id: "p1",
  slug: "lab",
  name: "Lab",
  icon,
  active: false,
  href: "/w/acme/p/lab",
  items: [],
  ...over,
});

const sidebarData = (projects: NavProject[], over: Partial<NavSidebarData> = {}): NavSidebarData => ({
  workspace: { id: "ws1", slug: "acme", name: "Acme" },
  workspaces: [{ id: "ws1", slug: "acme", name: "Acme", href: "/w/acme", current: true }],
  items: [],
  projects: { state: "ready", value: projects },
  collapsed: false,
  isMobile: false,
  workspaceResolved: true,
  archivedProjects: [],
  archivedError: null,
  ...over,
});

function mountSidebar(data: NavSidebarData) {
  return render(
    <MemoryRouter>
      <Slot
        name="nav.sidebar"
        data={data}
        actions={{ toggleCollapsed: vi.fn(), switchWorkspace: vi.fn(), createProject: vi.fn() }}
        themeSettings={{}}
        ctx={ctx("nav.sidebar")}
      />
    </MemoryRouter>,
  );
}

const Block = () => null;
const topbarData = (entityIcon?: string): TopbarData => ({
  breadcrumbs: [
    { kind: "workspace", label: "Acme", href: "/w/acme" },
    { kind: "project", label: "Lab", href: "/w/acme/p/lab", entityIcon, strong: true },
  ],
  user: { id: "u1", name: "U" },
  notifications: { unread: 0 },
  installable: false,
  isMobile: false,
  components: { NotificationBell: Block, UserMenu: Block, ViewTabs: Block, TeamTabs: Block, Search: Block },
});

function ThemeRocket({ className }: { className?: string }) {
  return <svg data-testid="theme-rocket" className={className} />;
}

const projectLink = () => screen.getByRole("link", { name: /Lab/ });

beforeEach(() => {
  clearThemeIcons("graphite-test");
  useThemeStore.getState().set({ viewsThemeId: "mesh-default" });
});

describe("project icon in the expanded menu", () => {
  it("empty: the FolderKanban default", () => {
    mountSidebar(sidebarData([project("")]));
    expect(projectLink().querySelector("svg.lucide-folder-kanban")).not.toBeNull();
  });

  it("emoji and legacy text are drawn as they are", () => {
    const { unmount } = mountSidebar(sidebarData([project("🚀")]));
    expect(within(projectLink()).getByRole("img", { name: "🚀" })).toBeTruthy();
    unmount();
    mountSidebar(sidebarData([project("OLD")]));
    expect(within(projectLink()).getByText("OLD")).toBeTruthy();
  });

  it("icon:<name> draws the core icon, then the theme's own one after a theme switch, without remount", async () => {
    registerThemeIcons("graphite-test", { rocket: ThemeRocket }, {});
    mountSidebar(sidebarData([project("icon:rocket")]));
    await waitFor(() => expect(projectLink().querySelector("svg.lucide-rocket")).not.toBeNull());
    act(() => useThemeStore.getState().set({ viewsThemeId: "graphite-test" }));
    expect(within(projectLink()).getByTestId("theme-rocket")).toBeTruthy();
    expect(projectLink().querySelector("svg.lucide-rocket")).toBeNull();
  });

  it("archived projects use the same renderer", () => {
    render(
      <MemoryRouter>
        <Slot
          name="nav.sidebar"
          data={sidebarData([], { archivedProjects: [project("🧪", { id: "p2", name: "Old", slug: "old", href: "/w/acme/p/old" })] })}
          actions={{ toggleCollapsed: vi.fn(), switchWorkspace: vi.fn(), createProject: vi.fn() }}
          themeSettings={{}}
          ctx={ctx("nav.sidebar")}
        />
      </MemoryRouter>,
    );
    act(() => screen.getByRole("button", { name: /archived/i }).click());
    expect(within(screen.getByRole("link", { name: /Old/ })).getByRole("img", { name: "🧪" })).toBeTruthy();
  });
});

describe("project icon on the collapsed rail", () => {
  const railTile = () => screen.getByTestId("project-icon");

  it("empty value or unknown name: the first letter", async () => {
    const { unmount } = mountSidebar(sidebarData([project("")], { collapsed: true }));
    expect(railTile()).toHaveTextContent("L");
    unmount();
    mountSidebar(sidebarData([project("icon:no-such-icon-xyz")], { collapsed: true }));
    await act(async () => {
      await import("@/core/icons/core-loader").then((m) => m.loadCoreIcons());
    });
    expect(railTile()).toHaveTextContent("L");
  });

  it("icon:<name> draws the icon and follows the theme", async () => {
    registerThemeIcons("graphite-test", { rocket: ThemeRocket }, {});
    mountSidebar(sidebarData([project("icon:rocket")], { collapsed: true }));
    await waitFor(() => expect(railTile().querySelector("svg.lucide-rocket")).not.toBeNull());
    expect(railTile()).not.toHaveTextContent("L");
    act(() => useThemeStore.getState().set({ viewsThemeId: "graphite-test" }));
    expect(within(railTile()).getByTestId("theme-rocket")).toBeTruthy();
  });

  it("emoji is drawn in the tile", () => {
    mountSidebar(sidebarData([project("🚀")], { collapsed: true }));
    expect(within(railTile()).getByRole("img", { name: "🚀" })).toBeTruthy();
  });
});

describe("project icon in the breadcrumbs", () => {
  function mountTopbar(entityIcon?: string) {
    return render(
      <MemoryRouter>
        <Slot name="nav.topbar" data={topbarData(entityIcon)} actions={{ toggleSidebar: vi.fn() }} themeSettings={{}} ctx={ctx("nav.topbar")} />
      </MemoryRouter>,
    );
  }

  it("draws the project's icon before its name; nothing extra when it has none", async () => {
    const { unmount } = mountTopbar("icon:rocket");
    const crumb = screen.getByRole("link", { name: /Lab/ });
    await waitFor(() => expect(crumb.querySelector("svg.lucide-rocket")).not.toBeNull());
    unmount();
    mountTopbar(undefined);
    expect(screen.getByRole("link", { name: "Lab" }).querySelector("svg")).toBeNull();
  });
});
