// The settings form of the task widgets as the dashboard editor opens it (catalog entry -> SettingsDialog): status,
// priority, scope and sort values read by name, in the user's language; Save still stores the slugs.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { setLanguage } from "@/i18n";
import { fromWireCatalog } from "../../../edit/api";
import { SettingsDialog } from "../../../edit/settings-dialog";
import type { LayoutItem } from "../../../edit/types";

const size = { default: { w: 6, h: 4 }, min: { w: 3, h: 2 }, max: { w: 12, h: 8 } };
// the server's catalog entry: its `settings` only validates (no titles); the form comes from the client descriptor
const catalog = (id: string) => fromWireCatalog({ id, origin: "core", category: "tasks", size, settings: { type: "object", properties: { limit: { type: "integer" } } } });
const item = (type: string, settings: Record<string, unknown> = {}): LayoutItem => ({ id: "w1", type, x: 0, y: 0, w: 6, h: 4, settings });

function open(type: string, settings: Record<string, unknown> = {}) {
  const onSave = vi.fn();
  render(<SettingsDialog item={item(type, settings)} type={catalog(type)} onClose={() => {}} onSave={onSave} />);
  return onSave;
}
const SLUGS = ["backlog", "triage", "todo", "in_progress", "review", "done", "cancelled", "urgent", "mine", "workspace", "updated", "due_date"];
const shownSlugs = () => {
  const text = document.body.textContent ?? "";
  const options = within(document.body).queryAllByRole("option").map((o) => o.textContent);
  return SLUGS.filter((s) => new RegExp(`\\b${s}\\b`).test(text) || options.includes(s));
};

afterEach(async () => {
  cleanup();
  await setLanguage("en");
});

describe("task widget settings show names, not slugs", () => {
  it("my-tasks: statuses read as on the board, Save stores the slug", async () => {
    await setLanguage("en");
    const onSave = open("my-tasks");
    const statuses = within(document.body).getByRole("group", { name: "Statuses" });
    expect(within(statuses).getByText("In Progress")).toBeTruthy();
    expect(within(statuses).getByText("To Do")).toBeTruthy();
    expect(document.body.textContent).not.toContain("in_progress");
    expect(shownSlugs()).toEqual([]);
    fireEvent.click(within(statuses).getByLabelText("In Progress"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSave).toHaveBeenCalled());
    expect(onSave.mock.calls[0]![0].settings).toMatchObject({ statuses: ["in_progress"] });
  });

  it("task-list: scope, priority and sort by name; the stored values stay selected", async () => {
    await setLanguage("en");
    const onSave = open("task-list", { scope: "workspace", sort: "due_date", priority: ["urgent"] });
    const dialog = within(document.body);
    const scope = dialog.getByLabelText("Scope") as HTMLSelectElement;
    const sort = dialog.getByLabelText("Sort") as HTMLSelectElement;
    expect(scope.value).toBe("workspace");
    expect(scope.selectedOptions[0]!.textContent).toBe("Whole workspace");
    expect(sort.selectedOptions[0]!.textContent).toBe("Due date");
    expect(within(dialog.getByRole("group", { name: "Priority" })).getByLabelText("Urgent")).toBeChecked();
    expect(shownSlugs()).toEqual([]);
    fireEvent.change(sort, { target: { value: "priority" } });
    fireEvent.click(dialog.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSave).toHaveBeenCalled());
    expect(onSave.mock.calls[0]![0].settings).toMatchObject({ scope: "workspace", sort: "priority", priority: ["urgent"] });
  });

  it("in Russian the same values are translated", async () => {
    await setLanguage("ru");
    open("task-list");
    const dialog = within(document.body);
    expect(within(dialog.getByRole("group", { name: "Statuses" })).getByText("В работе")).toBeTruthy();
    expect(within(dialog.getByRole("group", { name: "Priority" })).getByText("Срочный")).toBeTruthy();
    expect((dialog.getByLabelText("Sort") as HTMLSelectElement).selectedOptions[0]!.textContent).toBe("Недавно изменённые");
    expect(shownSlugs()).toEqual([]);
  });
});
