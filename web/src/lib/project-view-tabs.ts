import {
  Calendar,
  Columns3,
  List,
  NotebookText,
  type LucideIcon,
} from "lucide-react";

/**
 * One tab of the project strip. The strip renders whatever list it is given,
 * so a new tab (an extension's, say) is a new entry here or in a caller's
 * list, never an edit to the component.
 */
export interface ProjectViewTabDescriptor {
  id: string;
  /** i18n key in the `settings` namespace. */
  labelKey: string;
  Icon: LucideIcon;
  path: (wsSlug: string, projectSlug: string) => string;
  /**
   * Who leaves the strip first when it is too narrow: the lowest rank goes
   * into the "…" menu before the others. Ties fall back to list order, last
   * tab first.
   */
  overflowRank: number;
}

export const BUILTIN_PROJECT_TABS: readonly ProjectViewTabDescriptor[] = [
  {
    id: "board",
    labelKey: "viewTabs.tabs.board",
    Icon: Columns3,
    path: (ws, proj) => `/w/${ws}/p/${proj}`,
    overflowRank: 30,
  },
  {
    id: "list",
    labelKey: "viewTabs.tabs.list",
    Icon: List,
    path: (ws, proj) => `/w/${ws}/p/${proj}/list`,
    overflowRank: 20,
  },
  {
    id: "calendar",
    labelKey: "viewTabs.tabs.calendar",
    Icon: Calendar,
    path: (ws, proj) => `/w/${ws}/p/${proj}/calendar`,
    overflowRank: 0,
  },
  {
    id: "docs",
    labelKey: "viewTabs.tabs.docs",
    Icon: NotebookText,
    path: (ws, proj) => `/w/${ws}/p/${proj}/docs`,
    overflowRank: 10,
  },
];

/**
 * Splits `tabs` into the ones that stay in the strip and the ones that go to
 * the "…" menu, given each tab's natural width and the room available.
 * Both results keep the original list order.
 */
export function splitTabsByWidth(
  tabs: readonly ProjectViewTabDescriptor[],
  widths: readonly number[],
  available: number,
): { visible: ProjectViewTabDescriptor[]; overflow: ProjectViewTabDescriptor[] } {
  // Leave order: lowest rank first; on a tie, the later tab.
  const order = tabs
    .map((_, i) => i)
    .sort((a, b) => (tabs[a]?.overflowRank ?? 0) - (tabs[b]?.overflowRank ?? 0) || b - a);
  const hidden = new Set<number>();
  let total = widths.reduce((sum, w) => sum + w, 0);
  for (const i of order) {
    if (total <= available) break;
    hidden.add(i);
    total -= widths[i] ?? 0;
  }
  return {
    visible: tabs.filter((_, i) => !hidden.has(i)),
    overflow: tabs.filter((_, i) => hidden.has(i)),
  };
}
