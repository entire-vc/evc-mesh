// Data hooks of the «Коммуникации» widgets: mentions, my-comments, recent-comments, unseen counter.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useParams } from "react-router";
import { api } from "@/lib/api";
import { fetchMentionInbox, fetchUnseenMentionCount, mentionHref, type MentionInboxItem } from "@/lib/mentions/inbox";
import { useRealtime } from "@/core/primitives/use-realtime";
import { useAuthStore } from "@/stores/auth";
import { useWebSocketStore } from "@/stores/websocket";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { CommentView, CommentViewPage } from "@/types";
import { numberSetting, useWidgetConfig } from "../../config";
import type { Async, CommentItem, CommentsData, MentionItem, MentionsData } from "../../types";
import { readLastVisit, readShownIds, saveShownIds, writeLastVisit } from "./storage";

/** Fired by a widget action after a mention was marked seen on the server. */
export const MENTION_SEEN_EVENT = "mesh:mention:seen";
const LAST_VISIT_DELAY_MS = 5_000; // the «Новое» label must stay visible long enough to be read
const FETCH_ERROR = (e: unknown) => (e instanceof Error ? e : new Error(String(e)));

type Loaded<T> = { key: string; value: T } | { key: string; error: Error } | null;

/**
 * Loads `load(wsId)` for the current workspace. `reload()` refetches in place (no loading flash, an error on a
 * background refresh keeps the rows already shown); `retry()` is the error-state button.
 */
function useReloadable<T>(key: string, load: (wsId: string) => Promise<T>) {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const [loaded, setLoaded] = useState<Loaded<T>>(null);
  const [attempt, setAttempt] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  const fullKey = `${wsId}|${key}`;
  const keyRef = useRef(fullKey);
  keyRef.current = fullKey;

  const run = useCallback(
    (silent: boolean) => {
      if (!wsId) return () => {};
      let live = true;
      const k = `${wsId}|${key}`;
      loadRef.current(wsId).then(
        (value) => live && keyRef.current === k && setLoaded({ key: k, value }),
        (e) => live && keyRef.current === k && (silent ? undefined : setLoaded({ key: k, error: FETCH_ERROR(e) })),
      );
      return () => {
        live = false;
      };
    },
    [wsId, key],
  );

  useEffect(() => run(false), [run, attempt]);
  const reload = useCallback(() => void run(true), [run]);
  const retry = useCallback(() => {
    setLoaded(null);
    setAttempt((n) => n + 1);
  }, []);
  const patch = useCallback((fn: (v: T) => T) => setLoaded((l) => (l && "value" in l ? { ...l, value: fn(l.value) } : l)), []);
  return { loaded: loaded && loaded.key === fullKey ? loaded : null, reload, retry, patch };
}

function toAsync<T>(loaded: Loaded<T>, retry: () => void): Async<T> {
  if (!loaded) return { state: "loading" };
  if ("error" in loaded) return { state: "error", error: loaded.error, retry };
  return { state: "ready", value: loaded.value };
}

// ---------------------------------------------------------------------------------------------- mentions

/**
 * Calls `onMention` on every mention of the signed-in user in the current workspace. The server publishes them only to
 * the personal channel `ws:user:<id>` (comment_service.go, document_comment_mentions.go), never to the workspace one, so
 * the channel is subscribed here; only its owner may subscribe it (internal/ws/client.go). The name is `data.event`:
 * the hub copies only a bus event's `event_type` into `type`, which arrives empty here. The channel carries every
 * workspace's mentions, hence the `workspace_id` check. Read from the store, not from `lastEvent` in render: two
 * events in one tick are both seen.
 * Not unsubscribed on unmount: the server keeps one flag per channel, shared with the sidebar badge and the bell, and
 * the store does not count subscribers. A workspace switch keeps it too: the store moves `ws:user:*` to the new socket.
 */
function useMentionEvents(onMention: () => void) {
  const userId = useAuthStore((s) => s.user?.id);
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const handler = useRef(onMention);
  handler.current = onMention;
  useEffect(() => {
    if (!userId) return;
    useWebSocketStore.getState().subscribe(`ws:user:${userId}`);
    return useWebSocketStore.subscribe(({ lastEvent: m }, prev) => {
      if (m !== prev.lastEvent && m?.data?.event === "mention.badge" && m.data.workspace_id === wsId) handler.current();
    });
  }, [userId, wsId]);
}

interface MentionsRaw {
  items: MentionItem[];
  failed: MentionsData["failedSources"];
}

export function useMentions(): Async<MentionsData> {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const { projects } = useProjectStore();
  const shownRef = useRef<Set<string> | null>(null);
  const lastVisitRef = useRef<Date | null | undefined>(undefined);
  const badgeSentRef = useRef(false);

  const load = useCallback(
    async (wsId: string): Promise<MentionsRaw> => {
      const { items, failed } = await fetchMentionInbox(wsId, 50);
      // storage is read once per mount; later refetches keep what this mount already showed
      shownRef.current ??= readShownIds();
      if (lastVisitRef.current === undefined) lastVisitRef.current = readLastVisit(wsSlug);
      const shown = shownRef.current;
      const cutoff = lastVisitRef.current?.getTime();
      const out = items.map((m) => toMentionItem(m, shown, cutoff, wsSlug, projects));
      return { items: out, failed };
    },
    // projects only feed hrefs; a project list that arrives later is merged in the memo below
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [wsSlug],
  );
  const { loaded, reload, retry, patch } = useReloadable("mentions", load);

  useMentionEvents(reload);

  useEffect(() => {
    const onSeen = (e: Event) => {
      const id = (e as CustomEvent<{ id: string }>).detail?.id;
      if (id) patch((v) => ({ ...v, items: v.items.map((m) => (m.id === id ? { ...m, seen: true, state: "opened" } : m)) }));
    };
    window.addEventListener(MENTION_SEEN_EVENT, onSeen);
    return () => window.removeEventListener(MENTION_SEEN_EVENT, onSeen);
  }, [patch]);

  const rows = loaded && "value" in loaded ? loaded.value.items : null;
  // After rendering: remember which unseen ids were shown (new -> shown on the next visit), clear the sidebar badge once.
  useEffect(() => {
    if (!rows || rows.length === 0) return;
    const next = new Set(shownRef.current ?? readShownIds());
    for (const m of rows) if (!m.seen) next.add(m.id);
    shownRef.current = next;
    saveShownIds(next);
    if (!badgeSentRef.current) {
      badgeSentRef.current = true;
      window.dispatchEvent(new CustomEvent("mesh:mentions:shown", { detail: { newCount: 0 } }));
    }
  }, [rows]);
  // last_visit moves on a delay, once per mount, when the list was really on screen
  const ready = rows !== null;
  useEffect(() => {
    if (!ready) return;
    const id = window.setTimeout(() => writeLastVisit(wsSlug, new Date()), LAST_VISIT_DELAY_MS);
    return () => window.clearTimeout(id);
  }, [ready, wsSlug]);

  return useMemo<Async<MentionsData>>(() => {
    const r = toAsync(loaded, retry);
    if (r.state !== "ready") return r;
    const withHref = r.value.items.map((m) => {
      const project = projects.find((p) => p.id === m.projectId);
      const href = m.target.kind === "task" ? m.href : m.href ?? hrefOf(m, wsSlug, project?.slug);
      return m.target.kind === "document" ? { ...m, href, target: { ...m.target, projectSlug: project?.slug } } : m;
    });
    // «только новые» and the limit belong to the view (its own toggle starts from the config): filtering here would
    // make the checkbox one-way, the seen rows would never come back
    return {
      state: "ready",
      value: { items: withHref, failedSources: r.value.failed, unseen: withHref.filter((m) => !m.seen).length },
    };
  }, [loaded, retry, projects, wsSlug]);
}

function hrefOf(m: MentionItem, wsSlug: string | undefined, projectSlug: string | undefined): string | null {
  if (m.target.kind !== "document" || !wsSlug || !projectSlug) return null;
  return `/w/${wsSlug}/p/${projectSlug}/docs/${m.target.docId}?comment=${m.id}`;
}

function toMentionItem(
  m: MentionInboxItem,
  shown: Set<string>,
  cutoff: number | undefined,
  wsSlug: string | undefined,
  projects: { id: string; slug: string }[],
): MentionItem {
  const project = projects.find((p) => p.id === m.project_id);
  const state: MentionItem["state"] = m.seen_at ? "opened" : shown.has(m.comment_id) ? "shown" : "new";
  const base = {
    id: m.comment_id,
    source: m.source,
    title: m.title,
    at: m.extracted_at,
    seen: !!m.seen_at,
    authorId: m.author_id,
    authorName: m.author_name,
    body: m.comment_body,
    question: m.comment_body.includes("❓"),
    state,
    fresh: cutoff !== undefined && new Date(m.extracted_at).getTime() > cutoff,
    projectId: m.project_id,
  };
  // Task mentions keep the /t/:id resolver, which finds its own slugs. A document has no such resolver:
  // without its project there is nowhere honest to point.
  return m.source === "task"
    ? { ...base, href: `/t/${m.task_id}?comment=${m.comment_id}`, target: { kind: "task", taskId: m.task_id, commentId: m.comment_id } }
    : { ...base, href: mentionHref(m, wsSlug, project?.slug), target: { kind: "document", docId: m.document_id, projectSlug: project?.slug } };
}

/** Unseen mentions across tasks and documents: the menu counter (card M) and the «Непрочитанные упоминания» metric (W4). */
export function useUnseenMentionCount(): Async<number> {
  const { loaded, reload, retry, patch } = useReloadable("unseen", fetchUnseenMentionCount);
  useMentionEvents(reload);
  useEffect(() => {
    const onSeen = () => {
      patch((n) => Math.max(0, n - 1));
      reload(); // the server count is the truth; the decrement only covers the round trip
    };
    window.addEventListener(MENTION_SEEN_EVENT, onSeen);
    return () => window.removeEventListener(MENTION_SEEN_EVENT, onSeen);
  }, [patch, reload]);
  return useMemo(() => toAsync(loaded, retry), [loaded, retry]);
}

// ---------------------------------------------------------------------------------------------- comments

type Cursor = { before: string; beforeId: string } | null;

const PAGE = 50;

function toCommentItem(
  c: CommentView,
  wsSlug: string | undefined,
  projectSlug: string | undefined,
  projectIcon: string | undefined,
): CommentItem {
  return {
    id: c.comment_id,
    taskId: c.task_id,
    title: c.task_title,
    projectId: c.project_id,
    projectName: c.project_name,
    projectIcon: projectIcon ?? "",
    body: c.comment_body,
    authorId: c.author_id,
    authorName: c.author_name,
    authorKind: c.author_kind,
    internal: c.is_internal,
    at: c.created_at,
    href: wsSlug && projectSlug ? `/w/${wsSlug}/p/${projectSlug}/t/${c.task_id}?comment=${c.comment_id}` : `/t/${c.task_id}?comment=${c.comment_id}`,
  };
}

async function fetchPage(path: string, params: Record<string, string | number | undefined>, cursor?: Cursor) {
  const p = { ...params, limit: PAGE, ...(cursor ? { before: cursor.before, before_id: cursor.beforeId } : {}) };
  const page = await api<CommentViewPage>(path, { params: p });
  // created_at is not unique: the page boundary carries the id tie-breaker too, or part of a group is lost.
  const next = page?.next_cursor && page.next_cursor_id ? { before: page.next_cursor, beforeId: page.next_cursor_id } : null;
  return { raw: page?.items ?? [], next } as { raw: CommentView[]; next: Cursor };
}

interface Page {
  raw: CommentView[];
  next: Cursor;
}

function useComments(opts: { key: string; path: (wsId: string) => string; live: boolean; workspaceOnly: boolean }): Async<CommentsData> & {
  loadMore: () => void;
} {
  const { wsSlug } = useParams<{ wsSlug: string }>();
  const { projects } = useProjectStore();
  const cfg = useWidgetConfig();
  const limit = numberSetting(cfg, "limit", 20, 5, 50);
  const allWorkspaces = opts.workspaceOnly && cfg.scope === "all";
  const { path } = opts;
  // «этот воркспейс» is asked of the server (workspace_id), not guessed from the project list
  const params = useCallback(
    (_wsId: string): Record<string, string | undefined> => {
      if (opts.workspaceOnly) return allWorkspaces ? {} : { workspace_id: _wsId };
      // recent-comments: the server leaves internal notes out unless asked; the view's toggle hides them again
      return { include_internal: "true" };
    },
    [opts.workspaceOnly, allWorkspaces],
  );
  const load = useCallback((wsId: string): Promise<Page> => fetchPage(path(wsId), params(wsId)), [path, params]);
  const { loaded, reload, retry } = useReloadable(`${opts.key}:${allWorkspaces ? "all" : "ws"}`, load);
  const first = loaded && "value" in loaded ? loaded.value : null;

  // Pages after the first. Tied to the first page they continue: a fresh first page (reload / WS) drops them.
  const [more, setMore] = useState<{ base: Page | null; items: CommentView[]; cursor: Cursor | undefined; loading: boolean }>({
    base: null, items: [], cursor: undefined, loading: false,
  });
  const extra = first && more.base === first ? more : null;
  const nextCursor: Cursor = extra && extra.cursor !== undefined ? extra.cursor : first?.next ?? null;
  const [visible, setVisible] = useState(limit);
  useEffect(() => setVisible(limit), [limit]);

  useRealtime({ kind: "workspace" }, useCallback((e) => { if (opts.live && e.type === "comment.created") reload(); }, [reload, opts.live]));

  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const state = useRef({ first, extra, nextCursor });
  state.current = { first, extra, nextCursor };
  const matched = useRef(0);
  const loadMore = useCallback(() => {
    const cur = state.current;
    if (!wsId || !cur.first || cur.extra?.loading) return;
    // rows already loaded but not yet shown come first; only then the next page
    if (matched.current > visible) return setVisible((v) => v + limit);
    if (!cur.nextCursor) return;
    const base = cur.first;
    setMore({ base, items: cur.extra?.items ?? [], cursor: cur.extra?.cursor, loading: true });
    fetchPage(path(wsId), params(wsId), cur.nextCursor).then(
      ({ raw, next }) => {
        setMore((m) => (m.base === base ? { base, items: [...m.items, ...raw], cursor: next, loading: false } : m));
        setVisible((v) => v + limit);
      },
      () => setMore((m) => (m.base === base ? { ...m, loading: false } : m)),
    );
  }, [wsId, path, params, visible, limit]);

  const result = useMemo<Async<CommentsData>>(() => {
    const r = toAsync(loaded, retry);
    if (r.state !== "ready") return r;
    const slugOf = new Map(projects.map((p) => [p.id, p.slug]));
    const iconOf = new Map(projects.map((p) => [p.id, p.icon]));
    let items = [...r.value.raw, ...(extra?.items ?? [])].map((c) =>
      toCommentItem(c, wsSlug, slugOf.get(c.project_id), iconOf.get(c.project_id)),
    );
    // the project select is the view's: filtering here would hide the other projects' rows for good
    matched.current = items.length;
    return {
      state: "ready",
      value: { items: items.slice(0, visible), hasMore: items.length > visible || nextCursor !== null, loadingMore: !!extra?.loading },
    };
  }, [loaded, retry, extra, projects, wsSlug, visible, nextCursor]);
  return Object.assign({}, result, { loadMore });
}

const ME_COMMENTS = () => "/api/v1/me/comments";
const RECENT_COMMENTS = (wsId: string) => `/api/v1/workspaces/${wsId}/comments/recent`;

/** Comments I wrote, newest first, «Ещё» by cursor. Settings: scope (workspace|all), projectId. */
export function useMyComments(): Async<CommentsData> & { loadMore: () => void } {
  return useComments({ key: "mine", path: ME_COMMENTS, live: false, workspaceOnly: true });
}

/** Recent comments of the workspace with authors; live on `comment.created`. Settings: projectId (internal notes are filtered in the view). */
export function useRecentComments(): Async<CommentsData> & { loadMore: () => void } {
  return useComments({ key: "recent", path: RECENT_COMMENTS, live: true, workspaceOnly: false });
}
