// Initiatives widgets (PRD dashboards §5.8): the workspace's initiatives and one chosen initiative. Both lead to
// the initiative page w/:ws/initiatives/:id. Anyone in the workspace may read them; the buttons follow the role.
// The views are in views.tsx, loaded when a widget of the group is first drawn (see registry.ts).
import type { WidgetDescriptor } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";

const base = { origin: "core", scope: "workspace", category: coreCategory("initiatives") } as const;

const STATUS_OPTIONS = { active: "Active", completed: "Completed", archived: "Archived" };

export const descriptors: WidgetDescriptor[] = [
  {
    ...base,
    id: "initiatives",
    title: "Initiatives",
    description: "Initiatives of the workspace with their progress and deadline",
    size: coreSize("initiatives"),
    settings: {
      type: "object",
      properties: {
        statuses: {
          type: "array",
          title: "Statuses",
          items: { type: "string", enum: Object.keys(STATUS_OPTIONS) },
          "x-options": STATUS_OPTIONS,
        },
        sort: {
          type: "string",
          title: "Sort",
          enum: ["created", "name", "target_date", "progress"],
          "x-options": { created: "Newest first", name: "Name", target_date: "Deadline", progress: "Progress" },
          default: "created",
        },
        view: { type: "string", title: "View", enum: ["grid", "list"], "x-options": { grid: "Grid", list: "List" }, default: "grid" },
      },
    },
    actions: ["retry"],
  },
  {
    ...base,
    id: "initiative",
    title: "Initiative",
    description: "One initiative: progress, deadline and linked projects",
    size: coreSize("initiative"),
    settings: {
      type: "object",
      properties: { initiativeId: { type: "string", title: "Initiative id" } },
      required: ["initiativeId"],
    },
    actions: ["retry"],
  },
];

/** In the catalog, not on a dashboard that picks no widgets: the default dashboard stays as it was. */
export const catalogOnly = true;
