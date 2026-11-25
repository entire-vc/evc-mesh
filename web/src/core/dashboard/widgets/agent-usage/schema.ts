// Settings schema of the `agent-usage` widget: the same keys the page's URL carries (state.ts), so an instance
// stores exactly the slice its link would. Data only: no runtime module enters the registry chunk.
import type { WidgetSettingsSchema } from "../../types";

const DIM_OPTIONS = { model: "Model", provider: "Provider", account: "Pool / account", agent: "Agent", project: "Project", workspace: "Workspace" };
const VIEW_OPTIONS = { cost: "≈ $ at API prices", tokens: "Tokens", quota: "Subscription limits", turns: "Turns", duration: "Duration", cards_done: "Cards done" };

export const AGENT_USAGE_SETTINGS_SCHEMA: WidgetSettingsSchema = {
  type: "object",
  properties: {
    m: { type: "string", title: "Show", enum: ["cost", "tokens", "quota", "turns", "duration", "cards_done"], "x-options": VIEW_OPTIONS, default: "cost" },
    g: { type: "string", title: "Breakdown", enum: ["model", "provider", "account", "agent", "project", "workspace"], "x-options": DIM_OPTIONS, default: "model" },
    s: { type: "string", title: "Inside", enum: ["model", "provider", "account", "agent", "project", "workspace"], "x-options": DIM_OPTIONS },
    t: { type: "string", title: "Tokens", enum: ["total", "in", "out", "cache_read", "cache_write"], "x-options": { total: "All tokens", in: "Input", out: "Output", cache_read: "Cache read", cache_write: "Cache write" }, default: "total" },
    p: { type: "string", title: "Period", description: "1d, 7d, 30d or YYYY-MM-DD..YYYY-MM-DD", default: "7d" },
    w: { type: "string", title: "Limit window", enum: ["5h", "week", "month", "budget"], "x-options": { "5h": "5 hours", week: "Week", month: "Month", budget: "Budget" }, default: "week" },
    n: { type: "integer", title: "Rows", minimum: 1, maximum: 20, default: 8 },
  },
};
