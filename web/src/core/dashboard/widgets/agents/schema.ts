// Settings JSON Schemas of the agent widgets: data the registry chunk carries for index.ts.
// Contract C: index.ts imports only files like this one, with no runtime imports (no hooks, no React). Nothing
// else imports it: the bounds and statuses repeat ACTIVE_LIMIT and AGENT_STATUSES from use-agents.ts, and
// agents-widgets.test.tsx holds them equal.

/** agents-active: how many agents the card lists. */
export const AGENTS_ACTIVE_SETTINGS_SCHEMA = {
  type: "object",
  properties: {
    limit: { type: "integer", title: "Limit", minimum: 1, maximum: 50, default: 5 },
  },
};

/** agents-status: a status filter (stale agents count as offline) and agents with a current task only. */
export const AGENTS_STATUS_SETTINGS_SCHEMA = {
  type: "object",
  properties: {
    statuses: {
      type: "array",
      title: "Statuses",
      description: "Stale agents count as offline. None picked shows all.",
      items: {
        type: "string",
        enum: ["online", "busy", "offline", "error"],
        "x-options": { online: "Online", busy: "Busy", offline: "Offline", error: "Error" },
      },
      uniqueItems: true,
      default: [],
    },
    onlyWithTask: { type: "boolean", title: "Only agents with a task", default: false },
  },
};
