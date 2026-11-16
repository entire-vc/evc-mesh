// Data hook of the event-feed widget: GET /workspaces/:ws/events with the
// filters on the server, live updates through the workspace WebSocket.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/lib/api";
import { useWebSocket } from "@/hooks/use-websocket";
import { useWorkspaceStore } from "@/stores/workspace";
import type { EventBusMessage, EventType } from "@/types";

export const EVENT_TYPES: readonly EventType[] = [
  "summary",
  "status_change",
  "context_update",
  "error",
  "dependency_resolved",
  "custom",
];

export type ActorFilter = "all" | "agent" | "user" | "system";

export interface EventFeedFilters {
  eventTypes: EventType[];
  /** A project id or "all" (every project the viewer may see). */
  projectId: string;
  actorType: ActorFilter;
  /** yyyy-mm-dd, local day; "" = open. */
  dateFrom: string;
  dateTo: string;
}

export const DEFAULT_FILTERS: EventFeedFilters = {
  eventTypes: [],
  projectId: "all",
  actorType: "all",
  dateFrom: "",
  dateTo: "",
};

export function isDefaultFilters(f: EventFeedFilters): boolean {
  return f.eventTypes.length === 0 && f.projectId === "all" && f.actorType === "all" && f.dateFrom === "" && f.dateTo === "";
}

/** Filters away from the defaults: types, project, actor and period count once each. */
export function activeFilterCount(f: EventFeedFilters): number {
  return (
    (f.eventTypes.length > 0 ? 1 : 0) +
    (f.projectId !== "all" ? 1 : 0) +
    (f.actorType !== "all" ? 1 : 0) +
    (f.dateFrom !== "" || f.dateTo !== "" ? 1 : 0)
  );
}

export interface FeedEvent extends EventBusMessage {
  actor_type: "agent" | "user" | "system";
  actor_id?: string | null;
  project_slug?: string | null;
}

interface FeedPage {
  items: FeedEvent[];
  has_more: boolean;
  next_cursor: string | null;
}

export const PAGE_SIZE = 50;
const LIVE_DEBOUNCE_MS = 400;

/** Query parameters of GET /workspaces/:ws/events for the filters. */
export function feedParams(f: EventFeedFilters, cursor?: string | null): Record<string, string> {
  const p: Record<string, string> = { limit: String(PAGE_SIZE) };
  if (f.eventTypes.length > 0) p.event_type = f.eventTypes.join(",");
  if (f.projectId !== "all") p.project_id = f.projectId;
  if (f.actorType !== "all") p.actor_type = f.actorType;
  if (f.dateFrom) p.date_from = new Date(`${f.dateFrom}T00:00:00`).toISOString();
  if (f.dateTo) p.date_to = new Date(`${f.dateTo}T23:59:59.999`).toISOString();
  if (cursor) p.cursor = cursor;
  return p;
}

/** Newest first, each id once; `head` wins over `tail` for a duplicate id. */
export function mergeEvents(head: FeedEvent[], tail: FeedEvent[]): FeedEvent[] {
  const seen = new Set<string>();
  const out: FeedEvent[] = [];
  for (const e of [...head, ...tail]) {
    if (seen.has(e.id)) continue;
    seen.add(e.id);
    out.push(e);
  }
  return out.sort((a, b) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : a.id < b.id ? 1 : -1));
}

export type EventsState =
  | { state: "loading" }
  | { state: "error"; error: Error; retry: () => void }
  | { state: "ready"; items: FeedEvent[]; hasMore: boolean };

export interface UseEventsResult {
  result: EventsState;
  /** Ids that arrived after the first load (drawn as live rows). */
  liveIds: ReadonlySet<string>;
  connected: boolean;
  refreshing: boolean;
  loadingMore: boolean;
  refresh: () => void;
  loadMore: () => void;
}

/**
 * The workspace event feed for `filters`. A bus event on the WebSocket makes
 * the hook re-read the newest page (the socket message carries no task title,
 * project or actor names, and the server applies the viewer's project scope),
 * then merges it on top of what is loaded.
 */
export function useEvents(filters: EventFeedFilters, opts: { live: boolean }): UseEventsResult {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const key = JSON.stringify([wsId, filters]);
  const [data, setData] = useState<{ key: string; items: FeedEvent[]; cursor: string | null; hasMore: boolean } | null>(null);
  const [error, setError] = useState<{ key: string; error: Error } | null>(null);
  const [liveIds, setLiveIds] = useState<ReadonlySet<string>>(new Set());
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const keyRef = useRef(key);
  keyRef.current = key;

  const fetchPage = useCallback(
    (cursor?: string | null) =>
      api<FeedPage>(`/api/v1/workspaces/${wsId}/events`, { params: feedParams(filters, cursor) }),
    // key covers wsId and filters
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );

  // First page on mount and on every filter change.
  useEffect(() => {
    if (!wsId) return;
    let live = true;
    setLiveIds(new Set());
    fetchPage()
      .then((p) => live && setData({ key, items: p.items, cursor: p.next_cursor, hasMore: p.has_more }))
      .catch((e: unknown) => live && setError({ key, error: e instanceof Error ? e : new Error(String(e)) }));
    return () => {
      live = false;
    };
  }, [key, wsId, fetchPage, attempt]);

  const ready = data && data.key === key ? data : null;
  const readyRef = useRef(ready);
  readyRef.current = ready;

  // Re-read the newest page and merge it on top; new ids are marked live.
  const pullHead = useCallback(
    (markLive: boolean) => {
      const at = keyRef.current;
      return fetchPage().then((p) => {
        if (keyRef.current !== at) return;
        const prev = readyRef.current;
        const known = new Set((prev?.items ?? []).map((e) => e.id));
        const fresh = p.items.filter((e) => !known.has(e.id)).map((e) => e.id);
        setData({
          key: at,
          items: mergeEvents(p.items, prev?.items ?? []),
          cursor: prev ? prev.cursor : p.next_cursor,
          hasMore: prev ? prev.hasMore : p.has_more,
        });
        if (markLive && fresh.length > 0) setLiveIds((s) => new Set([...fresh, ...s]));
      });
    },
    [fetchPage],
  );

  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  // A bus event that arrived while the first page was loading (that page may have been read before it):
  // the key it arrived under, so the head is re-read once that page is ready.
  const pendingLive = useRef<string | null>(null);
  useEffect(() => {
    if (!ready || pendingLive.current !== ready.key) return;
    pendingLive.current = null;
    void pullHead(true).catch(() => undefined);
  }, [ready, pullHead]);
  const onEvent = useCallback(
    (e: { type: string; data: Record<string, unknown> }) => {
      if (!opts.live) return;
      // Only event-bus messages: their type is an event type and they carry an id.
      if (!(EVENT_TYPES as readonly string[]).includes(e.type) || typeof e.data?.id !== "string") return;
      if (!readyRef.current) {
        pendingLive.current = keyRef.current;
        return;
      }
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        timer.current = null;
        void pullHead(true).catch(() => undefined);
      }, LIVE_DEBOUNCE_MS);
    },
    [opts.live, pullHead],
  );
  // The workspace socket directly (not the themes' useRealtime wrapper): one shared chunk fewer.
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspace?.slug);
  const { isConnected: connected } = useWebSocket({ workspaceSlug: wsSlug, onEvent });

  const refresh = useCallback(() => {
    if (!readyRef.current) {
      setError(null);
      setAttempt((n) => n + 1);
      return;
    }
    setRefreshing(true);
    void pullHead(false)
      .catch(() => undefined)
      .finally(() => setRefreshing(false));
  }, [pullHead]);

  const loadMore = useCallback(() => {
    const cur = readyRef.current;
    if (!cur?.hasMore || !cur.cursor || loadingMore) return;
    const at = keyRef.current;
    setLoadingMore(true);
    fetchPage(cur.cursor)
      .then((p) => {
        if (keyRef.current !== at) return;
        setData((d) =>
          d && d.key === at ? { key: at, items: mergeEvents(d.items, p.items), cursor: p.next_cursor, hasMore: p.has_more } : d,
        );
      })
      .catch(() => undefined)
      .finally(() => setLoadingMore(false));
  }, [fetchPage, loadingMore]);

  const result = useMemo<EventsState>(() => {
    if (ready) return { state: "ready", items: ready.items, hasMore: ready.hasMore };
    if (error && error.key === key)
      return {
        state: "error",
        error: error.error,
        retry: () => {
          setError(null);
          setAttempt((n) => n + 1);
        },
      };
    return { state: "loading" };
  }, [ready, error, key]);

  return { result, liveIds, connected, refreshing, loadingMore, refresh, loadMore };
}
