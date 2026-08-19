import { api } from "@/lib/api";
import type { DocAnchor } from "@/lib/docs/anchor";
import type { ActorType, PaginatedResponse } from "@/types";

/**
 * Comments anchored to a range of text inside a document.
 *
 * The stored shape is the W3C Web Annotation selector pair — a TextQuoteSelector
 * (`exact`/`prefix`/`suffix`) and a TextPositionSelector (`start`/`end`) — which
 * is also what a paragraph link carries (lib/docs/anchor.ts). A comment range is
 * the general case; a linked paragraph is the range that covers one block.
 *
 * Two things about it are the server's contract and not ours to reinterpret:
 *
 *  - **`start`/`end` are nullable, together.** A quote with no position is an
 *    ORPHAN: we still know what the comment was written about, and no longer
 *    know where. That is a state a client WRITES, not only reads — it is how a
 *    reader that could not re-find the range says so rather than losing the
 *    comment.
 *  - **`orphaned` is computed on the way out**, never sent. It exists so a
 *    client cannot be told something the offsets contradict.
 *
 * Resolving an anchor against the current text is the client's job: the body is
 * here and already parsed, and a second resolver on the server would be free to
 * disagree with the one the reader can see.
 */
export interface DocumentCommentAnchor {
  exact: string;
  prefix: string;
  suffix: string;
  /** Null together when the anchor is orphaned. */
  start: number | null;
  end: number | null;
  /** Computed by the API. Never sent. */
  orphaned?: boolean;
}

export interface DocumentComment {
  id: string;
  document_id: string;
  parent_comment_id: string | null;
  body: string;
  /** Absent on a reply (it inherits its thread's anchor) and on a page-level note. */
  anchor?: DocumentCommentAnchor | null;
  resolved_at?: string | null;
  resolved_by?: string | null;
  resolved_by_type?: ActorType | null;
  author_id: string;
  author_type: ActorType;
  created_at: string;
  updated_at: string;
}

export interface CreateDocumentCommentRequest {
  body: string;
  anchor?: DocumentCommentAnchor;
  parent_comment_id?: string;
}

/** An anchor with a position, in the form the API takes. */
export function anchorPayload(anchor: DocAnchor): DocumentCommentAnchor {
  return {
    exact: anchor.exact,
    prefix: anchor.prefix,
    suffix: anchor.suffix,
    start: anchor.start,
    end: anchor.end,
  };
}

/** The position half, or null for an orphan. Callers resolve against it. */
export function positionOf(anchor: DocumentCommentAnchor | null | undefined): DocAnchor | null {
  if (!anchor || anchor.start === null || anchor.end === null) return null;
  return {
    start: anchor.start,
    end: anchor.end,
    exact: anchor.exact,
    prefix: anchor.prefix,
    suffix: anchor.suffix,
  };
}

// The list endpoint is paginated and a thread cannot be drawn from part of
// itself — a reply whose root landed on page 2 has nowhere to hang. So the
// client walks the pages, exactly as the document tree does, with the same cap
// so a backend that always answers has_more cannot spin here forever.
const PAGE_SIZE = 200;
const PAGE_LIMIT = 25;

/**
 * Every live comment of the document, oldest first.
 *
 * `include_resolved` is on: a resolved thread is still shown, behind a toggle,
 * and a client that never asked for them could not offer that without a second
 * round trip the moment anyone clicks.
 */
export async function listDocumentComments(
  documentId: string,
): Promise<DocumentComment[]> {
  const all: DocumentComment[] = [];
  for (let page = 1; page <= PAGE_LIMIT; page += 1) {
    const res = await api<PaginatedResponse<DocumentComment>>(
      `/api/v1/documents/${documentId}/comments`,
      { params: { page, page_size: PAGE_SIZE, include_resolved: "true" } },
    );
    all.push(...(res.items ?? []));
    if (!res.has_more) break;
  }
  return all;
}

export async function createDocumentComment(
  documentId: string,
  req: CreateDocumentCommentRequest,
): Promise<DocumentComment> {
  return api<DocumentComment>(`/api/v1/documents/${documentId}/comments`, {
    method: "POST",
    body: req,
  });
}

/** Edit the text of your own comment. The anchor is not editable. */
export async function updateDocumentComment(
  commentId: string,
  body: string,
): Promise<DocumentComment> {
  return api<DocumentComment>(`/api/v1/document-comments/${commentId}`, {
    method: "PATCH",
    body: { body },
  });
}

/**
 * Resolve or reopen a thread.
 *
 * Two endpoints rather than a boolean field, which is the API's shape: the verb
 * is in the URL, so a request cannot mean "resolve" and "not resolve" depending
 * on a field a serialiser might drop.
 */
export async function setDocumentCommentResolved(
  commentId: string,
  resolved: boolean,
): Promise<DocumentComment> {
  return api<DocumentComment>(
    `/api/v1/document-comments/${commentId}/${resolved ? "resolve" : "unresolve"}`,
    { method: "POST" },
  );
}

/** Soft-deletes the comment and its replies. */
export async function deleteDocumentComment(commentId: string): Promise<void> {
  await api<void>(`/api/v1/document-comments/${commentId}`, { method: "DELETE" });
}

/** A thread: the anchored root plus its replies, in the order they were written. */
export interface CommentThread {
  root: DocumentComment;
  replies: DocumentComment[];
}

/**
 * Group a flat comment list into threads.
 *
 * The API keeps threads one level deep — a reply to a reply is refused rather
 * than silently flattened — so this walks up to the root anyway rather than
 * assuming it. A depth the client cannot represent would show as a comment
 * present in the database and invisible in the panel: unanswerable, undeletable,
 * and impossible to diagnose from the UI.
 */
export function groupIntoThreads(comments: readonly DocumentComment[]): CommentThread[] {
  const byId = new Map(comments.map((c) => [c.id, c]));
  const threads = new Map<string, CommentThread>();

  for (const c of comments) {
    if (!c.parent_comment_id) threads.set(c.id, { root: c, replies: [] });
  }
  for (const c of comments) {
    if (!c.parent_comment_id) continue;
    let current = c;
    const seen = new Set<string>([c.id]);
    while (current.parent_comment_id) {
      const parent = byId.get(current.parent_comment_id);
      // A cycle cannot happen through the API, and a reply whose root is missing
      // means the root went without its subtree. Both are dropped rather than
      // looped on: the alternative is a hung tab.
      if (!parent || seen.has(parent.id)) break;
      seen.add(parent.id);
      current = parent;
    }
    threads.get(current.id)?.replies.push(c);
  }
  return Array.from(threads.values());
}
