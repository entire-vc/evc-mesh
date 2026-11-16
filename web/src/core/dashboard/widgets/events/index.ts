// The event-feed widget group (W5): one widget, the workspace event feed. The registry picks this folder up;
// the view is in views.tsx, loaded when the widget is drawn.
import type { WidgetDescriptor, WidgetSettingsSchema } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";
import { EVENT_FEED_SETTINGS_SCHEMA } from "./schema";

export const descriptors: WidgetDescriptor[] = [
  {
    id: "event-feed",
    title: "Event feed",
    description: "Events from every project you can see, live",
    category: coreCategory("event-feed"),
    origin: "core",
    scope: "workspace",
    actions: ["openTask"],
    size: coreSize("event-feed"),
    settings: EVENT_FEED_SETTINGS_SCHEMA as unknown as WidgetSettingsSchema,
  },
];

/** The feed is added from the catalog: a dashboard that picks no widgets keeps its default set. */
export const catalogOnly = true;
