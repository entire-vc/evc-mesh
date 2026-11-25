// The agent-usage widget group: one widget, the whole statistics view (≈ $, tokens, spawns, run time, closed
// cards, subscription limits). Its descriptor id and size come from catalog.go through the generated
// core-widgets.json (coreSize would throw without the catalog entry). Runtime-free (registry contract C).
import type { WidgetDescriptor } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";
import { AGENT_USAGE_SETTINGS_SCHEMA } from "./schema";

export const catalogOnly = true;

export const descriptors: WidgetDescriptor[] = [
  {
    id: "agent-usage",
    title: "Agent statistics",
    description: "≈ $ at API price, tokens and spawns by model, provider, pool, agent, project or workspace",
    category: coreCategory("agent-usage"),
    origin: "core",
    scope: "user",
    actions: [],
    size: coreSize("agent-usage"),
    settings: AGENT_USAGE_SETTINGS_SCHEMA,
  },
];
