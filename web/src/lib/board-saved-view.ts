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
import type { CFFilters } from "@/components/view-filters";

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
