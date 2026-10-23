import type { CustomFieldDefinition } from "@/types";
import type { FieldDescriptor, TaskRowColumn } from "./types";

/** Built-in list columns in the order `mesh-default` draws them; `name` of the column picker is `title` here. */
const BUILTIN_COLUMNS: readonly { key: string; kind: FieldDescriptor["kind"]; labelKey: string; width?: number }[] = [
  { key: "title", kind: "text", labelKey: "columns.title", width: 240 },
  { key: "status", kind: "select", labelKey: "columns.status", width: 120 },
  { key: "priority", kind: "select", labelKey: "columns.priority", width: 100 },
  { key: "assignee", kind: "person", labelKey: "columns.assignee", width: 100 },
  { key: "due_date", kind: "date", labelKey: "columns.dueDate", width: 110 },
  { key: "labels", kind: "multiselect", labelKey: "columns.labels" },
  { key: "estimated_hours", kind: "number", labelKey: "columns.estimate" },
  { key: "created_at", kind: "date", labelKey: "columns.created" },
];

const CUSTOM_KIND: Record<string, FieldDescriptor["kind"]> = {
  number: "number",
  date: "date",
  select: "select",
  multiselect: "multiselect",
  checkbox: "flag",
};

/**
 * The visible columns as the contract's `columns`: the title first (always), then what the user
 * picked in the built-in order, then custom fields. `rowFields` (the theme's `row_fields`) narrows
 * and orders what follows the title; unknown keys are skipped.
 */
export function buildRowColumns(
  visible: ReadonlySet<string>,
  customDefs: readonly CustomFieldDefinition[],
  translate: (key: string) => string,
  rowFields?: readonly string[],
): TaskRowColumn[] {
  const all: TaskRowColumn[] = BUILTIN_COLUMNS.filter((c) => c.key === "title" || visible.has(c.key)).map((c) => ({
    key: c.key,
    label: translate(c.labelKey),
    field: { key: c.key, label: translate(c.labelKey), kind: c.kind, source: "builtin" },
    ...(c.width ? { width: c.width } : {}),
  }));
  for (const def of customDefs) {
    if (!visible.has(`cf:${def.slug}`)) continue;
    all.push({
      key: `cf:${def.slug}`,
      label: def.name,
      field: { key: `cf:${def.slug}`, label: def.name, kind: CUSTOM_KIND[def.field_type] ?? "text", source: "custom", projectId: def.project_id },
      width: 100,
      customField: def,
    });
  }
  if (!rowFields) return all;
  const byKey = new Map(all.map((c) => [c.key, c]));
  const out: TaskRowColumn[] = [byKey.get("title") as TaskRowColumn];
  for (const k of rowFields) {
    const c = byKey.get(k);
    if (c && k !== "title" && !out.includes(c)) out.push(c);
  }
  return out;
}
