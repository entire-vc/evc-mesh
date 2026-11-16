import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), api: apiMock }));

import { Slot } from "../registry";
import "../mesh-default";
import { useDashboardsStore } from "@/stores/dashboards";
import { applyOrder, menuGroup, type Dashboard } from "@/lib/dashboards";
import type { NavItem, NavSidebarActions, NavSidebarData } from "../nav-sidebar/types";
import { DashboardGroupRows, DashboardRailTile } from "../nav-sidebar/dashboard-nav";

// Criterion 1 of the menu card: a drag inside a group and between groups, by mouse and by keyboard, ends in
// `PUT /workspaces/:ws/dashboards/order` with the expected body. The chain under test is the real one:
// view → DnD primitive → `reorderDashboards` action → store → API client; only the HTTP boundary is mocked.

const dash = (id: string, placement: "top" | "bottom", position: number, over: Partial<Dashboard> = {}): Dashboard => ({
  id,
  name: id.toUpperCase(),
  icon: "",
  placement,
  position,
  is_default: false,
  virtual: false,
  shared: false,
  hidden: false,
  can_edit: true,
  can_manage: true,
  ...over,
});

const LIST = [dash("a", "top", 0), dash("b", "top", 1), dash("c", "top", 2), dash("x", "bottom", 0), dash("y", "bottom", 1)];
// A shared dashboard I hid: it is not in the menu, so it must not be in the order body either (server: exactly the menu).
const HIDDEN = dash("h", "bottom", 2, { shared: true, hidden: true });

function navItems(list: Dashboard[]): NavItem[] {
  const mk = (d: Dashboard, g: "top" | "bottom"): NavItem => ({
    id: `dash:${d.id}`,
    group: g === "top" ? "dashboards-top" : "dashboards-bottom",
    icon: "LayoutDashboard",
    label: d.name,
    href: `/w/acme/d/${d.id}`,
    active: false,
    origin: "core",
    inRail: true,
    dashboard: { id: d.id, emoji: d.icon, shared: d.shared, isDefault: false, virtual: false, canEdit: d.can_edit, canManage: d.can_manage, canDelete: list.length > 1 },
  });
  return [...menuGroup(list, "top").map((d) => mk(d, "top")), ...menuGroup(list, "bottom").map((d) => mk(d, "bottom"))];
}

function Menu({ state = "ready" }: { state?: NavSidebarData["dashboardsState"] }) {
  const list = useDashboardsStore((s) => s.items);
  const reorder = useDashboardsStore((s) => s.reorder);
  const data: NavSidebarData = {
    workspace: { id: "ws1", slug: "acme", name: "Acme" },
    workspaces: [],
    items: navItems(list),
    projects: { state: "ready", value: [] },
    collapsed: false,
    isMobile: false,
    workspaceResolved: true,
    archivedProjects: [],
    archivedError: null,
    dashboardsState: state,
    allDashboardsHref: "/w/acme/dashboards",
  };
  const actions: NavSidebarActions = { toggleCollapsed: vi.fn(), switchWorkspace: vi.fn(), reorderDashboards: (o) => reorder(o).catch(() => {}) };
  return (
    <MemoryRouter>
      <Slot
        name="nav.sidebar"
        data={data}
        actions={actions}
        themeSettings={{}}
        ctx={{ slot: "nav.sidebar", contractVersion: "1.0.0", locale: "en", mode: "light", isMobile: false, workspace: data.workspace }}
      />
    </MemoryRouter>
  );
}

// jsdom has no layout: rows stack inside their group; the groups sit one under the other.
function stubLayout() {
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const el = this as HTMLElement;
    const rect = (x: number, y: number, w: number, h: number) =>
      ({ x, y, left: x, top: y, width: w, height: h, right: x + w, bottom: y + h, toJSON() {} }) as DOMRect;
    const zoneTop = (z: string | null | undefined) => (z === "dashboard-group:bottom" ? 400 : 0);
    if (el.hasAttribute("data-mesh-dropzone")) return rect(0, zoneTop(el.getAttribute("data-mesh-dropzone")), 200, 300);
    const row = el.closest("[data-mesh-draggable]") as HTMLElement | null;
    if (row) {
      const zone = row.closest("[data-mesh-dropzone]")!;
      const idx = [...zone.querySelectorAll("[data-mesh-draggable]")].indexOf(row);
      return rect(0, zoneTop(zone.getAttribute("data-mesh-dropzone")) + 10 + idx * 40, 200, 36);
    }
    return rect(0, 0, 0, 0);
  });
}

const menuOnly = () => useDashboardsStore.getState().items;
const tick = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });
const lastPut = () => {
  const call = [...apiMock.mock.calls].reverse().find((c: unknown[]) => (c[1] as { method?: string } | undefined)?.method === "PUT");
  return call ? { path: call[0] as string, body: call[1].body as unknown } : null;
};

// The drag layer loads after the menu renders (it is not in the shell's first chunk): wait until rows are draggable.
const renderMenu = async () => {
  render(<Menu />);
  await waitFor(() => expect(document.querySelector("[data-mesh-draggable]")).not.toBeNull());
};

// The grab area is the Handle span (the focusable one); the labelled icon sits inside it.
const handleOf = (id: string) => within(screen.getByTestId(`dashboard-row-${id}`)).getByLabelText(`Reorder ${id.toUpperCase()}`).parentElement!;

beforeEach(() => {
  apiMock.mockReset();
  // The HTTP boundary behaves like the server: the order answer is the new menu list (hidden ones are not in it).
  apiMock.mockImplementation(async (path: string, opts?: { method?: string; body?: { top: string[]; bottom: string[] } }) => {
    if (opts?.method === "PUT" && path.endsWith("/dashboards/order")) {
      return { dashboards: applyOrder(menuOnly(), opts.body!).filter((d) => !d.hidden) };
    }
    return opts?.method ? {} : { dashboards: menuOnly() };
  });
  useDashboardsStore.getState().clear(); // answers of the previous test still in flight are dropped
  useDashboardsStore.setState({ workspaceId: "ws1", items: [...LIST, HIDDEN], status: "ready", error: null });
});
afterEach(async () => {
  // After a pointer drop dnd-kit keeps a click-blocking window listener for a short while; let it expire so the
  // next test's first click is not swallowed.
  await new Promise((r) => setTimeout(r, 100));
  vi.restoreAllMocks();
});

describe("dashboard menu: reorder by keyboard", () => {
  it("Space, ArrowDown, Space on the first of the top group moves it one place down inside the group", async () => {
    stubLayout();
    await renderMenu();
    const h = handleOf("a");
    h.focus();
    fireEvent.keyDown(h, { code: "Space", key: " " });
    await tick();
    fireEvent.keyDown(document, { code: "ArrowDown", key: "ArrowDown" });
    await tick();
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await tick();
    await waitFor(() => expect(lastPut()).not.toBeNull());
    expect(lastPut()).toEqual({ path: "/api/v1/workspaces/ws1/dashboards/order", body: { top: ["b", "a", "c"], bottom: ["x", "y"] } });
  });

  it("the item menu buttons reorder too: «Move down» is the same PUT, «Move below projects» crosses groups", async () => {
    await renderMenu();
    fireEvent.click(screen.getByLabelText("Actions for B"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Move down" }));
    await waitFor(() => expect(lastPut()).not.toBeNull());
    expect(lastPut()!.body).toEqual({ top: ["a", "c", "b"], bottom: ["x", "y"] });

    fireEvent.click(screen.getByLabelText("Actions for A"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Move below projects" }));
    await waitFor(() => expect(lastPut()!.body).toEqual({ top: ["c", "b"], bottom: ["x", "y", "a"] }));
  });
});

describe("dashboard menu: the order body is exactly the menu", () => {
  it("a hidden shared dashboard is not sent, and it is still in the store after the save", async () => {
    await renderMenu();
    fireEvent.click(screen.getByLabelText("Actions for B"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Move down" }));
    await waitFor(() => expect(lastPut()).not.toBeNull());
    expect(lastPut()!.body).toEqual({ top: ["a", "c", "b"], bottom: ["x", "y"] });
    await waitFor(() => expect(menuGroup(useDashboardsStore.getState().items, "top").map((d) => d.id)).toEqual(["a", "c", "b"]));
    expect(useDashboardsStore.getState().items.find((d) => d.id === "h")?.hidden).toBe(true);
  });
});

describe("dashboard menu: reorder by keyboard between groups", () => {
  it("Space, three ArrowDown, Space moves the last top item into the bottom group", async () => {
    stubLayout();
    await renderMenu();
    const h = handleOf("c");
    h.focus();
    fireEvent.keyDown(h, { code: "Space", key: " " });
    await tick();
    for (let i = 0; i < 3; i++) {
      fireEvent.keyDown(document, { code: "ArrowDown", key: "ArrowDown" });
      await tick();
    }
    fireEvent.keyDown(document, { code: "Space", key: " " });
    await tick();
    await waitFor(() => expect(lastPut()).not.toBeNull());
    expect(lastPut()!.body).toEqual({ top: ["a", "b"], bottom: ["c", "x", "y"] });
  });
});

describe("dashboard menu: reorder by mouse", () => {
  it("dragging the handle of the first top item below the second one reorders inside the group", async () => {
    stubLayout();
    await renderMenu();
    const h = handleOf("a");
    fireEvent.pointerDown(h, { button: 0, isPrimary: true, pointerId: 1, clientX: 5, clientY: 28 });
    fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: 5, clientY: 40 });
    fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: 5, clientY: 130 });
    await tick();
    fireEvent.pointerUp(document, { isPrimary: true, pointerId: 1, clientX: 5, clientY: 130 });
    await waitFor(() => expect(lastPut()).not.toBeNull());
    expect(lastPut()).toEqual({ path: "/api/v1/workspaces/ws1/dashboards/order", body: { top: ["b", "c", "a"], bottom: ["x", "y"] } });
  });

  it("dragging the handle of the last top item onto the first bottom item moves it between groups", async () => {
    stubLayout();
    await renderMenu();
    const h = handleOf("c");
    fireEvent.pointerDown(h, { button: 0, isPrimary: true, pointerId: 1, clientX: 5, clientY: 90 });
    fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: 5, clientY: 300 });
    fireEvent.pointerMove(document, { isPrimary: true, pointerId: 1, clientX: 5, clientY: 412 });
    await tick();
    fireEvent.pointerUp(document, { isPrimary: true, pointerId: 1, clientX: 5, clientY: 412 });
    await waitFor(() => expect(lastPut()).not.toBeNull());
    const body = lastPut()!.body as { top: string[]; bottom: string[] };
    expect(body.top).toEqual(["a", "b"]);
    expect(body.bottom).toContain("c");
    expect([...body.bottom].sort()).toEqual(["c", "x", "y"]);
  });
});

describe("dashboard menu: a refused save rolls back", () => {
  it("no dashboards on screen: no drag container and no second live region (the board's stays the only one)", async () => {
    render(<Menu state="loading" />);
    await tick();
    await tick();
    expect(document.querySelector("[aria-live]")).toBeNull();
    expect(document.querySelector("[data-mesh-draggable]")).toBeNull();
  });

  it("the menu returns to the old order when the server says 422", async () => {
    apiMock.mockRejectedValueOnce(new Error("order mismatch"));
    await renderMenu();
    fireEvent.click(screen.getByLabelText("Actions for B"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Move down" }));
    await waitFor(() => expect(apiMock).toHaveBeenCalled());
    await waitFor(() => expect(menuGroup(useDashboardsStore.getState().items, "top").map((d) => d.id)).toEqual(["a", "b", "c"]));
  });
});

// perf `board.open.dom_mutations` counts every MutationObserver record: leaving the dashboard for a board must touch
// one attribute of its row (`aria-current`); the active look comes from that attribute, not from a second class swap.
describe("dashboard row: leaving it", () => {
  const records = async (ui: (active: boolean) => React.ReactElement) => {
    const { rerender, container } = render(ui(true));
    const link = container.querySelector('a[aria-current="page"]')!;
    expect(link).not.toBeNull();
    const seen: string[] = [];
    const mo = new MutationObserver((rs) => rs.forEach((r) => seen.push(r.attributeName ?? r.type)));
    mo.observe(link, { attributes: true, childList: true, subtree: true });
    rerender(ui(false));
    await tick();
    mo.disconnect();
    return seen;
  };
  const actions: NavSidebarActions = { toggleCollapsed: vi.fn(), switchWorkspace: vi.fn() };
  const item = (active: boolean): NavItem => ({ ...navItems(LIST)[0]!, active });

  it("menu row: one record, aria-current", async () => {
    const seen = await records((active) => (
      <MemoryRouter>
        <DashboardGroupRows group="top" title="T" lists={{ top: [item(active)], bottom: [] }} actions={actions} />
      </MemoryRouter>
    ));
    expect(seen).toEqual(["aria-current"]);
  });

  it("rail tile: one record, aria-current", async () => {
    const seen = await records((active) => (
      <MemoryRouter>
        <DashboardRailTile item={item(active)} />
      </MemoryRouter>
    ));
    expect(seen).toEqual(["aria-current"]);
  });
});
