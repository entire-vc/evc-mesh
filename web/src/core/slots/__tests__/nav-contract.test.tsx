import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { Slot } from "../registry";
import "../mesh-default";
import type { NavItem, NavSidebarActions, NavSidebarData } from "../nav-sidebar/types";
import type { TopbarActions, TopbarData } from "../nav-topbar/types";
import type { SlotContext } from "../types";

// CT-sidebar-01..08 and CT-topbar-01..05 from the spec «Каталог слотов тем v1»
// (nav.sidebar / nav.topbar), run on the mesh-default views through the same
// `Slot` entry the containers use.

const ctx = (slot: string, over: Partial<SlotContext> = {}): SlotContext => ({
  slot,
  contractVersion: "1.0.0",
  locale: "en",
  mode: "light",
  isMobile: false,
  workspace: { id: "ws1", slug: "acme", name: "Acme" },
  ...over,
});

const item = (id: string, over: Partial<NavItem> = {}): NavItem => ({
  id,
  label: id[0]!.toUpperCase() + id.slice(1),
  icon: "LayoutDashboard",
  href: `/w/acme/${id}`,
  active: false,
  origin: "core",
  group: "top",
  inRail: true,
  ...over,
});

const sidebarData = (over: Partial<NavSidebarData> = {}): NavSidebarData => ({
  workspace: { id: "ws1", slug: "acme", name: "Acme" },
  workspaces: [
    { id: "ws1", slug: "acme", name: "Acme", href: "/w/acme", current: true },
    { id: "ws2", slug: "beta", name: "Beta", href: "/w/beta", current: false },
  ],
  items: [
    item("dashboard", { active: true }),
    item("triage", { group: "inbox", icon: "Inbox" }),
    item("activity", { group: "inbox", icon: "Bell", badge: { count: 3, tone: "attention" } }),
    item("analytics", { group: "tools", icon: "BarChart2" }),
  ],
  projects: { state: "ready", value: [{ id: "p1", slug: "lab", name: "Lab", icon: "", active: false, href: "/w/acme/p/lab", items: [] }] },
  collapsed: false,
  isMobile: false,
  workspaceResolved: true,
  archivedProjects: [],
  archivedError: null,
  ...over,
});

const sidebarActions = (over: Partial<NavSidebarActions> = {}): NavSidebarActions => ({
  toggleCollapsed: vi.fn(),
  switchWorkspace: vi.fn(),
  createProject: vi.fn(),
  ...over,
});

function mountSidebar(data: NavSidebarData, actions = sidebarActions(), c = ctx("nav.sidebar")) {
  render(
    <MemoryRouter>
      <Slot name="nav.sidebar" data={data} actions={actions} themeSettings={{}} ctx={c} />
    </MemoryRouter>,
  );
  return actions;
}

describe("nav.sidebar contract on mesh-default", () => {
  it("CT-sidebar-01 every item is a link with its href; the active one is aria-current; no extras", () => {
    const data = sidebarData();
    mountSidebar(data);
    const nav = document.querySelector("nav")!;
    const links = within(nav).getAllByRole("link");
    // items + the one project link; nothing beyond them.
    expect(links).toHaveLength(data.items.length + 1);
    for (const it of data.items) {
      expect(within(nav).getByRole("link", { name: new RegExp(`^${it.label}`) })).toHaveAttribute("href", it.href);
    }
    const current = links.filter((l) => l.getAttribute("aria-current") === "page");
    expect(current).toHaveLength(1);
    expect(current[0]).toHaveAttribute("href", "/w/acme/dashboard");
  });

  it("CT-sidebar-02 an extension item is drawn like a core one", () => {
    mountSidebar(
      sidebarData({
        items: [item("dashboard"), item("ext:crm", { label: "CRM", icon: "Puzzle", group: "tools", origin: "extension", href: "/w/acme/x/crm" })],
      }),
    );
    const link = screen.getByRole("link", { name: "CRM" });
    expect(link).toHaveAttribute("href", "/w/acme/x/crm");
    expect(link).toHaveAttribute("data-slot-origin", "extension");
  });

  it("CT-sidebar-03 collapsed: every icon link has an accessible name; the toggle works from the keyboard", () => {
    const actions = mountSidebar(sidebarData({ collapsed: true }));
    const rail = document.querySelector("nav")!;
    for (const a of within(rail).getAllByRole("link")) {
      expect(a.getAttribute("aria-label") ?? "").not.toBe("");
    }
    const toggle = screen.getByRole("button", { name: /sidebar/i });
    toggle.focus();
    expect(document.activeElement).toBe(toggle);
    fireEvent.click(toggle);
    expect(actions.toggleCollapsed).toHaveBeenCalledTimes(1);
  });

  it("CT-sidebar-04 a badge shows the count and announces it", () => {
    mountSidebar(sidebarData());
    const badge = screen.getByLabelText("3 unread");
    expect(badge).toHaveTextContent("3");
  });

  it("CT-sidebar-05 switching workspace calls switchWorkspace and marks the current one", async () => {
    const actions = mountSidebar(sidebarData());
    fireEvent.pointerDown(screen.getByRole("button", { name: /Acme/ }), { button: 0, ctrlKey: false });
    fireEvent.click(screen.getByRole("button", { name: /Acme/ }));
    const beta = await screen.findByText("Beta");
    expect(screen.getByText("Acme", { selector: "[role=menuitem]" })).toHaveAttribute("aria-current", "true");
    fireEvent.click(beta);
    expect(actions.switchWorkspace).toHaveBeenCalledWith("beta");
  });

  it("CT-sidebar-06 projects loading shows a skeleton; an error shows the message", () => {
    const { unmount } = render(
      <MemoryRouter>
        <Slot name="nav.sidebar" data={sidebarData({ projects: { state: "loading" } })} actions={sidebarActions()} themeSettings={{}} ctx={ctx("nav.sidebar")} />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("sidebar-projects-loading")).toHaveAttribute("aria-busy", "true");
    unmount();
    const retry = vi.fn();
    mountSidebar(sidebarData({ projects: { state: "error", error: { code: "x", message: "boom" }, retry } }));
    expect(screen.getByRole("alert")).toHaveTextContent("boom");
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
  });

  it("CT-sidebar-07 on mobile the view keeps the Tab order: toggle, switcher, then the links in item order", () => {
    mountSidebar(sidebarData({ isMobile: true }), sidebarActions({ closeMobile: vi.fn() }), ctx("nav.sidebar", { isMobile: true }));
    const order = Array.from(document.querySelectorAll<HTMLElement>("button, a[href]")).map((el) => el.textContent?.trim() || el.getAttribute("aria-label"));
    expect(order[0]).toMatch(/sidebar/i);
    expect(order[1]).toMatch(/Acme/);
    const links = order.filter((x) => ["Dashboard", "Triage", "Activity3", "Analytics"].includes(x ?? ""));
    expect(links).toEqual(["Dashboard", "Triage", "Activity3", "Analytics"]);
  });

  it("CT-sidebar-08 without createProject there is no create button", () => {
    mountSidebar(sidebarData(), sidebarActions({ createProject: undefined }));
    expect(screen.queryByRole("button", { name: /new project/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /New Workspace/i })).toBeNull();
  });
});

const Block = (name: string) => () => <div data-testid={name}>{name}</div>;

const topbarData = (over: Partial<TopbarData> = {}): TopbarData => ({
  breadcrumbs: [
    { kind: "workspace", label: "Acme", href: "/w/acme" },
    { kind: "project", label: "Lab", href: "/w/acme/p/lab" },
    { kind: "page", label: "Board", strong: true },
  ],
  user: { id: "u1", name: "Alex" },
  notifications: { unread: 0 },
  installable: false,
  isMobile: false,
  components: {
    NotificationBell: Block("bell"),
    UserMenu: Block("user-menu"),
    ViewTabs: Block("view-tabs"),
    TeamTabs: () => null,
    Search: Block("search"),
  },
  ...over,
});

const topbarActions = (over: Partial<TopbarActions> = {}): TopbarActions => ({ toggleSidebar: vi.fn(), ...over });

function mountTopbar(data: TopbarData, actions = topbarActions(), c = ctx("nav.topbar")) {
  render(
    <MemoryRouter>
      <Slot name="nav.topbar" data={data} actions={actions} themeSettings={{}} ctx={c} />
    </MemoryRouter>,
  );
  return actions;
}

describe("nav.topbar contract on mesh-default", () => {
  it("CT-topbar-01 three crumbs: the first two are links, the last is aria-current=page", () => {
    mountTopbar(topbarData());
    const nav = screen.getByRole("navigation");
    expect(within(nav).getAllByRole("link")).toHaveLength(2);
    expect(within(nav).getByText("Board")).toHaveAttribute("aria-current", "page");
    expect(within(nav).getByText("Acme")).not.toHaveAttribute("aria-current");
  });

  it("CT-topbar-02 the core blocks appear exactly once", () => {
    mountTopbar(topbarData());
    expect(screen.getAllByTestId("bell")).toHaveLength(1);
    expect(screen.getAllByTestId("user-menu")).toHaveLength(1);
  });

  it("CT-topbar-03 the sidebar toggle is a keyboard-reachable button that calls toggleSidebar", () => {
    const actions = mountTopbar(topbarData());
    const btn = screen.getByRole("button", { name: /sidebar/i });
    btn.focus();
    expect(document.activeElement).toBe(btn);
    fireEvent.click(btn);
    expect(actions.toggleSidebar).toHaveBeenCalledTimes(1);
  });

  it("CT-topbar-04 the install button follows installable and calls install", () => {
    const install = vi.fn();
    const { unmount } = render(
      <MemoryRouter>
        <Slot name="nav.topbar" data={topbarData({ installable: true })} actions={topbarActions({ install })} themeSettings={{}} ctx={ctx("nav.topbar")} />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: /install/i }));
    expect(install).toHaveBeenCalledTimes(1);
    unmount();
    mountTopbar(topbarData({ installable: false }));
    expect(screen.queryByRole("button", { name: /install/i })).toBeNull();
  });

  it("CT-topbar-05 mobile: every action is present and the crumbs truncate instead of widening the page", () => {
    mountTopbar(
      topbarData({
        isMobile: true,
        breadcrumbs: [
          { kind: "workspace", label: "A very long workspace name that must not push the page wide", href: "/w/a" },
          { kind: "project", label: "An equally long project name for the same reason", href: "/w/a/p/b", strong: true },
        ],
      }),
      topbarActions({ install: vi.fn() }),
      ctx("nav.topbar", { isMobile: true }),
    );
    expect(screen.getByRole("button", { name: /sidebar/i })).toBeInTheDocument();
    expect(screen.getByTestId("search")).toBeInTheDocument();
    expect(screen.getByTestId("user-menu")).toBeInTheDocument();
    for (const a of within(screen.getByRole("navigation")).getAllByRole("link")) {
      expect(a.className).toContain("truncate");
    }
  });
});
