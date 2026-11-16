// Labelled enum values: `oneOf: [{ const, title }]` shows the title and stores the const; a bare `enum` still shows
// the value itself. Single choice (select) and many (checklist via `items.oneOf`).
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { SchemaForm, resolveSchema, validate, type FieldSchema } from "@/components/extensions/schema-form";

const labelled: FieldSchema = {
  type: "object",
  properties: {
    sort: { type: "string", title: "Sort", oneOf: [{ const: "updated", title: "Last updated" }, { const: "due_date", title: "Due date" }] },
    statuses: { type: "array", title: "Statuses", items: { type: "string", oneOf: [{ const: "todo", title: "To Do" }, { const: "in_progress", title: "In Progress" }] } },
  },
};
const ctx = { secretsSet: new Set<string>() };
const bare: FieldSchema = {
  type: "object",
  properties: {
    sort: { type: "string", title: "Sort", enum: ["updated", "due_date"] },
    statuses: { type: "array", title: "Statuses", items: { type: "string", enum: ["todo", "in_progress"] } },
  },
};

function mount(schema: FieldSchema) {
  const onSubmit = vi.fn().mockResolvedValue(undefined);
  render(<SchemaForm schema={schema} onSubmit={onSubmit} submitLabel="Save" />);
  return onSubmit;
}
const optionTexts = (select: HTMLElement) => within(select).getAllByRole("option").map((o) => o.textContent);

describe("oneOf with title", () => {
  it("shows the titles, never the stored slug", () => {
    mount(labelled);
    expect(optionTexts(screen.getByLabelText("Sort"))).toEqual(expect.arrayContaining(["Last updated", "Due date"]));
    const group = screen.getByRole("group", { name: "Statuses" });
    expect(within(group).getByText("In Progress")).toBeTruthy();
    expect(screen.queryByText("in_progress")).toBeNull();
    expect(screen.queryByText("due_date")).toBeNull();
  });

  it("writes the const into the value", async () => {
    const onSubmit = mount(labelled);
    fireEvent.change(screen.getByLabelText("Sort"), { target: { value: "due_date" } });
    fireEvent.click(within(screen.getByRole("group", { name: "Statuses" })).getByLabelText("In Progress"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ sort: "due_date", statuses: ["in_progress"] }));
  });

  it("is validated as an enum of its consts", () => {
    const r = resolveSchema(labelled, {});
    expect(r.properties!.sort!.enum).toEqual(["updated", "due_date"]);
    expect(validate(r, { sort: "random" }, ctx)).toMatchObject({ sort: { code: "enum" } });
    expect(validate(r, { sort: "updated" }, ctx)).toEqual({});
  });

  it("leaves a oneOf of sub-schemas alone", () => {
    const alt: FieldSchema = { type: "string", oneOf: [{ const: "a" }, { type: "number" } as never] };
    expect(resolveSchema(alt, {}).enum).toBeUndefined();
  });
});

describe("bare enum", () => {
  it("still shows the value itself and stores it", async () => {
    const onSubmit = mount(bare);
    expect(optionTexts(screen.getByLabelText("Sort"))).toEqual(expect.arrayContaining(["updated", "due_date"]));
    expect(within(screen.getByRole("group", { name: "Statuses" })).getByText("in_progress")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Sort"), { target: { value: "due_date" } });
    fireEvent.click(screen.getByLabelText("in_progress"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ sort: "due_date", statuses: ["in_progress"] }));
  });
});
