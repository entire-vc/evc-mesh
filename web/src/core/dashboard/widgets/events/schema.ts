// The settings JSON Schema of the event-feed widget: data the registry chunk carries for index.ts.
// Contract C: index.ts imports only files like this one, with no runtime imports (no hooks, no React). Nothing
// else imports it either: a second importer (the widget) would make it a chunk of its own on every registry route.
/** JSON Schema of the instance settings (x-widget controls per PRD §5.6). */
export const EVENT_FEED_SETTINGS_SCHEMA = {
  type: "object",
  properties: {
    event_types: {
      type: "array",
      title: "Event types",
      // The same list as EVENT_TYPES in use-events.ts (event-feed.test.tsx holds them equal).
      items: { type: "string", enum: ["summary", "status_change", "context_update", "error", "dependency_resolved", "custom"] },
      "x-widget": "event-type-picker",
    },
    project_id: { type: "string", title: "Project", "x-widget": "project-picker" },
    actor_type: { type: "string", title: "Actor", enum: ["all", "agent", "user", "system"], default: "all" },
    period: { title: "Period", "x-widget": "period" },
    auto_scroll: { type: "boolean", title: "Auto-scroll", default: true },
    auto_refresh: { type: "boolean", title: "Update automatically", default: true },
  },
  additionalProperties: false,
} as const;
