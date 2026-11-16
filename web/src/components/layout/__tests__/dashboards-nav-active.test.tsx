import { describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({ api: vi.fn(() => new Promise(() => {})), getAccessToken: vi.fn(() => null) }));

import { useDashboardsNav } from "../use-dashboards-nav";
import { useDashboardsStore } from "@/stores/dashboards";
import { useWebSocketStore } from "@/stores/websocket";
import type { Dashboard } from "@/lib/dashboards";

const base = { placement: "top", position: 0, is_default: false, virtual: false, shared: false, hidden: false, can_edit: true, can_manage: true } as const;
const VIRTUAL: Dashboard = { ...base, id: "default", name: "Dashboard", icon: "", is_default: true, virtual: true };
const OPS: Dashboard = { ...base, id: "d-ops", name: "Ops", icon: "", position: 1 };

// The page route sends `/w/:ws/dashboard` on to `/w/:ws/d/<home id>`: the menu row must link there and stay lit.
function nav(path: string) {
  useDashboardsStore.setState({ workspaceId: "w1", items: [VIRTUAL, OPS], status: "ready", error: null, load: async () => {} });
  useWebSocketStore.setState({ connect: () => {} } as never);
  const { result } = renderHook(() => useDashboardsNav({ workspace: { id: "w1", slug: "acme" }, path, unseenCount: 0 }), {
    wrapper: ({ children }) => <MemoryRouter>{children}</MemoryRouter>,
  });
  return result.current.items;
}

describe("dashboard menu links and the active row", () => {
  it("every dashboard, the virtual default included, links to /d/:id", () => {
    expect(nav("/w/acme/d/default").map((i) => i.href)).toEqual(["/w/acme/d/default", "/w/acme/d/d-ops"]);
  });

  it("the virtual default is active at /d/default, where the home redirect lands", () => {
    expect(nav("/w/acme/d/default").map((i) => i.active)).toEqual([true, false]);
  });

  it("the virtual default is active on the bare /dashboard address before the redirect", () => {
    expect(nav("/w/acme/dashboard").map((i) => i.active)).toEqual([true, false]);
  });

  it("a stored dashboard is active on its page and its expanded widget", () => {
    expect(nav("/w/acme/d/d-ops").map((i) => i.active)).toEqual([false, true]);
  });
});
