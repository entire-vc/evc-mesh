/**
 * The @-mention inbox, read from both places mentions actually live.
 *
 * ## Why this module exists
 *
 * Mentions are stored in two tables — one for task comments, one for document
 * comments — behind two endpoints, `/me/mentions` and `/me/document-mentions`.
 * That split is deliberate on the server (see the note above the routes in
 * `cmd/api/main.go`): the two rows name different objects, and a shared
 * response shape would need a nullable `task_id` that three screens currently
 * read as always present.
 *
 * The split was not supposed to reach the reader. It did: every screen showing
 * mentions read only the task endpoint, so a document mention was delivered by
 * the bell and simultaneously denied by the Mentions tab — "someone tagged you
 * twice" and "nobody has tagged you" on one screen. The merge therefore happens
 * here, on the client, where it costs one request and no server contract.
 *
 * ## An unanswered source is not an empty inbox
 *
 * `fetchMentionInbox` reports which sources failed instead of returning a
 * shorter list. This is the whole point: the defect being fixed was a screen
 * that answered "no mentions" when it simply had not looked, and a merge that
 * swallowed a failed request would rebuild that same lie one layer down. A
 * caller must not render the reassuring empty state while `failed` is
 * non-empty.
 */
import { api } from "@/lib/api";
import type { DocumentMention, Mention } from "@/types";

/** Which inbox a row came from. Also the field a reader branches on. */
export type MentionSource = "task" | "document";

/** The fields both inboxes have, so a card can render without branching. */
interface MentionInboxCommon {
  comment_id: string;
  extracted_at: string;
  seen_at: string | null;
  project_id: string;
  comment_body: string;
  author_id: string;
  author_name: string;
  /** What the mention is on — a task title or a document title. */
  title: string;
}

export interface TaskMentionItem extends MentionInboxCommon {
  source: "task";
  task_id: string;
}

export interface DocumentMentionItem extends MentionInboxCommon {
  source: "document";
  document_id: string;
  document_slug: string;
}

export type MentionInboxItem = TaskMentionItem | DocumentMentionItem;

export interface MentionInbox {
  items: MentionInboxItem[];
  /**
   * Sources that did not answer. Non-empty means `items` is incomplete, and
   * "no mentions" must not be shown as if it were the whole truth.
   */
  failed: MentionSource[];
}

const TASK_MENTIONS_PATH = "/api/v1/me/mentions";
const DOCUMENT_MENTIONS_PATH = "/api/v1/me/document-mentions";

export function toTaskMentionItem(m: Mention): TaskMentionItem {
  return {
    source: "task",
    comment_id: m.comment_id,
    extracted_at: m.extracted_at,
    seen_at: m.seen_at,
    project_id: m.project_id,
    comment_body: m.comment_body,
    author_id: m.author_id,
    author_name: m.author_name,
    title: m.task_title,
    task_id: m.task_id,
  };
}

export function toDocumentMentionItem(m: DocumentMention): DocumentMentionItem {
  return {
    source: "document",
    comment_id: m.comment_id,
    extracted_at: m.extracted_at,
    seen_at: m.seen_at,
    project_id: m.project_id,
    comment_body: m.comment_body,
    author_id: m.author_id,
    author_name: m.author_name,
    title: m.document_title,
    document_id: m.document_id,
    document_slug: m.document_slug,
  };
}

/**
 * Unseen first, then newest first.
 *
 * The same order the Mentions tab has always used for task mentions, kept so
 * that adding a second source changes what is on the list and not how it reads.
 * The comparison is by time, not by source: a document mention from an hour ago
 * belongs above a task mention from last week, and grouping by source would
 * bury whichever kind the reader happens to get less of.
 */
export function sortMentionInbox(items: MentionInboxItem[]): MentionInboxItem[] {
  return [...items].sort((a, b) => {
    if (!a.seen_at && b.seen_at) return -1;
    if (a.seen_at && !b.seen_at) return 1;
    return new Date(b.extracted_at).getTime() - new Date(a.extracted_at).getTime();
  });
}

/**
 * Fetch both inboxes concurrently and merge them.
 *
 * Neither request can fail the other: a document-mentions endpoint that is
 * missing or erroring must not blank out task mentions that loaded fine, and
 * vice versa. What it does instead is name itself in `failed`.
 */
export async function fetchMentionInbox(limit = 50): Promise<MentionInbox> {
  const [tasks, documents] = await Promise.allSettled([
    api<Mention[]>(TASK_MENTIONS_PATH, { params: { limit } }),
    api<DocumentMention[]>(DOCUMENT_MENTIONS_PATH, { params: { limit } }),
  ]);

  const items: MentionInboxItem[] = [];
  const failed: MentionSource[] = [];

  if (tasks.status === "fulfilled") {
    for (const m of tasks.value ?? []) items.push(toTaskMentionItem(m));
  } else {
    failed.push("task");
  }

  if (documents.status === "fulfilled") {
    for (const m of documents.value ?? []) items.push(toDocumentMentionItem(m));
  } else {
    failed.push("document");
  }

  return { items: sortMentionInbox(items), failed };
}

/** Mark one mention seen, on whichever inbox owns it. */
export async function markMentionSeen(item: MentionInboxItem): Promise<void> {
  const base = item.source === "task" ? TASK_MENTIONS_PATH : DOCUMENT_MENTIONS_PATH;
  await api(`${base}/${item.comment_id}/seen`, { method: "POST" });
}

/**
 * Where clicking a mention should go.
 *
 * Returns null when the project it belongs to is not among the ones loaded —
 * the route is built from the project's slug, and guessing one would produce a
 * link to nothing.
 */
export function mentionHref(
  item: MentionInboxItem,
  wsSlug: string | undefined,
  projectSlug: string | undefined,
): string | null {
  if (!wsSlug || !projectSlug) return null;
  const base = `/w/${wsSlug}/p/${projectSlug}`;
  // The comment id rides in the query on both branches, so the destination
  // page can focus that thread on arrival — task-panel.tsx reads it the same
  // way docs.tsx already did. The hash is already taken by paragraph anchors
  // on the document side (D6), which is why this is a query param there too.
  if (item.source === "task") return `${base}/t/${item.task_id}?comment=${item.comment_id}`;
  return `${base}/docs/${item.document_id}?comment=${item.comment_id}`;
}

/** Combined unseen count across both inboxes, for the sidebar badge. */
export async function fetchUnseenMentionCount(): Promise<number> {
  const [tasks, documents] = await Promise.allSettled([
    api<{ count: number }>(`${TASK_MENTIONS_PATH}/unseen_count`),
    api<{ count: number }>(`${DOCUMENT_MENTIONS_PATH}/unseen_count`),
  ]);
  let total = 0;
  if (tasks.status === "fulfilled") total += tasks.value?.count ?? 0;
  if (documents.status === "fulfilled") total += documents.value?.count ?? 0;
  return total;
}
