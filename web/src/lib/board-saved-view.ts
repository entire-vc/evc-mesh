/**
 * board-saved-view.ts — maps the board's toolbar state to and from the
 * saved-view payload (`SavedView.filters` + `sort_by`).
 *
 * Kept as two pure, mutually symmetric functions so the capture and restore
 * sides cannot silently drift apart: whatever `buildBoardSavedViewState` puts
 * into the payload must come back out of `readBoardSavedViewState`. That drift
 * *is* the bug this pair exists to prevent — tags, grouping, sort, show_closed
 * and show_subtasks were all selectable in the toolbar but dropped on save,
 * and the assignee-ids filter has to ride along too.
 *
 * `custom_fields` carries `cfFilters` (the filter that actually drives
 * filteredTasks via applyViewFilters), not the unused `customFieldFilters`
 * state, which is dead — board-toolbar.tsx never reads it.
 */

import type { GroupBy, SortBy } from "@/components/board-toolbar";
import { countActiveCFFilters, type CFFilters } from "@/components/view-filters";

export const GROUP_BY_VALUES: GroupBy[] = ["status", "priority", "assignee"];
export const SORT_BY_VALUES: SortBy[] = [
  "manual",
  "updated",
  "priority",
  "due_date",
  "created",
  "title",
];

/** The slice of board toolbar state a saved view round-trips. */
export interface BoardSavedViewState {
  searchQuery: string;
  priorityFilter: string;
  assigneeFilter: string;
  assigneeIdsFilter: string[];
  cfFilters: CFFilters;
  selectedTags: string[];
  groupBy: GroupBy;
  showClosed: boolean;
  showSubtasks: boolean;
  sortBy: SortBy;
}

/** Restored state. `sortBy` is absent when the view carries no `sort_by`, in
 *  which case the board keeps whatever sort is currently active. */
export type RestoredBoardViewState = Omit<BoardSavedViewState, "sortBy"> & {
  sortBy?: SortBy;
};

export function buildBoardSavedViewState(state: BoardSavedViewState): {
  filters: Record<string, unknown>;
  sortBy: SortBy;
} {
  return {
    filters: {
      search: state.searchQuery,
      priority: state.priorityFilter,
      assignee: state.assigneeFilter,
      assignee_ids: state.assigneeIdsFilter,
      custom_fields: state.cfFilters,
      tags: state.selectedTags,
      group_by: state.groupBy,
      show_closed: state.showClosed,
      show_subtasks: state.showSubtasks,
    },
    sortBy: state.sortBy,
  };
}

function asStringArray(value: unknown): string[] {
  return Array.isArray(value) ? (value as string[]) : [];
}

export function readBoardSavedViewState(
  filters: Record<string, unknown>,
  sortBy?: string | null,
): RestoredBoardViewState {
  return {
    searchQuery: (filters.search as string) ?? "",
    priorityFilter: (filters.priority as string) ?? "all",
    assigneeFilter: (filters.assignee as string) ?? "all",
    assigneeIdsFilter: asStringArray(filters.assignee_ids),
    cfFilters: (filters.custom_fields as CFFilters) ?? {},
    selectedTags: asStringArray(filters.tags),
    groupBy: GROUP_BY_VALUES.includes(filters.group_by as GroupBy)
      ? (filters.group_by as GroupBy)
      : "status",
    showClosed: Boolean(filters.show_closed),
    showSubtasks: Boolean(filters.show_subtasks),
    ...(sortBy && SORT_BY_VALUES.includes(sortBy as SortBy)
      ? { sortBy: sortBy as SortBy }
      : {}),
  };
}

/**
 * Default value for every FILTER field (excludes `groupBy`/`sortBy` — those
 * are the board's layout, not a filter, and "Reset filters" must not touch
 * them). Drawn from the same `BoardSavedViewState` shape so a future filter
 * added to that type doesn't get silently left out of a reset.
 */
export const BOARD_FILTER_DEFAULTS: Omit<BoardSavedViewState, "groupBy" | "sortBy"> = {
  searchQuery: "",
  priorityFilter: "all",
  assigneeFilter: "all",
  assigneeIdsFilter: [],
  cfFilters: {},
  selectedTags: [],
  showClosed: false,
  showSubtasks: false,
};

/** True when a saved-view-shaped filters payload matches the reset defaults
 *  — i.e. there is nothing left for "Reset filters" to do. */
export function isBoardFiltersAtDefault(filters: Record<string, unknown>): boolean {
  const restored = readBoardSavedViewState(filters);
  return (
    restored.searchQuery === BOARD_FILTER_DEFAULTS.searchQuery &&
    restored.priorityFilter === BOARD_FILTER_DEFAULTS.priorityFilter &&
    restored.assigneeFilter === BOARD_FILTER_DEFAULTS.assigneeFilter &&
    restored.assigneeIdsFilter.length === 0 &&
    countActiveCFFilters(restored.cfFilters) === 0 &&
    restored.selectedTags.length === 0 &&
    restored.showClosed === BOARD_FILTER_DEFAULTS.showClosed &&
    restored.showSubtasks === BOARD_FILTER_DEFAULTS.showSubtasks
  );
}
