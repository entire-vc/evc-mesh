import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { setLanguage } from "@/i18n";
import { useWorkspaceStore } from "@/stores/workspace";
import { SettingsDialog } from "@/core/dashboard/edit/settings-dialog";
import { fromWireCatalog } from "@/core/dashboard/edit/api";
import type { LayoutItem } from "@/core/dashboard/edit/types";
import { useCapsStore } from "../capabilities";
import { stateFromSettings } from "../state";
import { usageFixture } from "./fixtures";

const item: LayoutItem = { id: "stats", type: "agent-usage", x: 0, y: 0, w: 12, h: 6, settings: { m: "quota", g: "account", p: "2026-11-01..2026-11-18", "f.account": ["private"] } };
const catalog = fromWireCatalog({ id: "agent-usage", origin: "core", category: "agents", size: { default: { w: 12, h: 6 }, min: { w: 6, h: 4 }, max: { w: 12, h: 12 } } });
beforeEach(async () => {
  await setLanguage("en");
  useWorkspaceStore.setState({ currentWorkspace: { id: "ws" } as NonNullable<ReturnType<typeof useWorkspaceStore.getState>["currentWorkspace"]> });
  useCapsStore.setState({ byWs: {} });
});
afterEach(cleanup);

describe("statistics widget settings", () => {
  it("hides admin choices and removes a stored private account for a member", async () => {
    useCapsStore.getState().set("ws", { group_by: ["model", "agent", "project"], cost: true, quota: false });
    const save = vi.fn();
    render(<SettingsDialog item={item} type={catalog} onSave={save} onClose={() => {}} />);
    expect(screen.queryByRole("option", { name: "Pool / account" })).toBeNull();
    expect(screen.getAllByRole("option", { name: "Workspace" }).length).toBeGreaterThan(0);
    expect(screen.queryByRole("option", { name: "Subscription limits" })).toBeNull();
    expect(screen.queryByLabelText("Limit window")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(stateFromSettings(save.mock.calls[0]![0].settings)).toMatchObject({ m: "cost", g: "model", p: { from: "2026-11-01", to: "2026-11-18" }, f: {} });
  });

  it("retains an admin's window and custom dates when saving and reopening", async () => {
    useCapsStore.getState().set("ws", usageFixture().capabilities);
    const save = vi.fn();
    const view = render(<SettingsDialog item={item} type={catalog} onSave={save} onClose={() => {}} />);
    expect(screen.getAllByRole("option", { name: "Pool / account" })[0]).toBeTruthy();
    expect(screen.getByRole("option", { name: "Subscription limits" })).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Limit window"), { target: { value: "month" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    const saved = save.mock.calls[0]![0];
    expect(stateFromSettings(saved.settings)).toMatchObject({ m: "quota", g: "account", w: "month", p: { from: "2026-11-01", to: "2026-11-18" }, f: { account: ["private"] } });
    view.unmount();
    render(<SettingsDialog item={{ ...item, ...saved }} type={catalog} onSave={save} onClose={() => {}} />);
    expect((screen.getByLabelText("Limit window") as HTMLSelectElement).value).toBe("month");
    expect((screen.getByLabelText("Period") as HTMLInputElement).value).toBe("2026-11-01..2026-11-18");
  });
});
