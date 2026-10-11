import { api } from "@/lib/api";
import type { PaginatedResponse, Task } from "@/types";

class IncompleteTaskList extends Error {
  constructor() {
    super("The task list changed while loading. Please try again.");
  }
}

// A page walk is a snapshot, never an append to the visible board. The API
// checks revision before querying each page, so check once more AFTER the last
// page to catch a write between that check and the page query.
export async function fetchCompleteTaskList(
  projectId: string,
  filters: Record<string, string | number | undefined> | undefined,
  isCurrent: () => boolean,
): Promise<PaginatedResponse<Task> | null> {
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const items = new Map<string, Task>();
      let total: number | undefined;
      let revision: number | undefined;
      let first!: PaginatedResponse<Task>;
      for (let page = 1; ; page++) {
        if (!isCurrent()) return null;
        const data = await api<PaginatedResponse<Task>>(`/api/v1/projects/${projectId}/tasks`, {
          params: { ...filters, page_size: "200", include_description: "false",
            page, list_revision: page === 1 || !revision ? undefined : revision },
        });
        if (!isCurrent()) return null;
        const count = data.total_count ?? data.total;
        if (!Number.isSafeInteger(count) || count < 0 || data.page !== page ||
            (total !== undefined && count !== total) ||
            (page > 1 && data.list_revision !== revision)) throw new IncompleteTaskList();
        if (page === 1) {
          first = data;
          total = count;
          revision = data.list_revision;
        }
        for (const task of data.items ?? []) {
          if (task.project_id !== projectId || items.has(task.id)) throw new IncompleteTaskList();
          items.set(task.id, task);
        }
        if (items.size > count) throw new IncompleteTaskList();
        if (!data.has_more) {
          if (items.size !== count) throw new IncompleteTaskList();
          if (page > 1) {
            const check = await api<PaginatedResponse<Task>>(`/api/v1/projects/${projectId}/tasks`, {
              params: { ...filters, page: 1, page_size: 1, include_description: "false", list_revision: revision || undefined },
            });
            if (!isCurrent()) return null;
            if (check.list_revision !== revision || (check.total_count ?? check.total) !== count) {
              throw new IncompleteTaskList();
            }
          }
          return { ...first, items: [...items.values()], has_more: false };
        }
        // Revision 0 is a real value (project not written since the counter
        // was backfilled): the backend cannot validate it, but any write bumps
        // it to >= 1, so the equality checks on every page and the final
        // check after the walk still detect a concurrent change.
        if (!data.items?.length || items.size >= count ||
            !Number.isSafeInteger(revision) || (revision as number) < 0) throw new IncompleteTaskList();
      }
    } catch (error) {
      if (!isCurrent()) return null;
      const stale = error instanceof IncompleteTaskList ||
        (typeof error === "object" && error !== null && "status" in error && error.status === 410);
      if (!stale || attempt === 2) throw error;
    }
  }
  return null;
}
