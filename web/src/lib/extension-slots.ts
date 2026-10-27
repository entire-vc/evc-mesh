import { useEffect, useMemo, useSyncExternalStore } from "react";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import type { ManifestCapabilities } from "@/lib/extension-capabilities";

/**
 * UI slot registry (W2.7). A slot is a named place in the task view that an
 * extension may fill. The set of extensions that fill it comes from
 * `GET /workspaces/{ws}/extensions`: a connected, enabled extension whose
 * manifest `capabilities.ui` names the slot. Which component fills it is
 * decided by `components/extensions/slots/builtin-slots.ts` for built-ins; an
 * external extension fills `sidebar.item` and `task.panel` with an iframe screen
 * (`components/extensions/external-frame.tsx`, W3.3).
 *
 * One request serves every card on the board: the registry is a module-level
 * store, so opening a task or rendering a column never waits on it and never
 * issues a second fetch.
 */
export type SlotId = "task.panel" | "task.badge" | "task.actions" | "sidebar.item";

/** An iframe screen an external extension serves for a slot (manifest `external.ui`). */
export interface ExternalScreen {
  title: string;
  icon?: string;
}

export interface SlotExtension {
  id: string;
  name: string;
  slots: SlotId[];
  /** Iframe screens by slot; present only for external extensions that declare them. */
  external?: Partial<Record<SlotId, ExternalScreen>>;
  /** The manifest says the extension works per project (`levels.project`). */
  projectLevel?: boolean;
}

interface WorkspaceExtensionDto {
  id: string;
  name: string;
  levels?: { project?: boolean };
  manifest?: {
    runtime?: string;
    levels?: { project?: boolean };
    capabilities?: ManifestCapabilities;
    external?: { ui?: Record<string, { path?: string; title?: string; icon?: string }> } | null;
  };
  status?: { enabled?: boolean };
}

const SLOT_IDS: readonly SlotId[] = ["task.panel", "task.badge", "task.actions", "sidebar.item"];
/** Slots an external extension fills with an iframe screen. */
export const FRAME_SLOTS: readonly SlotId[] = ["sidebar.item", "task.panel"];
const SLOTS: ReadonlySet<string> = new Set<string>(SLOT_IDS);
const TTL_MS = 60_000;

/** Stable empty result: a card whose slot has no contributor must not re-render when the registry lands. */
const EMPTY: SlotExtension[] = [];

type BySlot = Record<SlotId, SlotExtension[]>;
const NONE: BySlot = { "task.panel": EMPTY, "task.badge": EMPTY, "task.actions": EMPTY, "sidebar.item": EMPTY };

interface Entry {
  bySlot: BySlot;
  fetchedAt: number;
  inflight: boolean;
  /** A change landed while a fetch was in flight: that answer may predate it, so fetch once more. */
  stale?: boolean;
}

const entries = new Map<string, Entry>();
const listeners = new Set<() => void>();

function emit() {
  listeners.forEach((l) => l());
}

export function parseSlotExtensions(list: WorkspaceExtensionDto[]): SlotExtension[] {
  const out: SlotExtension[] = [];
  for (const ext of list) {
    // `status.enabled` is the server-resolved state (workspace row, else instance row; a
    // placeholder without base_url/token/secret resolves to off), so it already means
    // "connected and on" — an instance-level connection counts, `connection` may be null.
    if (!ext.status?.enabled) continue;
    const slots: SlotId[] = [];
    for (const u of ext.manifest?.capabilities?.ui ?? []) {
      const slot = typeof u === "string" ? u : u?.slot;
      if (slot && SLOTS.has(slot) && !slots.includes(slot as SlotId)) slots.push(slot as SlotId);
    }
    if (slots.length === 0) continue;
    const entry: SlotExtension = { id: ext.id, name: ext.name, slots };
    if (ext.levels?.project ?? ext.manifest?.levels?.project) entry.projectLevel = true;
    if (ext.manifest?.runtime === "external") {
      // A screen exists only where the manifest both lists the slot and gives it a path.
      for (const slot of FRAME_SLOTS) {
        const screen = ext.manifest.external?.ui?.[slot];
        if (slots.includes(slot) && screen?.path) {
          (entry.external ??= {})[slot] = { title: screen.title || ext.name, ...(screen.icon ? { icon: screen.icon } : {}) };
        }
      }
    }
    out.push(entry);
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

function groupBySlot(exts: SlotExtension[]): BySlot {
  const out: BySlot = { "task.panel": [], "task.badge": [], "task.actions": [], "sidebar.item": [] };
  for (const e of exts) for (const s of e.slots) out[s].push(e);
  for (const s of SLOT_IDS) if (out[s].length === 0) out[s] = EMPTY;
  return out;
}

/** True when two groupings hold the same extensions per slot (so subscribers can keep their snapshot). */
function sameGrouping(a: BySlot, b: BySlot): boolean {
  return SLOT_IDS.every(
    (s) =>
      a[s].length === b[s].length &&
      a[s].every((e, i) => e.id === b[s][i]?.id && e.name === b[s][i]?.name && e.external?.[s]?.title === b[s][i]?.external?.[s]?.title && e.projectLevel === b[s][i]?.projectLevel),
  );
}

function load(wsId: string) {
  const prev = entries.get(wsId);
  if (prev?.inflight) {
    prev.stale = true;
    return;
  }
  const entry: Entry = prev ?? { bySlot: NONE, fetchedAt: 0, inflight: false };
  entry.inflight = true;
  entries.set(wsId, entry);
  api<{ extensions: WorkspaceExtensionDto[] }>(`/api/v1/workspaces/${wsId}/extensions`)
    .then((res) => {
      const next = groupBySlot(parseSlotExtensions(res.extensions ?? []));
      // Keep the old snapshot objects when nothing changed: no subscriber re-renders.
      if (!sameGrouping(entry.bySlot, next)) entry.bySlot = next;
    })
    .catch(() => {
      // A failed read leaves the previous registry (initially: no slots, the pre-W2.7 card).
    })
    .finally(() => {
      entry.inflight = false;
      entry.fetchedAt = Date.now();
      emit();
      if (entry.stale) {
        entry.stale = false;
        load(wsId);
      }
    });
}

function ensureLoaded(wsId: string) {
  const e = entries.get(wsId);
  if (!e || (!e.inflight && Date.now() - e.fetchedAt > TTL_MS)) load(wsId);
}

/** Drop the cached registry so the next mount refetches (after enable/disable/connect). */
export function invalidateSlotRegistry(wsId?: string) {
  if (wsId) {
    const e = entries.get(wsId);
    if (e) e.fetchedAt = 0;
  } else {
    entries.forEach((e) => { e.fetchedAt = 0; });
  }
}

/** Refetch now and tell subscribers (the extensions UI calls this after enable/disable/connect). */
export function refreshSlotRegistry(wsId: string) {
  invalidateSlotRegistry(wsId);
  load(wsId);
}

/** After any save that can change which extensions are enabled: drop every cached registry, refetch the active workspace's. */
export function refreshSlotRegistryAfterChange() {
  invalidateSlotRegistry();
  const wsId = useWorkspaceStore.getState().currentWorkspace?.id;
  if (wsId) load(wsId);
}

export function resetSlotRegistryForTests() {
  entries.clear();
  emit();
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

/**
 * Extensions filling `slot` in the active workspace; empty while the registry
 * loads or if it failed, so the task view is the pre-slot view until the answer
 * arrives and never waits for it. The snapshot is the same array object until
 * this slot's contributors change.
 */
export function useSlotExtensions(slot: SlotId): SlotExtension[] {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  useEffect(() => {
    if (wsId) ensureLoaded(wsId);
  }, [wsId]);
  return useSyncExternalStore(
    subscribe,
    () => (wsId ? entries.get(wsId)?.bySlot[slot] ?? EMPTY : EMPTY),
    () => EMPTY,
  );
}

/**
 * The tab to show: a `slot:<id>` selection whose extension left the registry
 * (disabled while its panel was open) falls back instead of rendering a blank pane.
 */
export function resolveSlotTab<T extends string>(selected: T, extensionIds: string[], fallback: T): T {
  if (!selected.startsWith("slot:")) return selected;
  return extensionIds.some((id) => `slot:${id}` === selected) ? selected : fallback;
}

/** `sidebar.item` screens of external extensions, for the navigation. */
export function useSidebarItems(): Array<{ extensionId: string; title: string; icon?: string; projectLevel?: boolean }> {
  const exts = useSlotExtensions("sidebar.item");
  return useMemo(
    () =>
      exts.flatMap((e) => {
        const screen = e.external?.["sidebar.item"];
        return screen
          ? [{ extensionId: e.id, title: screen.title || e.name, icon: screen.icon, ...(e.projectLevel ? { projectLevel: true } : {}) }]
          : [];
      }),
    [exts],
  );
}
