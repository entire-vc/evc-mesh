// Agent widgets (PRD Dashboards §5.8 "Agents and costs", agents part).
// The registry collects this folder: `descriptors` here, the views in views.tsx, loaded when a widget is drawn.
// Data refreshes on a 30 s poll: the event bus has no agent status change event yet. Sizes and categories are the
// core catalog's (core-catalog.ts, generated from catalog.go).
import type { WidgetDescriptor, WidgetSettingsSchema } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";
import { AGENTS_ACTIVE_SETTINGS_SCHEMA, AGENTS_STATUS_SETTINGS_SCHEMA } from "./schema";

export const descriptors: WidgetDescriptor[] = [
  {
    id: "agents-active",
    title: "Active agents",
    description: "Agents that are not offline, up to a limit",
    category: coreCategory("agents-active"),
    origin: "core",
    scope: "workspace",
    actions: [],
    size: coreSize("agents-active"),
    settings: AGENTS_ACTIVE_SETTINGS_SCHEMA as unknown as WidgetSettingsSchema,
  },
  {
    id: "agents-status",
    title: "Agents",
    description: "Every agent: type, status, presence, last heartbeat and current task",
    category: coreCategory("agents-status"),
    origin: "core",
    scope: "workspace",
    actions: [],
    size: coreSize("agents-status"),
    settings: AGENTS_STATUS_SETTINGS_SCHEMA as unknown as WidgetSettingsSchema,
  },
  {
    id: "agents-summary",
    title: "Agents now",
    description: "Three counters: online, busy, offline and error",
    category: coreCategory("agents-summary"),
    origin: "core",
    scope: "workspace",
    actions: [],
    size: coreSize("agents-summary"),
  },
];
