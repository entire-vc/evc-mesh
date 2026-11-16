// The widgets of the pre-registry dashboard, moved here as they were: same views (views.tsx, shipped with the host,
// see registry.ts), same data hooks. `my-tasks` lives in the tasks group now (its own settings, server query, view).
// Sizes and categories are the core catalog's (§5.8): "triage" and "sessions" are the pre-dashboards ids of
// triage-queue and agents-active and take theirs.
import type { WidgetDescriptor } from "../../types";
import { coreCategory, coreSize } from "../../core-catalog";

const core = (id: string) => ({ id, category: coreCategory(id), size: coreSize(id), origin: "core" }) as const;

export const descriptors: WidgetDescriptor[] = [
  { ...core("mentions"), title: "Mentions", description: "Mentions of you in tasks and documents", scope: "user", dataHook: "useMentions", actions: ["openMention", "retry"] },
  { ...core("triage"), title: "Triage", description: "Cards waiting for a human", scope: "workspace", dataHook: "useTriageTasks", actions: ["openTask", "retry"] },
  { ...core("sessions"), title: "Sessions", description: "Agents online now", scope: "workspace", dataHook: "useActiveSessions", actions: ["openAgent", "retry"] },
];
