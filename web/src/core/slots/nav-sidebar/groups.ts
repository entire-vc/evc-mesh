import type { NavItem } from "./types";

/**
 * A theme view written before `dashboards-top` / `dashboards-bottom` knows only the four old groups and would
 * silently drop the dashboards. For such a view they are folded into `top`, in menu order (top, then bottom).
 */
export function normalizeNavGroups(items: NavItem[], viewKnowsDashboardGroups: boolean): NavItem[] {
  if (viewKnowsDashboardGroups) return items;
  return items.map((i) => (i.group === "dashboards-top" || i.group === "dashboards-bottom" ? { ...i, group: "top" as const } : i));
}
