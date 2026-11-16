// The Widget settings dialog is capped to the window: the form scrolls in its own body, while the
// title and Cancel / Save sit outside it and never scroll away (geometry at 1366x768, 1440x900 and
// 393 is checked in e2e-widget-settings). Save in the footer still submits the form.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { fromWireCatalog } from "../api";
import { SettingsDialog } from "../settings-dialog";
import type { LayoutItem } from "../types";

const size = { default: { w: 6, h: 4 }, min: { w: 3, h: 2 }, max: { w: 12, h: 10 } };
const taskList = fromWireCatalog({ id: "task-list", origin: "core", category: "tasks", size, settings: { type: "object", properties: {} } });
const widget: LayoutItem = { id: "w1", type: "task-list", x: 0, y: 0, w: 6, h: 4, settings: {} };

afterEach(cleanup);

describe("Widget settings dialog height", () => {
  it("caps the dialog to the window and scrolls only the form body", () => {
    render(<SettingsDialog item={widget} type={taskList} onClose={() => {}} onSave={() => {}} />);
    const dialog = screen.getByRole("dialog", { name: "Widget settings" });
    expect(dialog.className).toMatch(/\bmax-h-\[85dvh\]/);
    expect(dialog.className).toMatch(/\bflex-col\b/);
    const body = screen.getByTestId("widget-settings-body");
    expect(body.className).toMatch(/\boverflow-y-auto\b/);
    expect(body.className).toMatch(/\bmin-h-0\b/);
    // the form fields are in the scrolling body; title, Cancel and Save are not
    expect(within(body).getByRole("textbox", { name: /Title/ })).toBeTruthy();
    expect(body.contains(screen.getByRole("heading", { name: "Widget settings" }))).toBe(false);
    expect(body.contains(screen.getByRole("button", { name: "Save" }))).toBe(false);
    expect(body.contains(screen.getByRole("button", { name: "Cancel" }))).toBe(false);
  });

  it("Save in the footer submits the form and closes the dialog", async () => {
    const onSave = vi.fn();
    const onClose = vi.fn();
    render(<SettingsDialog item={widget} type={taskList} onClose={onClose} onSave={onSave} />);
    fireEvent.change(within(screen.getByTestId("widget-settings-body")).getByRole("textbox", { name: /Title/ }), { target: { value: "  Sprint  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ title: "Sprint", auto_refresh: true }));
    expect(onClose).toHaveBeenCalled();
  });

  it("Cancel closes without saving", () => {
    const onSave = vi.fn();
    const onClose = vi.fn();
    render(<SettingsDialog item={widget} type={taskList} onClose={onClose} onSave={onSave} />);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalled();
    expect(onSave).not.toHaveBeenCalled();
  });
});
