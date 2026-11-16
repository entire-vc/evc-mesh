// Widget group "Tasks and triage" (PRD §5.8): my-tasks, triage-queue, task-list. `my-tasks` moved here from the legacy
// group. All three are dashboard.widget.<id> slots (a theme may redraw any of them): the core reads the rows with the
// instance settings (`dataHook`, see ../../hooks.ts), the view only draws them. Sizes and categories are the core
// catalog's (§5.8, core-widgets.json).
import type { WidgetDescriptor } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";
import { PRIORITIES, SCOPES, SORTS, STATUS_CATEGORIES } from "./query";

// Values the user picks are shown by name, never as the stored slug: `oneOf: [{ const, title }]` (SchemaForm), the
// title translated by the settings dialog through `x-title-key` (dashboardEdit namespace). Statuses and priorities
// read as on the board. The server's validation schema (catalog.go) keeps the same values.
const STATUS_TITLES: Record<(typeof STATUS_CATEGORIES)[number], string> = {
  backlog: "Backlog", triage: "Triage", todo: "To Do", in_progress: "In Progress", review: "Review", done: "Done", cancelled: "Cancelled",
};
const PRIORITY_TITLES: Record<(typeof PRIORITIES)[number], string> = { urgent: "Urgent", high: "High", medium: "Medium", low: "Low", none: "None" };
const SCOPE_TITLES: Record<(typeof SCOPES)[number], string> = { mine: "Assigned to me", project: "One project", workspace: "Whole workspace" };
const SORT_TITLES: Record<(typeof SORTS)[number], string> = { updated: "Last updated", priority: "Priority", due_date: "Due date" };

function choices<V extends string>(group: string, values: readonly V[], titles: Record<V, string>) {
  return values.map((v) => ({ const: v, title: titles[v], "x-title-key": `settingsForm.options.${group}.${v}` }));
}

const project = { type: "string", title: "Project", "x-widget": "project-picker" } as const;
const statuses = { type: "array", title: "Statuses", items: { type: "string", oneOf: choices("status", STATUS_CATEGORIES, STATUS_TITLES) } } as const;

export const descriptors: WidgetDescriptor[] = [
  {
    id: "my-tasks",
    title: "My tasks",
    description: "Tasks assigned to you, edited in place",
    category: coreCategory("my-tasks"),
    origin: "core",
    scope: "user",
    dataHook: "useMyTasks",
    actions: ["openTask", "quickEdit", "retry"],
    size: coreSize("my-tasks"),
    settings: {
      type: "object",
      properties: {
        limit: { type: "integer", title: "Limit", minimum: 5, maximum: 50, default: 10 },
        statuses,
        projectId: project,
      },
    },
  },
  {
    id: "triage-queue",
    title: "Triage queue",
    description: "Cards waiting for triage: open or move them to a project and status",
    category: coreCategory("triage-queue"),
    origin: "core",
    scope: "workspace",
    dataHook: "useTriageQueue",
    actions: ["openTask", "moveTask", "retry"],
    size: coreSize("triage-queue"),
    settings: {
      type: "object",
      properties: {
        limit: { type: "integer", title: "Limit", minimum: 1, maximum: 50, default: 10 },
        projectId: project,
        hardGatesOnly: { type: "boolean", title: "Hard gates only", default: false },
      },
    },
  },
  {
    id: "task-list",
    title: "Task list",
    description: "Tasks by a filter: scope, statuses, priority, due date, gate, sort",
    category: coreCategory("task-list"),
    origin: "core",
    scope: "workspace",
    dataHook: "useTaskList",
    actions: ["openTask", "quickEdit", "retry"],
    size: coreSize("task-list"),
    settings: {
      type: "object",
      properties: {
        scope: { type: "string", title: "Scope", oneOf: choices("scope", SCOPES, SCOPE_TITLES), default: "mine" },
        projectId: project,
        statuses,
        priority: { type: "array", title: "Priority", items: { type: "string", oneOf: choices("priority", PRIORITIES, PRIORITY_TITLES) } },
        dueBefore: { type: "string", title: "Due before", format: "date" },
        gated: { type: "boolean", title: "Only with a gate", default: false },
        sort: { type: "string", title: "Sort", oneOf: choices("sort", SORTS, SORT_TITLES), default: "updated" },
        limit: { type: "integer", title: "Limit", minimum: 1, maximum: 50, default: 20 },
      },
    },
  },
];
