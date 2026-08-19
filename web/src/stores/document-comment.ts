import { create } from "zustand";
import { api } from "@/lib/api";
import type {
  CreateDocumentCommentRequest,
  DocumentComment,
  PaginatedResponse,
} from "@/types";

function getErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed";
}

const PAGE_SIZE = 200;
const PAGE_LIMIT = 25;

interface DocumentCommentState {
  /** The document the loaded comments belong to. Null before the first load. */
  documentId: string | null;
  comments: DocumentComment[];
  isLoading: boolean;
  error: string | null;

  fetchComments: (documentId: string) => Promise<void>;
  createComment: (
    documentId: string,
    req: CreateDocumentCommentRequest,
  ) => Promise<DocumentComment>;
  updateComment: (id: string, body: string) => Promise<DocumentComment>;
  setResolved: (id: string, resolved: boolean) => Promise<DocumentComment>;
  deleteComment: (id: string) => Promise<void>;
  reset: () => void;
}

/** Oldest first, id as the tiebreak — the same order the API lists them in. */
function sorted(comments: DocumentComment[]): DocumentComment[] {
  return [...comments].sort((a, b) => {
    if (a.created_at !== b.created_at) return a.created_at < b.created_at ? -1 : 1;
    return a.id < b.id ? -1 : 1;
  });
}

export const useDocumentCommentStore = create<DocumentCommentState>((set, get) => ({
  documentId: null,
  comments: [],
  isLoading: false,
  error: null,

  /**
   * Load every comment on the document, resolved ones included.
   *
   * `include_resolved=true` unconditionally, and the "Show resolved" switch
   * filters the loaded list instead of re-querying. Two reasons: resolving a
   * thread with the server-side filter on would make it vanish from the rail
   * the instant you resolved it, leaving no way back to unresolve it; and the
   * count in the actions row has to know about resolved threads to say
   * "3 of 7 open" rather than just "3".
   */
  fetchComments: async (documentId: string) => {
    set({ isLoading: true, error: null, documentId });
    try {
      const all: DocumentComment[] = [];
      for (let page = 1; page <= PAGE_LIMIT; page++) {
        const res = await api<PaginatedResponse<DocumentComment>>(
          `/api/v1/documents/${documentId}/comments`,
          { params: { page, page_size: PAGE_SIZE, include_resolved: "true" } },
        );
        all.push(...(res.items ?? []));
        if (!res.has_more) break;
      }
      // A slow response for a document the reader has already left must not
      // overwrite the one they are looking at now.
      if (get().documentId !== documentId) return;
      set({ comments: sorted(all), isLoading: false, error: null });
    } catch (error) {
      if (get().documentId !== documentId) return;
      set({ comments: [], isLoading: false, error: getErrorMessage(error) });
    }
  },

  createComment: async (documentId, req) => {
    const created = await api<DocumentComment>(
      `/api/v1/documents/${documentId}/comments`,
      { method: "POST", body: req },
    );
    set((state) => ({ comments: sorted([...state.comments, created]) }));
    return created;
  },

  updateComment: async (id, body) => {
    const updated = await api<DocumentComment>(`/api/v1/document-comments/${id}`, {
      method: "PATCH",
      body: { body },
    });
    set((state) => ({
      comments: state.comments.map((c) => (c.id === id ? updated : c)),
    }));
    return updated;
  },

  setResolved: async (id, resolved) => {
    const updated = await api<DocumentComment>(
      `/api/v1/document-comments/${id}/${resolved ? "resolve" : "unresolve"}`,
      { method: "POST" },
    );
    set((state) => ({
      comments: state.comments.map((c) => (c.id === id ? updated : c)),
    }));
    return updated;
  },

  deleteComment: async (id) => {
    await api(`/api/v1/document-comments/${id}`, { method: "DELETE" });
    // The server's soft delete takes a thread root's replies with it, so the
    // local list drops them too — otherwise they survive on screen as answers
    // to a comment that is no longer there.
    set((state) => ({
      comments: state.comments.filter(
        (c) => c.id !== id && c.parent_comment_id !== id,
      ),
    }));
  },

  reset: () =>
    set({ documentId: null, comments: [], isLoading: false, error: null }),
}));

/** A thread: its first comment and the replies to it, oldest first. */
export interface DocumentCommentThread {
  root: DocumentComment;
  replies: DocumentComment[];
}

/**
 * Group a flat comment list into threads.
 *
 * A reply whose root is missing is promoted to a thread of its own rather than
 * dropped: the server never produces that state, and inventing a way to lose
 * somebody's words if it ever did is not worth the two lines it saves.
 */
export function toThreads(comments: DocumentComment[]): DocumentCommentThread[] {
  const roots = comments.filter((c) => !c.parent_comment_id);
  const rootIds = new Set(roots.map((c) => c.id));
  const orphanedReplies = comments.filter(
    (c) => c.parent_comment_id && !rootIds.has(c.parent_comment_id),
  );

  const byParent = new Map<string, DocumentComment[]>();
  for (const c of comments) {
    if (!c.parent_comment_id || !rootIds.has(c.parent_comment_id)) continue;
    const list = byParent.get(c.parent_comment_id);
    if (list) list.push(c);
    else byParent.set(c.parent_comment_id, [c]);
  }

  return [...roots, ...orphanedReplies].map((root) => ({
    root,
    replies: byParent.get(root.id) ?? [],
  }));
}
