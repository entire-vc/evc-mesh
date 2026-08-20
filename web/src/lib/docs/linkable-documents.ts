import { api } from "@/lib/api";
import type { LinkableDocument } from "@/lib/docs/doc-link";
import type { PaginatedResponse, ProjectDocument } from "@/types";

/**
 * The documents a task description or comment can link to: the ones in the same
 * project.
 *
 * Fetched here rather than read from the document store, which holds whichever
 * project the Docs page last opened. A task panel reaching into that store would
 * either show another project's documents or clobber the page's state on the way
 * past; neither is worth saving forty lines.
 *
 * Scope is the task's own project, and titles only. Searching document CONTENT,
 * and across scopes, is D9 — half of it built here would be a second search
 * giving different answers to the same question.
 */

// The list endpoint pages at 200; the cap stops a backend that always answers
// has_more from spinning here forever. Same shape as the document tree's walk.
const PAGE_SIZE = 200;
const PAGE_LIMIT = 25;

/**
 * Cached for the same 30 seconds the mentionables endpoint asks to be cached
 * for. A picker that refetched the project's documents on every `[[` would put a
 * request behind a keystroke; one that never refetched would go on offering a
 * document somebody deleted.
 */
const TTL_MS = 30_000;

interface CacheEntry {
  at: number;
  documents: LinkableDocument[];
}

const cache = new Map<string, CacheEntry>();

/** Drop the cache. Exported for tests, and for a caller that just created a doc. */
export function forgetLinkableDocuments(projectId?: string): void {
  if (projectId) cache.delete(projectId);
  else cache.clear();
}

export async function fetchLinkableDocuments(
  projectId: string,
  now: number = Date.now(),
): Promise<LinkableDocument[]> {
  const hit = cache.get(projectId);
  if (hit && now - hit.at < TTL_MS) return hit.documents;

  const documents: LinkableDocument[] = [];
  for (let page = 1; page <= PAGE_LIMIT; page += 1) {
    const res = await api<PaginatedResponse<ProjectDocument>>(
      `/api/v1/projects/${projectId}/documents`,
      { params: { page, page_size: PAGE_SIZE } },
    );
    for (const doc of res.items ?? []) {
      documents.push({ id: doc.id, title: doc.title });
    }
    if (!res.has_more) break;
  }

  cache.set(projectId, { at: now, documents });
  return documents;
}
