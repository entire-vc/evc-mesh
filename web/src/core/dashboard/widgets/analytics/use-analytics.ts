// Data hook of the analytics and cost widgets (PRD §6.3): one request per
// distinct filter. Every widget on a dashboard with the same period, project and
// sort shares one in-flight request and one cached answer for CACHE_MS.
// The answer depends on who asks (a member sees only their projects), so the
// cache is per signed-in user and is dropped whenever that user changes.
import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import type { AnalyticsMetrics } from "@/types";
import type { Async } from "../../types";

export const CACHE_MS = 30_000;

/** A preset counts back from today; a custom range names its days (inclusive). */
export type AnalyticsPeriod = { days: 7 | 30 | 90 } | { from: string; to: string };

export interface AnalyticsQuery {
  period?: AnalyticsPeriod;
  /** One project; absent = every project the viewer may see. */
  projectId?: string;
  topTasksSort?: "cost" | "tokens";
}

export const DEFAULT_PERIOD: AnalyticsPeriod = { days: 30 };

const day = (d: Date) => d.toISOString().slice(0, 10);

/**
 * The request parameters of a query. Presets send only the first day: the server
 * counts up to now, so two widgets mounted a second apart still ask the same thing.
 * A custom `to` is the end of that day, not its first instant.
 */
export function analyticsParams(q: AnalyticsQuery, now = new Date()): Record<string, string> {
  const p = q.period ?? DEFAULT_PERIOD;
  const out: Record<string, string> = {};
  if ("days" in p) {
    const from = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - p.days));
    out.from = day(from);
  } else {
    out.from = p.from;
    out.to = `${p.to}T23:59:59Z`;
  }
  if (q.projectId) out.project_id = q.projectId;
  if (q.topTasksSort && q.topTasksSort !== "cost") out.top_tasks_sort = q.topTasksSort;
  return out;
}

const keyOf = (viewerId: string, wsId: string, params: Record<string, string>, path: string) =>
  `${viewerId}@${wsId}${path}?${Object.keys(params)
    .sort()
    .map((k) => `${k}=${params[k]}`)
    .join("&")}`;

interface Entry {
  promise: Promise<unknown>;
  at: number;
}
const cache = new Map<string, Entry>();

/** Drop every cached answer (sign-in, sign-out, user switch; tests). */
export function clearAnalyticsCache(): void {
  cache.clear();
}

// Logout, login or a different account: nothing cached for the previous user
// may answer the next one, not even a request still in flight.
useAuthStore.subscribe((state, prev) => {
  if (state.user?.id !== prev.user?.id) clearAnalyticsCache();
});

/**
 * One GET per viewer, path and parameters while it is in flight or younger than CACHE_MS; a failure is not kept.
 * `path` is relative to the workspace (`/analytics`, `/analytics/memory`).
 */
export function fetchCached<T>(viewerId: string, wsId: string, path: string, params: Record<string, string>, now = Date.now()): Promise<T> {
  const key = keyOf(viewerId, wsId, params, path);
  const hit = cache.get(key);
  if (hit && now - hit.at < CACHE_MS) return hit.promise as Promise<T>;
  const promise = api<T>(`/api/v1/workspaces/${wsId}${path}`, { params });
  const entry = { promise, at: now };
  cache.set(key, entry);
  promise.catch(() => {
    if (cache.get(key) === entry) cache.delete(key);
  });
  return promise;
}

/** One request per viewer and key while it is in flight or younger than CACHE_MS; a failure is not kept. */
export function fetchAnalytics(
  viewerId: string,
  wsId: string,
  params: Record<string, string>,
  now = Date.now(),
): Promise<AnalyticsMetrics> {
  return fetchCached<AnalyticsMetrics>(viewerId, wsId, "/analytics", params, now);
}

/** Path and parameters of the CSV export for the same filter as the widget. */
export function analyticsExportRequest(wsId: string, q: AnalyticsQuery): { path: string; params: Record<string, string> } {
  return { path: `/api/v1/workspaces/${wsId}/analytics/export`, params: { ...analyticsParams(q), format: "csv" } };
}

/**
 * A cached workspace GET as an Async: loading until the answer for these parameters arrives, an error with its
 * retry, or the value. `path` null = nothing to ask yet (stays loading).
 */
export function useCachedGet<T>(path: string | null, params: Record<string, string>): Async<T> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const viewerId = useAuthStore((s) => s.user?.id);
  const key = wsId && viewerId && path ? keyOf(viewerId, wsId, params, path) : "";
  const [loaded, setLoaded] = useState<{ key: string; value: T } | { key: string; error: Error } | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!wsId || !viewerId || !path) return;
    let live = true;
    fetchCached<T>(viewerId, wsId, path, params)
      .then((value) => live && setLoaded({ key, value }))
      .catch((e) => live && setLoaded({ key, error: e instanceof Error ? e : new Error(String(e)) }));
    return () => {
      live = false;
    };
    // wsId, viewerId, path and params are derived from key; attempt re-runs a failed load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, attempt]);
  return useMemo<Async<T>>(() => {
    if (!loaded || loaded.key !== key) return { state: "loading" };
    if ("error" in loaded) return { state: "error", error: loaded.error, retry: () => setAttempt((n) => n + 1) };
    return { state: "ready", value: loaded.value };
  }, [loaded, key]);
}

export function useAnalytics(q: AnalyticsQuery): Async<AnalyticsMetrics> {
  return useCachedGet<AnalyticsMetrics>("/analytics", analyticsParams(q));
}
