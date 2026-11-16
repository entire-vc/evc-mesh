import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", () => ({ api: apiMock, getAccessToken: vi.fn(() => null) }));

import { DashboardDialogs } from "../dashboard-dialogs";
import type { Dashboard } from "@/lib/dashboards";

const D: Dashboard = { id: "d1", name: "Ops", icon: "icon:rocket", placement: "top", position: 0, is_default: false, virtual: false, shared: false, hidden: false, can_edit: true, can_manage: true };

describe("dashboard icon dialog", () => {
  it("shows the stored theme icon through the shared picker, never as literal text", () => {
    render(<DashboardDialogs state={{ kind: "icon", id: "d1" }} onClose={() => {}} workspaceId="w1" dashboards={[D]} onChanged={() => {}} />);
    const trigger = screen.getByRole("button", { name: "Icon" });
    expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");
    expect(screen.queryByText("icon:rocket")).toBeNull();
    expect(trigger.querySelector("svg")).toBeTruthy();
  });

  it("rename keeps the icon; the PATCH goes under the workspace", async () => {
    apiMock.mockReset();
    apiMock.mockResolvedValue({ ...D, name: "Ops 2" });
    render(<DashboardDialogs state={{ kind: "rename", id: "d1" }} onClose={() => {}} workspaceId="w1" dashboards={[D]} onChanged={() => {}} />);
    const input = screen.getByDisplayValue("Ops");
    fireEvent.change(input, { target: { value: "Ops 2" } });
    fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/v1/workspaces/w1/dashboards/d1", { method: "PATCH", body: { name: "Ops 2" } }));
  });
});
