import { useEffect, useMemo, useSyncExternalStore } from "react";
import { api } from "@/lib/api";
// Type only: the module itself belongs to the lazy settings screens, not the header's chunk.
import type { ProjectBinding } from "@/lib/extension-settings";
import { useSidebarItems } from "@/lib/extension-slots";

/**
 * Extension tabs of a project's header strip. `sidebar.item` is a workspace
 * navigation entry (left menu); it becomes a project tab only for an extension
 * that works per project (`levels.project`) and is bound to this project.
 * Until the bindings are known, or if they fail to load, there are no extension
 * tabs: fail-closed, so a workspace-only extension never flashes into the strip.
 */

const TTL_MS = 60_000;
/** Stable "no tabs" result, so the strip does not rebuild while bindings are unknown. */
const NONE: Array<{ extensionId: string; title: string }> = [];

interface Entry {
  /** Extension ids with an enabled binding; null until known, or after a failed read. */
  bound: ReadonlySet<string> | null;
  fetchedAt: number;
  /** Number of the read this answer came from (see `readProjectBindings`). */
  seq: number;
}

const entries = new Map<string, Entry>();
/** Number of the last read started per project, by the header or by project settings. */
const started = new Map<string, number>();
const inflight = new Set<string>();
const listeners = new Set<() => void>();

function emit() {
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

function boundIds(list: ProjectBinding[]): ReadonlySet<string> {
  return new Set(list.filter((b) => b.enabled).map((b) => b.extension_id));
}

/**
 * `GET /projects/{p}/extensions`, feeding the header cache on the way. Every
 * reader goes through here (the header and the project settings tab after
 * bind/unbind), so answers are ordered by when their read started: one that
 * started before an already applied answer is dropped, whoever issued it.
 */
export function readProjectBindings(projectId: string): Promise<ProjectBinding[]> {
  const seq = (started.get(projectId) ?? 0) + 1;
  started.set(projectId, seq);
  const apply = (bound: ReadonlySet<string> | null) => {
    if (seq <= (entries.get(projectId)?.seq ?? 0)) return;
    entries.set(projectId, { bound, fetchedAt: Date.now(), seq });
    emit();
  };
  // `GET /projects/{p}/extensions`, as `listProjectBindings` in extension-settings.
  const read = api<{ extensions: ProjectBinding[] | null }>(`/api/v1/projects/${projectId}/extensions`).then(
    (res) => res.extensions ?? [],
  );
  // A failed read drops the cached answer: it may list a binding removed since.
  read.then((list) => apply(boundIds(list)), () => apply(null));
  return read;
}

function load(projectId: string) {
  if (inflight.has(projectId)) return;
  inflight.add(projectId);
  readProjectBindings(projectId)
    .catch(() => {})
    .finally(() => { inflight.delete(projectId); });
}

export function resetProjectExtensionTabsForTests() {
  entries.clear();
  started.clear();
  inflight.clear();
  emit();
}

export function useProjectExtensionTabs(
  projectId: string | undefined,
): Array<{ extensionId: string; title: string }> {
  const items = useSidebarItems();
  const candidates = useMemo(() => items.filter((i) => i.projectLevel), [items]);
  // No project-level screen in the workspace: nothing to ask the project about.
  const needed = Boolean(projectId) && candidates.length > 0;
  useEffect(() => {
    if (!needed || !projectId) return;
    const e = entries.get(projectId);
    if (!inflight.has(projectId) && (!e || Date.now() - e.fetchedAt > TTL_MS)) load(projectId);
  }, [needed, projectId]);
  const bound = useSyncExternalStore(
    subscribe,
    () => (needed && projectId ? entries.get(projectId)?.bound ?? null : null),
    () => null,
  );
  return useMemo(
    () => (bound ? candidates.filter((i) => bound.has(i.extensionId)) : NONE),
    [candidates, bound],
  );
}
