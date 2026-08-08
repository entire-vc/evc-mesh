/**
 * board-view-storage.ts — persists the kanban board's grouping/sort/filter
 * selections per project so they survive a page reload or navigating away
 * and back (e.g. opening a task and returning to the board).
 */

import type { CFFilters } from "@/components/view-filters";

const STORAGE_PREFIX = "mesh_board_filters_";

export interface BoardFilterState {
  groupBy: string;
  sortBy: string;
  showClosed: boolean;
  showSubtasks: boolean;
  searchQuery: string;
  priorityFilter: string;
  assigneeFilter: string;
  customFieldFilters: Record<string, unknown>;
  selectedTags: string[];
  cfFilters: CFFilters;
  assigneeIdsFilter: string[];
}

export function loadBoardFilters(projectId: string): Partial<BoardFilterState> | null {
  try {
    const raw = localStorage.getItem(STORAGE_PREFIX + projectId);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return null;
    return parsed as Partial<BoardFilterState>;
  } catch {
    return null;
  }
}

export function saveBoardFilters(projectId: string, state: BoardFilterState): void {
  try {
    localStorage.setItem(STORAGE_PREFIX + projectId, JSON.stringify(state));
  } catch {
    // localStorage unavailable (private browsing quota, disabled, etc.) — filters
    // simply won't persist this session, which is a safe degradation.
  }
}
