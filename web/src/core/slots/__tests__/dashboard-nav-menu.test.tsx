import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { Slot } from "../registry";
import "../mesh-default";
import { normalizeNavGroups } from "../nav-sidebar/groups";
import type { NavDashboard, NavItem, NavSidebarActions, NavSidebarData } from "../nav-sidebar/types";

const di = (id: string, group: "top" | "bottom", over: Partial<NavDashboard> = {}, item: Partial<NavItem> = {}): NavItem => ({
  id: `dash:${id}`,
  group: group === "top" ? "dashboards-top" : "dashboards-bottom",
  icon: "LayoutDashboard",
  label: id.toUpperCase(),
  href: `/w/acme/d/${id}`,
  active: false,
  origin: "core",
  inRail: true,
  dashboard: { id, emoji: "", shared: false, isDefault: false, virtual: false, canEdit: true, canManage: true, canDelete: true, ...over },
  ...item,
});

const data = (items: NavItem[], collapsed = false, over: Partial<NavSidebarData> = {}): NavSidebarData => ({
  workspace: { id: "ws1", slug: "acme", name: "Acme" },
  workspaces: [],
  items,
  projects: { state: "ready", value: [{ id: "p1", slug: "lab", name: "Lab", icon: "", active: false, href: "/w/acme/p/lab", items: [] }] },
  collapsed,
  isMobile: false,
  workspaceResolved: true,
  archivedProjects: [],
  archivedError: null,
  dashboardsState: "ready",
  allDashboardsHref: "/w/acme/dashboards",
  ...over,
});

const allActions = (): NavSidebarActions => ({
  toggleCollapsed: vi.fn(),
  switchWorkspace: vi.fn(),
  reorderDashboards: vi.fn().mockResolvedValue(undefined),
  createDashboard: vi.fn(),
  renameDashboard: vi.fn(),
  changeDashboardIcon: vi.fn(),
  duplicateDashboard: vi.fn(),
  shareDashboard: vi.fn(),
  resetDashboard: vi.fn(),
  deleteDashboard: vi.fn(),
  hideDashboard: vi.fn(),
});

function mount(d: NavSidebarData, actions = allActions()) {
  render(
    <MemoryRouter>
      <Slot name="nav.sidebar" data={d} actions={actions} themeSettings={{}} ctx={{ slot: "nav.sidebar", contractVersion: "1.0.0", locale: "en", mode: "light", isMobile: false, workspace: d.workspace }} />
    </MemoryRouter>,
  );
  return actions;
}
const menuNames = () => within(screen.getByTestId("dashboard-menu")).getAllByRole("menuitem").map((e) => e.textContent);

describe("dashboard groups around the projects", () => {
  it("top group above the projects, bottom group below them, «All dashboards» closes the bottom one", () => {
    mount(data([di("a", "top"), di("x", "bottom")]));
    const nav = document.querySelector("nav")!;
    const top = within(nav).getByTestId("dashboards-top");
    const bottom = within(nav).getByTestId("dashboards-bottom");
    const lab = within(nav).getByRole("link", { name: "Lab" });
    expect(top.compareDocumentPosition(lab) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(lab.compareDocumentPosition(bottom) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const all = within(bottom).getByRole("link", { name: "All dashboards" });
    expect(all.getAttribute("href")).toBe("/w/acme/dashboards");
    const links = within(bottom).getAllByRole("link");
    expect(links[links.length - 1]).toBe(all);
  });

  it("each group has its «+», which opens the create dialog for that group", () => {
    const a = mount(data([di("a", "top")]));
    fireEvent.click(screen.getByLabelText("New dashboard above projects"));
    fireEvent.click(screen.getByLabelText("New dashboard below projects"));
    expect(a.createDashboard).toHaveBeenNthCalledWith(1, "top");
    expect(a.createDashboard).toHaveBeenNthCalledWith(2, "bottom");
  });

  it("emoji replaces the icon; a shared dashboard carries the 👥 mark, a personal one does not", () => {
    mount(data([di("a", "top", { emoji: "🚦" }), di("s", "top", { shared: true })]));
    expect(within(screen.getByTestId("dashboard-row-a")).getByText("🚦")).toBeTruthy();
    expect(within(screen.getByTestId("dashboard-row-s")).getByRole("img", { name: "Shared with the workspace" })).toBeTruthy();
    expect(within(screen.getByTestId("dashboard-row-a")).queryByRole("img", { name: "Shared with the workspace" })).toBeNull();
  });

  it("a theme icon value `icon:<name>` is drawn as an icon, never as literal text", () => {
    mount(data([di("a", "top", { emoji: "icon:rocket" })]));
    const row = screen.getByTestId("dashboard-row-a");
    expect(within(row).queryByText("icon:rocket")).toBeNull();
    expect(row.querySelector("svg")).toBeTruthy();
  });

  it("an empty group says so and keeps its «+»", () => {
    mount(data([di("a", "top")]));
    const bottom = screen.getByTestId("dashboards-bottom");
    expect(within(bottom).getByText("Nothing here yet")).toBeTruthy();
    expect(within(bottom).getByLabelText("New dashboard below projects")).toBeTruthy();
  });

  it("the mention counter badge is drawn on the dashboard that carries it", () => {
    mount(data([di("a", "top"), di("b", "top", {}, { badge: { count: 3, tone: "attention" } })]));
    expect(within(screen.getByTestId("dashboard-row-b")).getByLabelText("3 unread")).toBeTruthy();
    expect(within(screen.getByTestId("dashboard-row-a")).queryByLabelText(/unread/)).toBeNull();
  });

  it("while the list is loading no group is drawn (the container keeps the legacy entry)", () => {
    mount(data([], false, { dashboardsState: "loading" }));
    expect(screen.queryByTestId("dashboards-top")).toBeNull();
    expect(screen.queryByTestId("dashboards-bottom")).toBeNull();
  });
});

describe("item menu shows only the allowed actions", () => {
  it("owner of a personal dashboard: rename, icon, duplicate, move, share, delete — no «hide»", () => {
    mount(data([di("a", "top"), di("b", "top")]));
    fireEvent.click(screen.getByLabelText("Actions for B"));
    expect(menuNames()).toEqual(["Rename", "Change icon", "Duplicate", "Move up", "Move down", "Move below projects", "Share with workspace…", "Delete"]);
  });

  it("member of a shared dashboard without edit rights: no rename/icon/sharing/delete; may hide, duplicate and move", () => {
    mount(data([di("s", "top", { shared: true, canEdit: false, canManage: false })]));
    fireEvent.click(screen.getByLabelText("Actions for S"));
    expect(menuNames()).toEqual(["Duplicate", "Move up", "Move down", "Move below projects", "Hide from my menu"]);
  });

  it("can_edit without can_manage (members_can_edit): rename and icon, but no delete and no sharing", () => {
    mount(data([di("s", "top", { shared: true, canEdit: true, canManage: false })]));
    fireEvent.click(screen.getByLabelText("Actions for S"));
    const names = menuNames();
    expect(names).toContain("Rename");
    expect(names).toContain("Change icon");
    expect(names).not.toContain("Delete");
    expect(names).not.toContain("Sharing…");
  });

  it("the default dashboard offers «Reset to default»; the last dashboard cannot be deleted (item disabled, with a hint)", () => {
    const a = mount(data([di("default", "top", { isDefault: true, virtual: true, canDelete: false })]));
    fireEvent.click(screen.getByLabelText("Actions for DEFAULT"));
    expect(menuNames()).toContain("Reset to default");
    const del = screen.getByRole("menuitem", { name: "Delete" });
    expect(del.getAttribute("aria-disabled")).toBe("true");
    expect(del.getAttribute("title")).toBe("The last dashboard cannot be deleted");
    fireEvent.click(del);
    expect(a.deleteDashboard).not.toHaveBeenCalled();
  });

  it("right click opens the same menu; an item runs its action and closes the menu", () => {
    const a = mount(data([di("a", "top")]));
    fireEvent.contextMenu(screen.getByTestId("dashboard-row-a"));
    expect(screen.getByTestId("dashboard-menu")).toBeTruthy();
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(a.renameDashboard).toHaveBeenCalledWith("a");
    expect(screen.queryByTestId("dashboard-menu")).toBeNull();
  });

  it("Escape closes the menu; «Move up» on the first item is disabled", () => {
    const a = mount(data([di("a", "top"), di("b", "top")]));
    fireEvent.click(screen.getByLabelText("Actions for A"));
    expect(screen.getByRole("menuitem", { name: "Move up" }).getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(screen.getByRole("menuitem", { name: "Move up" }));
    expect(a.reorderDashboards).not.toHaveBeenCalled();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByTestId("dashboard-menu")).toBeNull();
  });
});

describe("item menu is drawn above the page, not inside the sidebar", () => {
  // The sidebar is its own stacking context (and a dragged row carries a transform): a fixed panel inside it
  // still sits under the page content, so kanban cards covered the menu. The panel lives in document.body.
  it("«…» menu is mounted outside the sidebar, straight under document.body", () => {
    mount(data([di("a", "top")]));
    fireEvent.click(screen.getByLabelText("Actions for A"));
    const menu = screen.getByTestId("dashboard-menu");
    expect(document.querySelector("nav")!.contains(menu)).toBe(false);
    expect(menu.parentElement).toBe(document.body);
  });

  it("right-click menu is mounted outside the sidebar; a right click inside it does not re-open it elsewhere", () => {
    const a = mount(data([di("a", "top")]));
    fireEvent.contextMenu(screen.getByTestId("dashboard-row-a"), { clientX: 40, clientY: 50 });
    const menu = screen.getByTestId("dashboard-menu");
    expect(document.querySelector("nav")!.contains(menu)).toBe(false);
    const before = menu.style.left + menu.style.top;
    fireEvent.contextMenu(screen.getByRole("menuitem", { name: "Rename" }), { clientX: 300, clientY: 400 });
    expect(screen.getByTestId("dashboard-menu").style.left + screen.getByTestId("dashboard-menu").style.top).toBe(before);
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    expect(a.renameDashboard).toHaveBeenCalledWith("a");
    expect(screen.queryByTestId("dashboard-menu")).toBeNull();
  });

  it("a click outside closes the portal menu, a click inside it does not", () => {
    mount(data([di("a", "top")]));
    fireEvent.click(screen.getByLabelText("Actions for A"));
    fireEvent.mouseDown(screen.getByTestId("dashboard-menu"));
    expect(screen.getByTestId("dashboard-menu")).toBeTruthy();
    fireEvent.mouseDown(document.body);
    expect(screen.queryByTestId("dashboard-menu")).toBeNull();
  });

  it("arrow keys move focus between items inside the portal menu", () => {
    mount(data([di("a", "top"), di("b", "top")]));
    fireEvent.click(screen.getByLabelText("Actions for B"));
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Change icon" }));
    fireEvent.keyDown(document.activeElement!, { key: "ArrowUp" });
    expect(document.activeElement).toBe(screen.getByRole("menuitem", { name: "Rename" }));
  });
});

describe("collapsed rail", () => {
  it("tiles with emoji in the same order and split by group, projects between them", () => {
    mount(data([di("a", "top", { emoji: "🚦" }), di("b", "top"), di("x", "bottom", { emoji: "📈" })], true));
    const nav = document.querySelector("nav")!;
    const links = within(nav).getAllByRole("link").map((l) => l.getAttribute("aria-label"));
    expect(links.slice(0, 5)).toEqual(["A", "B", "Lab", "X", "All dashboards"]);
    expect(within(screen.getByTestId("dashboard-tile-a")).getByText("🚦")).toBeTruthy();
    expect(within(screen.getByTestId("dashboard-tile-x")).getByText("📈")).toBeTruthy();
  });

  it("a shared dashboard's tile names it shared; «All dashboards» does not reuse the dashboard icon", () => {
    mount(data([di("a", "top"), di("x", "bottom", { shared: true })], true));
    expect(screen.getByTestId("dashboard-tile-x").getAttribute("aria-label")).toBe("X · Shared with the workspace");
    expect(screen.getByTestId("dashboard-tile-a").getAttribute("aria-label")).toBe("A");
    const all = screen.getByRole("link", { name: "All dashboards" }).querySelector("svg")!.getAttribute("class");
    const tile = screen.getByTestId("dashboard-tile-a").querySelector("svg")!.getAttribute("class");
    expect(all).toContain("lucide-library");
    expect(tile).not.toContain("lucide-library");
  });
});

describe("a theme view that does not know the dashboard groups", () => {
  it("gets the dashboards in the old `top` group, none dropped", () => {
    const out = normalizeNavGroups([di("a", "top"), di("x", "bottom"), { ...di("t", "top"), group: "tools", dashboard: undefined }], false);
    expect(out.map((i) => i.group)).toEqual(["top", "top", "tools"]);
    expect(normalizeNavGroups([di("a", "top")], true)[0]!.group).toBe("dashboards-top");
  });
});
