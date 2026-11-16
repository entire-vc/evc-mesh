// Data hooks of the initiatives widgets (PRD dashboards §6.3): views read
// initiatives only through these, in the current viewer's rights; the server
// counts progress over the linked projects the viewer may open.
import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Initiative, InitiativeStatus } from "@/types";
import type { Async } from "../../types";

export type InitiativeSort = "created" | "name" | "target_date" | "progress";
export type InitiativeAsync<T> = Async<T> & { reload: () => void };

export function initiativeHref(wsSlug: string | undefined, id: string): string {
  return wsSlug ? `/w/${wsSlug}/initiatives/${id}` : `/initiatives/${id}`;
}

export function progressPct(ini: Initiative): number {
  if (ini.status === "completed") return 100;
  const total = ini.total_tasks ?? 0;
  return total > 0 ? Math.round(((ini.completed_tasks ?? 0) / total) * 100) : 0;
}

/** Filter by status and sort, as the `initiatives` widget settings ask. */
export function selectInitiatives(
  items: readonly Initiative[],
  opts: { statuses?: readonly InitiativeStatus[]; sort?: InitiativeSort },
): Initiative[] {
  const statuses = opts.statuses?.length ? new Set(opts.statuses) : null;
  const out = items.filter((i) => !statuses || statuses.has(i.status));
  switch (opts.sort ?? "created") {
    case "name":
      return out.sort((a, b) => a.name.localeCompare(b.name));
    case "target_date":
      // Undated last: a deadline-ordered list leads with what is due.
      return out.sort((a, b) =>
        (a.target_date ?? "￿").localeCompare(b.target_date ?? "￿"),
      );
    case "progress":
      return out.sort((a, b) => progressPct(b) - progressPct(a));
    default:
      return out.sort((a, b) => b.created_at.localeCompare(a.created_at));
  }
}

function useAsync<T>(key: string | null, load: () => Promise<T>): InitiativeAsync<T> {
  const [loaded, setLoaded] = useState<{ key: string; value: T } | { key: string; error: Error } | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!key) return;
    let live = true;
    load()
      .then((value) => live && setLoaded({ key, value }))
      .catch((e) => live && setLoaded({ key, error: e instanceof Error ? e : new Error(String(e)) }));
    return () => {
      live = false;
    };
    // load is keyed by `key`; a new key is a new load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, attempt]);
  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return useMemo<InitiativeAsync<T>>(() => {
    if (loaded === null || loaded.key !== key) return { state: "loading", reload };
    if ("error" in loaded) {
      return { state: "error", error: loaded.error, retry: () => { setLoaded(null); reload(); }, reload };
    }
    return { state: "ready", value: loaded.value, reload };
  }, [loaded, key, reload]);
}

/** Initiatives of the current workspace. */
export function useInitiatives(): InitiativeAsync<Initiative[]> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id) ?? null;
  return useAsync(wsId, async () => {
    const res = await api<Initiative[] | null>(`/api/v1/workspaces/${wsId}/initiatives`);
    // A Go nil slice arrives as null; anything but a list is no initiatives, never a crash.
    return Array.isArray(res) ? res : [];
  });
}

/** One initiative with the linked projects the viewer may open. */
export function useInitiative(id: string | null | undefined): InitiativeAsync<Initiative> {
  return useAsync(id || null, () => api<Initiative>(`/api/v1/initiatives/${id}`));
}
