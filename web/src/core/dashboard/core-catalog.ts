// Sizes and categories of the core widget types (PRD §5.8 «Р», §5.7 groups). The server's catalog
// (internal/extensions/dashboards/catalog.go) is the source; core-widgets.json is its generated copy, kept in step by
// the Go test TestCoreWidgetsJSON. Group descriptors take `size` and `category` from here, so the view clamps an
// instance by the same bounds the editor got from GET /dashboard-widgets.
import type { GridUnit, WidgetSize } from "./types";
import CATALOG from "./core-widgets.json";

export interface CoreSizeRange {
  default: WidgetSize;
  min: WidgetSize;
  max: WidgetSize;
}
export interface CoreWidgetType {
  id: string;
  category: string;
  size: CoreSizeRange;
}

const ALIASES: Readonly<Record<string, string>> = CATALOG.aliases;
const BY_ID: ReadonlyMap<string, CoreWidgetType> = new Map(
  CATALOG.types.map((t) => [t.id, { id: t.id, category: t.category, size: t.size as CoreSizeRange }]),
);

/** The core type a client id stands for: itself, or the type a pre-dashboards id was renamed to ("triage"). */
export function coreTypeId(id: string): string {
  return ALIASES[id] ?? id;
}

/** The catalog entry of a core widget id (aliases resolved), or undefined for an id the catalog does not know. */
export function coreWidgetType(id: string): CoreWidgetType | undefined {
  return BY_ID.get(coreTypeId(id));
}

function known(id: string): CoreWidgetType {
  const t = coreWidgetType(id);
  if (!t) throw new Error(`dashboard widgets: "${id}" is not a core type of catalog.go`);
  return t;
}

/** Size range of a core widget, a fresh copy per descriptor. */
export function coreSize(id: string): CoreSizeRange {
  const { size } = known(id);
  const cells = (c: WidgetSize): WidgetSize => ({ w: c.w as GridUnit, h: c.h as GridUnit });
  return { default: cells(size.default), min: cells(size.min), max: cells(size.max) };
}

/** Catalog group of a core widget (§5.7): tasks, communication, agents, analytics, events, initiatives. */
export function coreCategory(id: string): string {
  return known(id).category;
}

/** Every core type in §5.8 order. */
export const CORE_TYPES: readonly CoreWidgetType[] = [...BY_ID.values()];
