import type { Permission } from "@/core/primitives";
import type { DashboardLayoutSettings, WidgetDescriptor, WidgetSize } from "./types";

const SIZE = {
  default: { w: 2, h: 2 } as WidgetSize,
  min: { w: 1, h: 1 } as WidgetSize,
  max: { w: 4, h: 3 } as WidgetSize,
};

/** Core registry v1: array order is the default order. */
export const CORE_WIDGETS: readonly WidgetDescriptor[] = [
  { id: "mentions", title: "Mentions", description: "Mentions of you in tasks and documents", origin: "core", dataHook: "useMentions", actions: ["openMention", "retry"], size: SIZE },
  { id: "triage", title: "Triage", description: "Cards waiting for a human", origin: "core", dataHook: "useTriageTasks", actions: ["openTask", "retry"], size: SIZE },
  { id: "my-tasks", title: "My tasks", description: "Tasks assigned to you", origin: "core", dataHook: "useMyTasks", actions: ["openTask", "retry"], size: SIZE },
  { id: "sessions", title: "Sessions", description: "Agents online now", origin: "core", dataHook: "useActiveSessions", actions: ["openAgent", "retry"], size: SIZE },
];

export function clampSize(size: WidgetSize, d: WidgetDescriptor): WidgetSize {
  const c = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
  return {
    w: c(size.w, d.size.min.w, d.size.max.w) as WidgetSize["w"],
    h: c(size.h, d.size.min.h, d.size.max.h) as WidgetSize["h"],
  };
}

/**
 * Which widgets show, in which order: the theme's `widgets` setting picks and
 * orders (unknown ids are skipped, not fatal), then the user's permissions
 * filter. No setting = every registered widget in registry order.
 */
export function resolveWidgets(opts: {
  settings?: DashboardLayoutSettings;
  registry?: readonly WidgetDescriptor[];
  can?: (p: Permission) => boolean;
}): { shown: WidgetDescriptor[]; available: WidgetDescriptor[] } {
  const registry = opts.registry ?? CORE_WIDGETS;
  const can = opts.can ?? (() => true);
  const allowed = registry.filter((d) => (d.requires ?? []).every(can));
  const picked = opts.settings?.widgets;
  if (!picked) return { shown: allowed, available: [] };
  const byId = new Map(allowed.map((d) => [d.id, d]));
  const shown: WidgetDescriptor[] = [];
  for (const id of picked) {
    const d = byId.get(id);
    if (d && !shown.includes(d)) shown.push(d);
  }
  return { shown, available: allowed.filter((d) => !shown.includes(d)) };
}
