// Transport of the edit mode, per PRD §6.2; every route lives under the workspace (cmd/api/routes_dashboards.go):
//   GET  /workspaces/:ws/dashboard-widgets               {widgets:[...]} catalog for the current viewer
//   GET  /workspaces/:ws/dashboards/:id                  {version, widgets:[{id, widget_type, x,y,w,h, settings}]}
//   PUT  /workspaces/:ws/dashboards/:id/layout           {version, widgets:[{id?, widget_type, x,y,w,h, settings}]}, 409 stale_layout
// The server's catalog knows ids, sizes, categories and settings schemas; titles and descriptions of core widgets
// are the client's (contract C), so core entries are completed from the widget registry.
// The editor works with `LayoutItem` (type, title, auto_refresh); the wire keeps title and auto_refresh inside
// the per-instance `settings` object, as the table has no separate columns for them.
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import { WIDGETS } from "../registry";
import type { FieldSchema } from "@/components/extensions/schema-form";
import { StaleLayoutError, type CatalogType, type DashboardLayout, type LayoutApi, type LayoutItem } from "./types";

export interface WireWidget {
  id?: string;
  widget_type: string;
  x: number;
  y: number;
  w: number;
  h: number;
  settings?: Record<string, unknown>;
}
interface WireDashboard {
  version: number;
  widgets?: WireWidget[];
}

export function fromWire(w: WireWidget): LayoutItem {
  const { title, auto_refresh, ...rest } = (w.settings ?? {}) as Record<string, unknown>;
  const item: LayoutItem = { id: w.id ?? "", type: w.widget_type, x: w.x, y: w.y, w: w.w, h: w.h };
  if (typeof title === "string" && title) item.title = title;
  if (typeof auto_refresh === "boolean") item.auto_refresh = auto_refresh;
  if (Object.keys(rest).length) item.settings = rest as LayoutItem["settings"];
  return item;
}

export function toWire(it: LayoutItem): WireWidget {
  const settings: Record<string, unknown> = { ...(it.settings ?? {}) };
  if (it.title) settings.title = it.title;
  if (it.auto_refresh !== undefined) settings.auto_refresh = it.auto_refresh;
  const wire: WireWidget = { widget_type: it.type, x: it.x, y: it.y, w: it.w, h: it.h, settings };
  if (!it.fresh) wire.id = it.id;
  return wire;
}

function isStale(e: unknown): boolean {
  const x = e as { status?: number; code?: string; errorCode?: string } | null;
  return !!x && (x.status === 409 || x.code === "stale_layout" || x.errorCode === "stale_layout");
}

interface WireCatalogEntry {
  id?: string;
  widget_type?: string;
  origin?: string;
  category?: string;
  size: CatalogType["size"];
  title?: string;
  description?: string;
  source?: { kind?: string; name?: string };
  theme_id?: string;
  /** The server's name for the settings schema; `settings_schema` is read too. */
  settings?: FieldSchema;
  settings_schema?: FieldSchema;
}

const SOURCE_KINDS: ReadonlySet<string> = new Set(["core", "extension", "theme", "external"]);
const DESCRIPTORS = new Map(WIDGETS.map((d) => [d.id, d]));

/** One catalog entry as the editor reads it: server facts, client strings for core widgets, a source always set. */
export function fromWireCatalog(c: WireCatalogEntry): CatalogType {
  const id = c.widget_type ?? c.id ?? "";
  const d = DESCRIPTORS.get(id);
  const rawKind = c.source?.kind ?? c.origin ?? d?.origin ?? "core";
  const kind = (SOURCE_KINDS.has(rawKind) ? rawKind : "external") as CatalogType["source"]["kind"];
  const name = c.source?.name || (kind === "theme" ? c.theme_id : undefined) || (kind === "core" ? "Mesh" : id);
  const entry: CatalogType = {
    id,
    title: c.title || d?.title || id,
    description: c.description ?? d?.description ?? "",
    category: c.category || d?.category || kind,
    source: { kind, name },
    size: c.size,
  };
  // For a core widget the server's `settings` only validates (catalog.go limitSchema: bounds, no titles or
  // defaults, "other keys pass"); the form is the registry's UI schema. Extensions and themes describe their own.
  const clientForm = kind === "core" ? (d?.settings as FieldSchema | undefined) : undefined;
  const schema = clientForm ?? c.settings_schema ?? c.settings ?? (d?.settings as FieldSchema | undefined);
  if (schema) entry.settings_schema = schema;
  return entry;
}

function currentWorkspaceId(): string {
  const ws = useWorkspaceStore.getState().currentWorkspace?.id;
  if (!ws) throw new Error("no workspace");
  return ws;
}

/** Ids the page makes up for the instances of the virtual default (page/data.ts); never sent back. */
const isLocalId = (id: string) => !id || id.startsWith("~");

/** The page's dashboard document as the editor's layout: an instance without a server id is saved as a new one. */
export function layoutFromDoc(doc: { id: string; version: number; widgets: WireWidget[] }): DashboardLayout {
  return {
    dashboardId: doc.id,
    version: doc.version,
    items: doc.widgets.map((w) => {
      const item = fromWire(w);
      return isLocalId(item.id) ? { ...item, fresh: true } : item;
    }),
  };
}

/**
 * Transport bound to one workspace. The page passes the workspace of the edit session, so a later switch of the
 * current workspace cannot send this session's saves elsewhere. `ws` is read per call for the default transport.
 */
export function workspaceApi(ws: string | (() => string)): LayoutApi {
  const wsId = typeof ws === "function" ? ws : () => ws;
  const dashboardUrl = (dashboardId: string) => `/api/v1/workspaces/${wsId()}/dashboards/${encodeURIComponent(dashboardId)}`;
  const self: LayoutApi = {
    async catalog() {
      const r = await api<WireCatalogEntry[] | { widgets?: WireCatalogEntry[]; items?: WireCatalogEntry[] }>(`/api/v1/workspaces/${wsId()}/dashboard-widgets`);
      const list = Array.isArray(r) ? r : (r.widgets ?? r.items ?? []);
      return list.map(fromWireCatalog);
    },
    async load(dashboardId): Promise<DashboardLayout> {
      const r = await api<WireDashboard>(dashboardUrl(dashboardId));
      return { dashboardId, version: r.version, items: (r.widgets ?? []).map(fromWire) };
    },
    async save(dashboardId, version, items) {
      try {
        const r = await api<WireDashboard>(`${dashboardUrl(dashboardId)}/layout`, { method: "PUT", body: { version, widgets: items.map(toWire) } });
        if (r.widgets) return { version: r.version, items: r.widgets.map(fromWire) };
        // the server did not echo the widgets: new ones have no server id yet, read them back
        if (items.some((i) => i.fresh)) {
          const fresh = await self.load(dashboardId);
          return { version: fresh.version, items: fresh.items };
        }
        return { version: r.version };
      } catch (e) {
        if (isStale(e)) throw new StaleLayoutError();
        throw e;
      }
    },
  };
  return self;
}

/** The current workspace's transport (tests, and callers without a session). */
export const realApi: LayoutApi = workspaceApi(currentWorkspaceId);
