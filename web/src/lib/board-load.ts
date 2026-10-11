import { api } from "@/lib/api";
import type { PaginatedResponse, StatusCategory, Task, TaskStatus } from "@/types";

// How the Board loads a project's tasks.
//
// Open work (every status whose category is not done/cancelled) is loaded in
// full: it is what people act on, and it is small (Lab: a few hundred of its
// 4378 tasks). Closed statuses get a bounded slice — the newest
// BOARD_PAGE_SIZE cards by updated_at — plus the server's total_count, and
// more only when the user asks for it.
//
// Before this, the Board walked every page of the whole project behind a pinned
// list_revision and retried the walk on revision drift (!1104). With the fleet
// writing constantly, Lab's 22-page walk (18.7 s) drifted and restarted up to
// three times. A full-project walk must not come back to this hot path: the
// request count of a load is bounded by the open pages plus the closed pages
// the user explicitly loaded, and nothing here pins a revision.

/** Page size of every board request, open and closed (the API maximum). */
export const BOARD_PAGE_SIZE = 200;
export function isClosedCategory(category: StatusCategory): boolean {
  return category === "done" || category === "cancelled";
}

/** One closed status column: how much of it is loaded and how big it is. */
export interface ClosedSlice {
  /** Pages of BOARD_PAGE_SIZE loaded, newest first. */
  pages: number;
  /** Distinct cards loaded for this status. */
  loaded: number;
  /** Server total_count for this status (no board filters applied). */
  total: number;
  hasMore: boolean;
}

export interface BoardSnapshot {
  items: Task[];
  closed: Record<string, ClosedSlice>;
}

class BoardLoadError extends Error {}

function tasksUrl(projectId: string) {
  return `/api/v1/projects/${projectId}/tasks`;
}

function pageParams(statusIds: string[], page: number, oldestFirst: boolean) {
  return {
    // `status` takes comma-separated status UUIDs (listTasksQuery.Status in
    // internal/handler/task_handler.go). Callers never send an empty list:
    // the backend would read it as "no status filter" — the whole project.
    status: statusIds.join(","),
    page,
    page_size: String(BOARD_PAGE_SIZE),
    include_description: "false",
    // Closed slices use the handler's default, updated_at desc, t.id — the
    // "newest first" they need (NormalizeWithDefaultSort in List). Open work
    // pages by created_at asc instead: an update does not move a card across
    // a page border there, so a walk only drifts when cards enter or leave
    // the open statuses, which the count check in walkOpen catches.
    ...(oldestFirst ? { sort_by: "created_at", sort_dir: "asc" } : {}),
  };
}

const MALFORMED = "Malformed task list. Please try again.";

/** One page of `statusIds`, newest-updated first, checked for shape and project. */
async function getPage(projectId: string, statusIds: string[], page: number, oldestFirst = false) {
  const data = await api<PaginatedResponse<Task>>(tasksUrl(projectId), {
    params: pageParams(statusIds, page, oldestFirst),
  });
  const total = data.total_count ?? data.total;
  const items = data.items ?? [];
  if (!Number.isSafeInteger(total) || total < 0 || data.page !== page ||
      items.some((t) => t.project_id !== projectId)) throw new BoardLoadError(MALFORMED);
  return { items, total, hasMore: Boolean(data.has_more) };
}

/**
 * Every page of the open statuses, deduplicated by id. Not revision-pinned on
 * purpose (a pinned walk restarted on every fleet write, !1104). Paged oldest
 * first, so only a card entering or leaving the open statuses can shift a
 * page border; that changes total_count, and a walk whose totals moved, or
 * whose distinct cards do not add up to the total, is walked once more. A
 * second drifting walk is published as is: the write behind it also emits a
 * WS event, and the refresh after it reads the list again.
 */
async function walkOpen(projectId: string, statusIds: string[], isCurrent: () => boolean) {
  let items = new Map<string, Task>();
  for (let attempt = 0; attempt < 2; attempt++) {
    items = new Map();
    let total = 0, drifted = false;
    for (let page = 1; ; page++) {
      const data = await getPage(projectId, statusIds, page, true);
      if (!isCurrent()) return null;
      if (page > 1 && data.total !== total) drifted = true;
      total = data.total;
      for (const task of data.items) items.set(task.id, task);
      // has_more is the server's word; the item guard stops a server that
      // keeps saying has_more with empty pages from looping forever.
      if (!data.hasMore || !data.items.length) break;
    }
    if (!drifted && items.size === total) break;
  }
  return [...items.values()];
}

/** Pages 1..`pages` of one closed status, newest first, requested together. */
async function closedSlice(projectId: string, statusId: string, pages: number, isCurrent: () => boolean) {
  const all = await Promise.all(
    Array.from({ length: pages }, (_, i) => getPage(projectId, [statusId], i + 1)),
  );
  if (!isCurrent()) return null;
  const items = new Map<string, Task>();
  let n = 0;
  for (const data of all) {
    n++;
    for (const task of data.items) items.set(task.id, task);
    if (!data.hasMore) break;
  }
  const last = all[n - 1]!;
  const slice: ClosedSlice = { pages: n, loaded: items.size, total: all[0]!.total, hasMore: last.hasMore };
  return { items: [...items.values()], slice };
}

/**
 * Load the board for `statuses` (this project's). `closedPages` carries how many
 * pages of each closed status the user already loaded, so a refresh keeps the
 * slice they asked for instead of collapsing it to the first page.
 *
 * Request count: ceil(open / 200) for open work (one request when any open
 * status exists; open pages go one after another) + Σ closedPages (≥ 1 per
 * closed status, all pages of a status requested together).
 */
export async function fetchBoardSnapshot(
  projectId: string,
  statuses: Pick<TaskStatus, "id" | "category">[],
  closedPages: Record<string, number>,
  isCurrent: () => boolean,
): Promise<BoardSnapshot | null> {
  const openIds = statuses.filter((s) => !isClosedCategory(s.category)).map((s) => s.id);
  const closedIds = statuses.filter((s) => isClosedCategory(s.category)).map((s) => s.id);
  const [open, ...shut] = await Promise.all([
    openIds.length ? walkOpen(projectId, openIds, isCurrent) : [],
    ...closedIds.map((id) => closedSlice(projectId, id, Math.max(1, closedPages[id] ?? 1), isCurrent)),
  ]);
  if (!isCurrent() || !open || shut.some((c) => !c)) return null;
  const items = new Map<string, Task>();
  const closed: Record<string, ClosedSlice> = {};
  for (const task of open) items.set(task.id, task);
  shut.forEach((c, i) => {
    closed[closedIds[i]!] = c!.slice;
    // A card that moved between an open and a closed status while the
    // requests ran shows up in both answers: keep the first seen.
    for (const task of c!.items) if (!items.has(task.id)) items.set(task.id, task);
  });
  return { items: [...items.values()], closed };
}

/** Page `page` of one closed status ("load more"). */
export function fetchClosedPage(projectId: string, statusId: string, page: number) {
  return getPage(projectId, [statusId], page);
}
