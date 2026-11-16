import { useEffect, useSyncExternalStore } from "react";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { onSlotRegistryInvalidated } from "@/lib/extension-slots";

/**
 * Dashboard widgets of extensions (PRD Dashboards §7.2). A widget type of an
 * extension is `<ext_id>.<widget_id>`; core widget ids have no dot. Whether an
 * instance on a dashboard can draw is decided here from
 * `GET /workspaces/{ws}/extensions` with the same rule the server's catalog
 * uses (`internal/extensions/widgets.go`): the extension is enabled, declares
 * the `dashboard.widget` slot and the widget, and a workspace connection has
 * consented to the slot. Otherwise the instance stays on the dashboard and
 * shows a stub; once the extension is back it draws again by itself.
 */
export const WIDGET_SLOT = "dashboard.widget";
const WIDGET_CAPABILITY = "ui:dashboard.widget";

export interface ManifestWidget {
  id: string;
  title: string;
  title_i18n?: Record<string, string>;
  description?: string;
  category?: string;
  scope?: "workspace" | "project" | "user";
  settings?: unknown;
  render: { builtin?: string; frame?: { path: string; min_refresh_s?: number } };
}

interface WorkspaceExtensionDto {
  id: string;
  name: string;
  manifest?: {
    capabilities?: { ui?: Array<string | { slot?: string }> };
    widgets?: ManifestWidget[];
  };
  status?: { enabled?: boolean; source?: string; consented_capabilities?: string[] };
}

export type ExtensionWidgetState =
  | { state: "loading" }
  | { state: "ready"; extensionId: string; extensionName: string; widget: ManifestWidget }
  /** The extension is installed but off in this workspace. */
  | { state: "disabled"; extensionId: string; extensionName: string }
  /** The extension was updated to ask for the slot and the administrator has not agreed yet. */
  | { state: "consent"; extensionId: string; extensionName: string }
  /** The extension or this widget of it is gone. */
  | { state: "removed"; extensionId: string; extensionName: string };

/** `<ext_id>.<widget_id>` → its parts; null for a core id (no dot). */
export function parseWidgetType(type: string): { extensionId: string; widgetId: string } | null {
  const dot = type.indexOf(".");
  if (dot <= 0 || dot === type.length - 1) return null;
  return { extensionId: type.slice(0, dot), widgetId: type.slice(dot + 1) };
}

export function isExtensionWidgetType(type: string): boolean {
  return parseWidgetType(type) !== null;
}

function declaresSlot(ext: WorkspaceExtensionDto): boolean {
  return (ext.manifest?.capabilities?.ui ?? []).some((u) => (typeof u === "string" ? u : u?.slot) === WIDGET_SLOT);
}

/** The state of one widget type against the workspace's extensions (null list: still loading). */
export function resolveExtensionWidget(type: string, list: WorkspaceExtensionDto[] | null): ExtensionWidgetState {
  const parsed = parseWidgetType(type);
  if (!parsed) return { state: "removed", extensionId: type, extensionName: type };
  if (!list) return { state: "loading" };
  const ext = list.find((e) => e.id === parsed.extensionId);
  if (!ext) return { state: "removed", extensionId: parsed.extensionId, extensionName: parsed.extensionId };
  const base = { extensionId: ext.id, extensionName: ext.name || ext.id };
  if (!ext.status?.enabled) return { state: "disabled", ...base };
  const widget = declaresSlot(ext) ? ext.manifest?.widgets?.find((w) => w.id === parsed.widgetId) : undefined;
  if (!widget) return { state: "removed", ...base };
  // A workspace connection must carry the slot in its consent; the instance level of a builtin has none to give.
  if (ext.status.source === "workspace" && !(ext.status.consented_capabilities ?? []).includes(WIDGET_CAPABILITY)) {
    return { state: "consent", ...base };
  }
  return { state: "ready", ...base, widget };
}

// ── workspace extensions store: one request serves every widget on a dashboard ──

const TTL_MS = 60_000;
interface Entry {
  list: WorkspaceExtensionDto[] | null;
  fetchedAt: number;
  inflight: boolean;
  /** Invalidated while a request was in flight: its answer may predate the change, so fetch again. */
  stale: boolean;
}
const entries = new Map<string, Entry>();
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

function load(wsId: string) {
  const entry = entries.get(wsId) ?? { list: null, fetchedAt: 0, inflight: false, stale: false };
  if (entry.inflight) {
    entry.stale = true;
    return;
  }
  entry.stale = false;
  entry.inflight = true;
  entries.set(wsId, entry);
  api<{ extensions: WorkspaceExtensionDto[] | null }>(`/api/v1/workspaces/${wsId}/extensions`)
    .then((res) => {
      entry.list = res.extensions ?? [];
    })
    .catch(() => {
      // Keep the previous answer; with none yet, an empty list draws stubs rather than spinning forever.
      entry.list ??= [];
    })
    .finally(() => {
      entry.inflight = false;
      if (entry.stale) {
        // Never cache an answer that may be older than the change: draw it meanwhile, refetch now.
        entry.fetchedAt = 0;
        emit();
        load(wsId);
        return;
      }
      entry.fetchedAt = Date.now();
      emit();
    });
}

/** Refetch on next use (called after an extension is enabled, disabled or re-consented). */
export function invalidateExtensionWidgets(wsId?: string) {
  const mark = (e: Entry) => {
    e.fetchedAt = 0;
    if (e.inflight) e.stale = true;
  };
  if (wsId) {
    const e = entries.get(wsId);
    if (e) mark(e);
  } else {
    entries.forEach(mark);
  }
  const current = useWorkspaceStore.getState().currentWorkspace?.id;
  if (current && listeners.size > 0 && (!wsId || wsId === current)) load(current);
}

// Enable/disable/connect invalidates the slot registry; the widget stubs follow it.
onSlotRegistryInvalidated(invalidateExtensionWidgets);

export function resetExtensionWidgetsForTests() {
  entries.clear();
  emit();
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

/** State of an extension widget type in the active workspace. */
export function useExtensionWidget(type: string): ExtensionWidgetState {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  useEffect(() => {
    if (!wsId) return;
    const e = entries.get(wsId);
    if (!e || (!e.inflight && Date.now() - e.fetchedAt > TTL_MS)) load(wsId);
  }, [wsId]);
  const list = useSyncExternalStore(subscribe, () => (wsId ? entries.get(wsId)?.list ?? null : null));
  return resolveExtensionWidget(type, list);
}
