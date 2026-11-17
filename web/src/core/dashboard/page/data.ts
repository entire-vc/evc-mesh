// Reads for the dashboard page (PRD §6.2). One fetch per id; `dashboard.updated`
// re-reads in place, so a change made in another tab shows without a reload.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiRequestError } from "@/lib/api";
import { useRealtime } from "@/core/primitives/use-realtime";
import type { DashboardDoc, DashboardSummary, WidgetInstance } from "./model";

export type Load<T> =
  | { state: "loading" }
  | { state: "error"; error: Error; retry: () => void }
  | { state: "missing" }
  /** `reload` resolves to whether the newest read reached the screen (false: it failed and the old value stays). */
  | { state: "ready"; value: T; refreshing: boolean; reload: () => Promise<boolean> };

export const isMissing = (e: unknown) => e instanceof ApiRequestError && (e.status === 404 || e.status === 501);

/** Re-reads on every change of `key`; a re-read keeps the old value on screen (no flash). `key` null: not yet (loading). */
function useReread<T>(key: string | null, read: () => Promise<T>, onLive?: (type: string, data: unknown) => boolean) {
  const [value, setValue] = useState<{ key: string; v: T } | { key: string; err: Error | "missing" } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const readRef = useRef(read);
  readRef.current = read;
  const seq = useRef(0);
  const latest = useRef<Promise<boolean>>(Promise.resolve(true));

  const run = useCallback(
    (silent: boolean): Promise<boolean> => {
      if (key === null) return Promise.resolve(true);
      const mine = ++seq.current;
      if (silent) setRefreshing(true);
      const p: Promise<boolean> = readRef
        .current()
        .then(
          (v) => {
            if (mine === seq.current) setValue({ key, v });
            return true;
          },
          (e) => {
            if (mine !== seq.current) return false;
            // A failed silent re-read keeps what is on screen, unless the thing is gone (deleted in another
            // tab: `dashboard.updated {id, deleted:true}` re-reads into a 404) — then the page says so.
            if (isMissing(e)) setValue({ key, err: "missing" });
            else if (!silent) setValue({ key, err: e instanceof Error ? e : new Error(String(e)) });
            return false;
          },
        )
        .finally(() => mine === seq.current && setRefreshing(false))
        // A newer read replaced this one: its outcome is what reaches the screen (none after unmount).
        .then((ok) => (mine === seq.current ? ok : latest.current === p ? false : latest.current));
      latest.current = p;
      return p;
    },
    [key],
  );

  useEffect(() => {
    setValue(null);
    void run(false);
    return () => {
      seq.current++;
    };
  }, [run, attempt]);

  useRealtime({ kind: "workspace" }, (e) => {
    if (onLive?.(e.type, e.data)) void run(true);
  });

  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  const cur = value && value.key === key ? value : null;
  const load: Load<T> = !cur
    ? { state: "loading" }
    : "err" in cur
      ? cur.err === "missing"
        ? { state: "missing" }
        : { state: "error", error: cur.err, retry }
      : { state: "ready", value: cur.v, refreshing, reload: () => run(true) };
  return load;
}

/** The server's event is `{event_type, workspace_id, id, version, deleted?}` (dashboards/service.go). */
const touches = (data: unknown, id: string) => {
  const target = ((data ?? {}) as { id?: string }).id;
  return target === undefined || target === id;
};

// Every dashboards route lives under the workspace (cmd/api/routes_dashboards.go).
const base = (wsId: string) => `/api/v1/workspaces/${wsId}/dashboards`;

export function useDashboardList(wsId: string | undefined): Load<DashboardSummary[]> {
  return useReread(
    wsId ? `list:${wsId}` : null,
    async () => normalizeList(await api<unknown>(base(wsId!))),
    (type) => type === "dashboard.updated" || type === "dashboard.created" || type === "dashboard.deleted",
  );
}

export function useDashboard(wsId: string | undefined, id: string): Load<DashboardDoc> {
  return useReread(
    wsId ? `doc:${wsId}:${id}` : null,
    async () => normalizeDoc(await api<DashboardDoc>(`${base(wsId!)}/${encodeURIComponent(id)}`)),
    (type, data) => type === "dashboard.updated" && touches(data, id),
  );
}

/** First `top` dashboard by position, else the first `bottom` one (PRD §5.3). */
export function pickHome(list: readonly DashboardSummary[]): DashboardSummary | undefined {
  const by = (p: DashboardSummary["placement"]) => list.filter((d) => d.placement === p).sort((a, b) => a.position - b.position)[0];
  return by("top") ?? by("bottom");
}

/** The server answers `{dashboards: [...]}`; a bare array or `{items}` is read too. */
export function normalizeList(raw: unknown): DashboardSummary[] {
  if (Array.isArray(raw)) return raw;
  const r = raw as { dashboards?: DashboardSummary[]; items?: DashboardSummary[] } | null;
  return r?.dashboards ?? r?.items ?? [];
}

/** Ids for the instances the server has not stored yet (the virtual default's); never sent back. */
const LOCAL_ID = "~";
const isLocalId = (id: string) => id.startsWith(LOCAL_ID);

/** Every instance gets an id: the virtual default's widgets come without one until its first save. */
export function normalizeDoc(doc: DashboardDoc): DashboardDoc {
  const widgets = (doc.widgets ?? []).map((w, i) => (w.id ? w : { ...w, id: `${LOCAL_ID}${i}` }));
  return { ...doc, widgets };
}

export async function saveWidgets(wsId: string, doc: DashboardDoc, widgets: WidgetInstance[]): Promise<DashboardDoc> {
  // A made-up id is not an instance of the server's: it gets a new one (the virtual default is saved on the way).
  const body = widgets.map(({ id, ...rest }) => (isLocalId(id) ? rest : { id, ...rest }));
  return api<DashboardDoc>(`${base(wsId)}/${encodeURIComponent(doc.id)}/layout`, { method: "PUT", body: { version: doc.version, widgets: body } });
}

export async function duplicateDashboard(wsId: string, id: string): Promise<DashboardSummary> {
  return api<DashboardSummary>(`${base(wsId)}/${encodeURIComponent(id)}/duplicate`, { method: "POST" });
}
