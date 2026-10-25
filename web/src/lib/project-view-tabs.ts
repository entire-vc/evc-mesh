import {
  Calendar,
  Columns3,
  FileText,
  GitBranch,
  List,
  MessageSquare,
  NotebookText,
  Puzzle,
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
  /** Literal title (an extension's own); wins over `labelKey`. */
  label?: string;
  Icon: LucideIcon;
  path: (wsSlug: string, projectSlug: string) => string;
  /**
   * Who leaves the strip first when it is too narrow: the lowest rank goes
   * into the "…" menu before the others. Ties fall back to list order, last
   * tab first.
   */
  overflowRank: number;
}

// Every route is `/w/<ws>/p/<proj>/<segment>` and every label `viewTabs.tabs.<id>`;
// the board is the project's bare path.
const builtin = (
  id: string,
  Icon: LucideIcon,
  overflowRank: number,
): ProjectViewTabDescriptor => ({
  id,
  labelKey: `viewTabs.tabs.${id}`,
  Icon,
  path: (ws, proj) => `/w/${ws}/p/${proj}${id === "board" ? "" : `/${id}`}`,
  overflowRank,
});

export const BUILTIN_PROJECT_TABS: readonly ProjectViewTabDescriptor[] = [
  builtin("board", Columns3, 30),
  builtin("list", List, 20),
  builtin("timeline", GitBranch, 15),
  builtin("calendar", Calendar, 0),
  builtin("docs", NotebookText, 10),
  builtin("updates", FileText, 5),
];

/**
 * The Chat with Agent tab. Not in BUILTIN_PROJECT_TABS: the strip adds it only
 * for a project that turned the extension on.
 */
export const CHAT_PROJECT_TAB = builtin("chat", MessageSquare, 5);

/**
 * The strip of a project: the built-in tabs, Chat where the project turned it
 * on, and one tab per extension that is enabled and declares a screen. A
 * disabled extension is not in `screens`, so it has no tab.
 */
export function projectTabsWith(
  screens: ReadonlyArray<{ extensionId: string; title: string }>,
  chatEnabled = false,
): readonly ProjectViewTabDescriptor[] {
  if (screens.length === 0 && !chatEnabled) return BUILTIN_PROJECT_TABS;
  return [
    ...BUILTIN_PROJECT_TABS,
    ...(chatEnabled ? [CHAT_PROJECT_TAB] : []),
    ...screens.map(({ extensionId, title }): ProjectViewTabDescriptor => ({
      id: `ext:${extensionId}`,
      labelKey: "viewTabs.tabs.extension",
      label: title,
      Icon: Puzzle,
      path: (ws, proj) => `/w/${ws}/p/${proj}/x/${extensionId}`,
      overflowRank: 8,
    })),
  ];
}

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
