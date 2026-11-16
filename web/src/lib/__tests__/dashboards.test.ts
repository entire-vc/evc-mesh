import { beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", () => ({ api: apiMock }));

import { applyOrder, deleteDashboard, duplicateDashboard, listDashboards, putDashboardOrder, renameDashboard, resetDashboard, setDashboardHidden, setDashboardSharing, firstMentionsDashboardId, menuGroup, moveInOrder, orderOf, stepInOrder, type Dashboard } from "../dashboards";

const d = (id: string, placement: "top" | "bottom", position: number, over: Partial<Dashboard> = {}): Dashboard => ({
  id, name: id, icon: "", placement, position, is_default: false, virtual: false, shared: false, hidden: false, can_edit: true, can_manage: true, ...over,
});
const LIST = [d("b", "top", 1), d("a", "top", 0), d("x", "bottom", 0), d("h", "bottom", 1, { shared: true, hidden: true })];

describe("dashboard order helpers", () => {
  it("the menu group is sorted by position and leaves hidden dashboards out", () => {
    expect(menuGroup(LIST, "top").map((x) => x.id)).toEqual(["a", "b"]);
    expect(orderOf(LIST)).toEqual({ top: ["a", "b"], bottom: ["x"] });
  });
  it("moves inside a group and across groups", () => {
    const o = orderOf(LIST);
    expect(moveInOrder(o, "a", "top", 1)).toEqual({ top: ["b", "a"], bottom: ["x"] });
    expect(moveInOrder(o, "a", "bottom", 0)).toEqual({ top: ["b"], bottom: ["a", "x"] });
    expect(moveInOrder(o, "a", "bottom", 99)).toEqual({ top: ["b"], bottom: ["x", "a"] });
  });
  it("one step stops at the edges", () => {
    const o = orderOf(LIST);
    expect(stepInOrder(o, "a", -1)).toBeNull();
    expect(stepInOrder(o, "b", 1)).toBeNull();
    expect(stepInOrder(o, "a", 1)).toEqual({ top: ["b", "a"], bottom: ["x"] });
  });
  it("applyOrder rewrites placement and position and leaves the hidden one alone", () => {
    const next = applyOrder(LIST, { top: ["x", "a"], bottom: ["b"] });
    expect(menuGroup(next, "top").map((x) => x.id)).toEqual(["x", "a"]);
    expect(menuGroup(next, "bottom").map((x) => x.id)).toEqual(["b"]);
    expect(next.find((x) => x.id === "h")).toEqual(LIST[3]);
  });
});

describe("mention counter host", () => {
  it("is the first menu dashboard with a `mentions` widget, top before bottom; a virtual default counts as having it", () => {
    const list = [
      d("a", "top", 0, { widget_types: ["my-tasks"] }),
      d("b", "top", 1, { widget_types: ["mentions"] }),
      d("x", "bottom", 0, { widget_types: ["mentions"] }),
    ];
    expect(firstMentionsDashboardId(list)).toBe("b");
    expect(firstMentionsDashboardId([d("default", "top", 0, { virtual: true })])).toBe("default");
  });
  it("no host when no dashboard has the widget (the counter then stays on the bell only)", () => {
    expect(firstMentionsDashboardId([d("a", "top", 0, { widget_types: ["my-tasks"] })])).toBeNull();
  });
  it("a hidden shared dashboard does not host it", () => {
    expect(firstMentionsDashboardId([d("h", "top", 0, { hidden: true, widget_types: ["mentions"] })])).toBeNull();
  });
});

// The wire contract of the backend card (routes_dashboards.go): everything per id lives under the workspace.
describe("API client paths and bodies", () => {
  beforeEach(() => { apiMock.mockReset(); apiMock.mockResolvedValue({ dashboards: [] }); });
  const last = () => apiMock.mock.calls[apiMock.mock.calls.length - 1]! as [string, { method?: string; body?: unknown } | undefined];

  it("per-id calls go under /workspaces/:ws; sharing is a PATCH with shared/members_can_edit", async () => {
    await renameDashboard("w1", "d1", "New");
    expect(last()).toEqual(["/api/v1/workspaces/w1/dashboards/d1", { method: "PATCH", body: { name: "New" } }]);
    await setDashboardSharing("w1", "d1", { shared: true, members_can_edit: true });
    expect(last()).toEqual(["/api/v1/workspaces/w1/dashboards/d1", { method: "PATCH", body: { shared: true, members_can_edit: true } }]);
    await setDashboardHidden("w1", "d1", true);
    expect(last()).toEqual(["/api/v1/workspaces/w1/dashboards/d1/hidden", { method: "PUT", body: { hidden: true } }]);
    await duplicateDashboard("w1", "d1");
    expect(last()).toEqual(["/api/v1/workspaces/w1/dashboards/d1/duplicate", { method: "POST" }]);
    await resetDashboard("w1", "d1");
    expect(last()[0]).toBe("/api/v1/workspaces/w1/dashboards/d1/reset");
    await deleteDashboard("w1", "d1");
    expect(last()).toEqual(["/api/v1/workspaces/w1/dashboards/d1", { method: "DELETE" }]);
  });

  it("the list asks for hidden ones only when told to; the order answer is the new list", async () => {
    await listDashboards("w1");
    expect(last()[0]).toBe("/api/v1/workspaces/w1/dashboards");
    await listDashboards("w1", true);
    expect(last()[0]).toBe("/api/v1/workspaces/w1/dashboards?include_hidden=true");
    apiMock.mockResolvedValueOnce({ dashboards: [d("real-uuid", "top", 0)] });
    expect((await putDashboardOrder("w1", { top: ["default"], bottom: [] })).map((x) => x.id)).toEqual(["real-uuid"]);
  });
});
