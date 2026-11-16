// Edit-mode contracts (PRD §5.5-§5.7, §6.2). Cells of the 12-column grid.
import type { Values, FieldSchema } from "@/components/extensions/schema-form";

export interface Cells {
  w: number;
  h: number;
}

export interface CatalogSize {
  default: Cells;
  min: Cells;
  max: Cells;
}

export type CatalogSourceKind = "core" | "extension" | "theme" | "external";

/** One entry of `GET /dashboard-widgets`: a widget type the current user may place. */
export interface CatalogType {
  id: string;
  title: string;
  description: string;
  /** Group heading: Tasks, Communications, Agents, ... */
  category: string;
  source: { kind: CatalogSourceKind; name: string };
  size: CatalogSize;
  /** JSON Schema with `x-widget`, rendered by SchemaForm. */
  settings_schema?: FieldSchema;
}

/** A widget instance on a dashboard. */
export interface LayoutItem {
  /** Instance id, stable across moves. */
  id: string;
  /** Catalog type id. */
  type: string;
  x: number;
  y: number;
  w: number;
  h: number;
  /** Overrides the type title. */
  title?: string;
  auto_refresh?: boolean;
  settings?: Values;
  /** Added in this session, not yet known to the server: sent without an id. */
  fresh?: boolean;
}

export interface DashboardLayout {
  dashboardId: string;
  /** Server version the items were read at; sent back with every save. */
  version: number;
  items: LayoutItem[];
}

/** What a theme's `dashboard.layout` view declares (§5.10). */
export type ThemeLayoutContract = "v2" | "v1";

export interface DashboardEditorProps {
  layout: DashboardLayout;
  /** Right to edit (§5.2). When false the editor renders nothing. */
  canEdit: boolean;
  /** Called with the saved layout after every successful action (and on "Cancel changes"). */
  onChange: (next: DashboardLayout) => void;
  /** Re-read the layout from the server (conflict "Refresh"). Default: the editor adopts the server copy itself. */
  onRefresh?: () => void;
  /** Draws the body of one instance inside its frame. Default: the core `Widget` view by type id. */
  renderWidget?: (item: LayoutItem, type: CatalogType | undefined) => import("react").ReactNode;
  /** Injected for tests and for P's own transport; defaults to the real API. */
  api?: LayoutApi;
}

export interface LayoutApi {
  catalog(): Promise<CatalogType[]>;
  save(dashboardId: string, version: number, items: LayoutItem[]): Promise<{ version: number; items?: LayoutItem[] }>;
  load(dashboardId: string): Promise<DashboardLayout>;
}

export class StaleLayoutError extends Error {
  constructor() {
    super("stale_layout");
    this.name = "StaleLayoutError";
  }
}
