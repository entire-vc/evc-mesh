import { act, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router";

// The dashboards list as the server answers it now; a test swaps it to rename or delete.
const server = vi.hoisted(() => ({ list: [] as unknown[] }));

vi.mock("@/lib/api", async (orig) => {
  const real = await orig<typeof import("@/lib/api")>();
  return {
    ...real,
    api: vi.fn(async (url: string) => (url.includes("/dashboards") ? server.list : {})),
    getAccessToken: vi.fn(() => null),
  };
});

import { Header } from "@/components/layout/header";
import { useDashboardsNav } from "@/components/layout/use-dashboards-nav";
import { setLanguage } from "@/i18n";
import { useDashboardsStore } from "@/stores/dashboards";
import { useProjectStore } from "@/stores/project";
import { useWebSocketStore } from "@/stores/websocket";
import { useWorkspaceStore } from "@/stores/workspace";

function dash(id: string, name: string, icon = "") {
  return {
    id,
    name,
    icon,
    placement: "top",
    position: 0,
    is_default: false,
    virtual: false,
    shared: false,
    hidden: false,
    can_edit: true,
    can_manage: true,
  };
}

const WS = { id: "ws-1", slug: "ws", name: "QA pilot" };

// The left menu, as the app mounts it next to the header: it owns the list and its `dashboard.*` refresh.
function Menu() {
  const nav = useDashboardsNav({ workspace: WS, path: "/w/ws/d/d1", unseenCount: 0 });
  return <>{nav.dialogs}</>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          path="w/:wsSlug/*"
          element={
            <>
              <Menu />
              <Header onToggleSidebar={() => {}} />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

const crumbs = () => within(screen.getByRole("navigation", { name: /breadcrumbs/i }));
const lastCrumb = () => crumbs().getByText((_, el) => el?.getAttribute("aria-current") === "page");

describe("Header crumb on an open dashboard", () => {
  beforeEach(async () => {
    server.list = [dash("d1", "QA-M личный", "😀"), dash("d2", "Ops")];
    useDashboardsStore.getState().clear();
    useWebSocketStore.setState({ lastEvent: null, connect: vi.fn() } as never);
    useWorkspaceStore.setState({ currentWorkspace: WS as never });
    useProjectStore.setState({ currentProject: null });
    await setLanguage("en");
  });

  it("names the open dashboard with its emoji, not «Dashboard»", async () => {
    renderAt("/w/ws/d/d1");
    await waitFor(() => expect(lastCrumb()).toHaveTextContent("QA-M личный"));
    expect(lastCrumb()).toHaveTextContent("😀");
    expect(crumbs().queryByText("Dashboard")).toBeNull();
  });

  it("a dashboard without an emoji keeps the dashboard icon and its own name", async () => {
    renderAt("/w/ws/d/d2");
    await waitFor(() => expect(lastCrumb()).toHaveTextContent(/^Ops$/));
    expect(lastCrumb().querySelector("svg")).not.toBeNull();
  });

  it("a rename (dashboard.updated) moves the crumb without a reload", async () => {
    renderAt("/w/ws/d/d1");
    await waitFor(() => expect(lastCrumb()).toHaveTextContent("QA-M личный"));
    server.list = [dash("d1", "QA-M renamed", "🚀"), dash("d2", "Ops")];
    act(() =>
      useWebSocketStore.setState({
        lastEvent: { type: "dashboard.updated", channel: "ws:ws-1", data: { id: "d1" }, timestamp: "t1" },
      }),
    );
    await waitFor(() => expect(lastCrumb()).toHaveTextContent("QA-M renamed"));
    expect(lastCrumb()).toHaveTextContent("🚀");
    expect(crumbs().queryByText("QA-M личный")).toBeNull();
  });

  it("a deleted dashboard leaves no stale name in the crumb", async () => {
    renderAt("/w/ws/d/d1");
    await waitFor(() => expect(lastCrumb()).toHaveTextContent("QA-M личный"));
    server.list = [dash("d2", "Ops")];
    act(() =>
      useWebSocketStore.setState({
        lastEvent: { type: "dashboard.deleted", channel: "ws:ws-1", data: { id: "d1" }, timestamp: "t2" },
      }),
    );
    await waitFor(() => expect(crumbs().queryByText("QA-M личный")).toBeNull());
    expect(lastCrumb()).toHaveTextContent("Dashboard");
  });

  it("another workspace's list never names this workspace's dashboard", () => {
    useDashboardsStore.setState({ workspaceId: "ws-other", items: [dash("d1", "Foreign")] as never, status: "ready" });
    useWebSocketStore.setState({ connect: vi.fn() } as never);
    render(
      <MemoryRouter initialEntries={["/w/ws/d/d1"]}>
        <Routes>
          <Route path="w/:wsSlug/*" element={<Header onToggleSidebar={() => {}} />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(crumbs().queryByText("Foreign")).toBeNull();
    expect(lastCrumb()).toHaveTextContent("Dashboard");
  });

  it.each(["/w/ws", "/w/ws/dashboard"])("%s stays «Dashboard»", async (path) => {
    renderAt(path);
    await waitFor(() => expect(useDashboardsStore.getState().status).toBe("ready"));
    expect(lastCrumb()).toHaveTextContent(/^Dashboard$/);
  });
});
