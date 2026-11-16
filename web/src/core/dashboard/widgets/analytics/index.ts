// Analytics and cost widgets: everything the Analytics page and the cost block of Sessions show, one widget
// per question, each with its own period and project. Data: useAnalytics (one request per distinct filter).
// The views are in views.tsx, loaded when a widget of the group is first drawn (see registry.ts).
import type { WidgetDescriptor, WidgetSettingsSchema } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";
import { METRICS, METRIC_OPTIONS } from "./metrics";

/** Every analytics widget: the period (SchemaForm `period`, default 30 days) and an optional project. */
const FILTER: WidgetSettingsSchema["properties"] = {
  period: { type: "object", title: "Period", "x-widget": "period", default: { days: 30 } },
  project: { type: "string", title: "Project", "x-widget": "project-picker" },
};
const settings = (own: WidgetSettingsSchema["properties"] = {}): WidgetSettingsSchema => ({
  type: "object",
  properties: { ...FILTER, ...own },
});

/** The id with its catalog size and category (catalog.go is their one source) and the common fields. */
const core = (id: string) => ({ id, category: coreCategory(id), size: coreSize(id), origin: "core", scope: "workspace" }) as const;

export const descriptors: WidgetDescriptor[] = [
  {
    ...core("metric"),
    title: "Metric",
    description: "One number for the period, compared with the previous period of the same length",
    settings: settings({
      metric: {
        type: "string",
        title: "Metric",
        enum: METRIC_OPTIONS.map((m) => m.id),
        "x-options": Object.fromEntries(METRIC_OPTIONS.map((m) => [m.id, m.label])),
        default: METRICS[0]!.id,
      },
    }),
    actions: ["retry"],
  },
  {
    ...core("tasks-breakdown"),
    title: "Tasks breakdown",
    description: "Tasks by status, priority, assignee or project, as bars or a ring",
    settings: settings({
      dimension: {
        type: "string",
        title: "Split by",
        enum: ["status", "priority", "assignee", "project"],
        "x-options": { status: "Status", priority: "Priority", assignee: "Assignee", project: "Project" },
        default: "status",
      },
      view: { type: "string", title: "View", enum: ["bars", "donut"], "x-options": { bars: "Bars", donut: "Ring" }, default: "bars" },
    }),
    actions: ["openAgent", "retry"],
  },
  {
    ...core("tasks-timeline"),
    title: "Tasks timeline",
    description: "Tasks created and completed per day",
    settings: settings(),
    actions: ["retry"],
  },
  {
    ...core("events-breakdown"),
    title: "Events by type",
    description: "Events of the period by type",
    settings: settings(),
    actions: ["retry"],
  },
  {
    ...core("agent-leaderboard"),
    title: "Agent leaderboard",
    description: "Tasks completed per agent",
    settings: settings(),
    actions: ["openAgent", "retry"],
  },
  {
    ...core("cost-kpis"),
    title: "Cost summary",
    description: "Reported cost, tokens, sessions and the average per session",
    settings: settings(),
    actions: ["retry"],
  },
  {
    ...core("cost-by-agent"),
    title: "Cost by agent",
    description: "Reported cost per agent",
    settings: settings(),
    actions: ["openAgent", "retry"],
  },
  {
    ...core("cost-top-tasks"),
    title: "Most expensive tasks",
    description: "Tasks with the highest cost or token use, with their sessions",
    settings: settings({
      sort: { type: "string", title: "Sort by", enum: ["cost", "tokens"], "x-options": { cost: "Cost", tokens: "Tokens" }, default: "cost" },
    }),
    actions: ["openTask", "retry"],
  },
  {
    ...core("cost-by-project"),
    title: "Cost by project",
    description: "Reported cost per project",
    settings: settings(),
    actions: ["retry"],
  },
  {
    ...core("cost-by-day"),
    title: "Cost by day",
    description: "Reported cost per day",
    settings: settings(),
    actions: ["retry"],
  },
];

/** Ten analytics tiles would bury the dashboard everyone already has: offered in the catalog, placed by choice. */
export const catalogOnly = true;

