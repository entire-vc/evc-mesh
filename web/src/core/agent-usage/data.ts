// Reads of the statistics view: one request per distinct query, shared for CACHE_MS by every view that asks the
// same (the page and a dashboard widget with equal settings). The answer depends on the viewer (rows they may see,
// `canSeeCost`), so the cache is keyed by the signed-in user and dropped when that user changes.
import { useEffect, useMemo, useState } from "react";
import { api, apiBlob } from "@/lib/api";
import { useAuthStore } from "@/stores/auth";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Async } from "@/core/dashboard/types";
import { pickHome, normalizeList, saveWidgets, normalizeDoc } from "@/core/dashboard/page/data";
import type { DashboardDoc } from "@/core/dashboard/page/model";
import { isUserUsage, quotaQuery, stateToSettings, usageQuery, type StatsState } from "./state";
import type { QuotaResponse, UsageResponse } from "./types";

export const CACHE_MS = 30_000;
export const AGENT_USAGE_WIDGET = "agent-usage";

const base = (wsId: string) => `/api/v1/workspaces/${wsId}/analytics`;

interface Entry {
  promise: Promise<unknown>;
  at: number;
}
const cache = new Map<string, Entry>();

export function clearAgentUsageCache(): void {
  cache.clear();
}
useAuthStore.subscribe((state, prev) => {
  if (state.user?.id !== prev.user?.id) clearAgentUsageCache();
});

/** When each answer arrived: «Updated 16:40 MSK» under the table. A shared cached answer keeps its first time. */
const arrived = new WeakMap<object, number>();
export const loadedAt = (value: unknown): number | undefined => (value && typeof value === "object" ? arrived.get(value) : undefined);

function cached<T>(key: string, load: () => Promise<T>, now = Date.now()): Promise<T> {
  const hit = cache.get(key);
  if (hit && now - hit.at < CACHE_MS) return hit.promise as Promise<T>;
  const promise = load().then((value) => {
    if (value && typeof value === "object" && !arrived.has(value)) arrived.set(value, Date.now());
    return value;
  });
  const entry = { promise, at: now };
  cache.set(key, entry);
  promise.catch(() => {
    if (cache.get(key) === entry) cache.delete(key);
  });
  return promise;
}

/** `path?query`: the API helper takes single-valued params only, and filters repeat their key. */
const withQuery = (path: string, q: URLSearchParams) => `${path}?${q.toString()}`;

function useRead<T>(path: string | null, userScope = false): Async<T> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const viewerId = useAuthStore((s) => s.user?.id);
  const scope = userScope ? "me" : wsId;
  const key = scope && viewerId && path ? `${viewerId}@${scope}${path}` : "";
  const [loaded, setLoaded] = useState<{ key: string; value: T } | { key: string; error: Error } | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!key || !scope || !path) return;
    let live = true;
    const root = userScope ? "/api/v1/me/analytics" : base(scope);
    cached(key, () => api<T>(`${root}${path}`))
      .then((value) => live && setLoaded({ key, value }))
      .catch((e) => live && setLoaded({ key, error: e instanceof Error ? e : new Error(String(e)) }));
    return () => {
      live = false;
    };
    // scope and path are part of key; attempt re-runs a failed read.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, attempt]);
  return useMemo<Async<T>>(() => {
    if (!loaded || loaded.key !== key) return { state: "loading" };
    if ("error" in loaded) return { state: "error", error: loaded.error, retry: () => setAttempt((n) => n + 1) };
    return { state: "ready", value: loaded.value };
  }, [loaded, key]);
}

/** Usage of the state (§3.1); `null` state: not asked (the quota view is open). */
export function useUsage(st: StatsState | null): Async<UsageResponse> {
  return useRead<UsageResponse>(st ? withQuery("/usage", usageQuery(st)) : null, Boolean(st && isUserUsage(st)));
}

/** Subscription limits of the state (§3.2); `null`: not asked. */
export function useQuota(st: StatsState | null): Async<QuotaResponse> {
  return useRead<QuotaResponse>(st ? withQuery("/quota", quotaQuery(st)) : null);
}

/** Downloads the CSV of what is on screen: the same query with `format=csv`. */
export async function downloadStatsCsv(wsId: string, st: StatsState): Promise<void> {
  const usageBase = isUserUsage(st) ? "/api/v1/me/analytics" : base(wsId);
  const path = st.m === "quota" ? withQuery(`${base(wsId)}/quota`, quotaQuery(st, { format: "csv" })) : withQuery(`${usageBase}/usage`, usageQuery(st, { format: "csv" }));
  const { blob, filename } = await apiBlob(path);
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename && filename !== "download" ? filename : `agent-stats-${st.m}.csv`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

/**
 * Puts an `agent-usage` widget with this state on the viewer's home dashboard (the first one of the menu), full width
 * under everything already there. Resolves to the dashboard it went to.
 */
export async function addToHomeDashboard(wsId: string, st: StatsState): Promise<DashboardDoc> {
  const dashboards = `/api/v1/workspaces/${wsId}/dashboards`;
  const home = pickHome(normalizeList(await api<unknown>(dashboards)));
  if (!home) throw new Error("no dashboard");
  const doc = normalizeDoc(await api<DashboardDoc>(`${dashboards}/${encodeURIComponent(home.id)}`));
  const y = doc.widgets.reduce((m, w) => Math.max(m, w.y + w.h), 0);
  const widget = { id: "~agent-usage", widget_type: AGENT_USAGE_WIDGET, x: 0, y, w: 12, h: 6, settings: stateToSettings(st) };
  return saveWidgets(wsId, doc, [...doc.widgets, widget]);
}
