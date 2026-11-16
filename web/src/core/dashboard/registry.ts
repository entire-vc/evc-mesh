import type { ComponentType } from "react";
import type { Permission } from "@/core/primitives";
import { views as COMMS_VIEWS } from "./widgets/comms/views";
import { views as LEGACY_VIEWS } from "./widgets/legacy/views";
import type { DashboardLayoutSettings, GridUnit, WidgetDescriptor, WidgetInstance, WidgetSize } from "./types";

// A widget group is a folder `widgets/<group>/` with two files:
//   index.ts   exports `descriptors` and is loaded eagerly: the catalog, permissions and the default layout need them;
//   views.tsx  exports `views` (views by descriptor id) and is loaded lazily, the first time a widget of the group
//              is drawn. The registry chunk never references such a view, so the registry (which every SchemaForm
//              screen pulls in through CORE_WIDGETS) and the dashboard route drag no view chunk, and none of the
//              chunks a view needs, into the entry chunk's preload map.
// The exceptions are `legacy` and `comms`: the widgets every default dashboard draws (legacy's mentions view is in
// comms/views.tsx). Their views ship with the host (imported below): they draw on every visit, so a lazy load would
// only add a round trip, and they need no chunk of their own. Adding a group needs no edit here: the globs below
// pick the folder up.
type AnyView = ComponentType<any>;
export interface WidgetGroup {
  descriptors: readonly WidgetDescriptor[];
  /** `true`: the group's widgets are offered in the catalog only, never on a dashboard that picks no widgets. */
  catalogOnly?: boolean;
}
/** A group's `views.tsx`. */
export interface WidgetViewsModule {
  views?: Readonly<Record<string, AnyView>>;
}
export type WidgetViewsLoader = () => Promise<WidgetViewsModule>;
export interface WidgetRegistry {
  descriptors: WidgetDescriptor[];
  /** The loader of the group's `views.tsx`, by descriptor id; no entry = the group has no views file. */
  viewLoaders: Record<string, WidgetViewsLoader>;
  /** Ids from groups that export `catalogOnly = true`: in the catalog, not on a dashboard that picks no widgets. */
  catalogOnly: Set<string>;
}

/**
 * Merges group modules (path -> module) into one registry. Paths go in alphabetical order, `legacy` first, so the
 * four legacy widgets keep their default order. A duplicate id is an error in dev (`strict`) and skipped in prod:
 * the first one wins. A module without `descriptors` is skipped with a warning. `viewModules` are the lazy
 * `views.tsx` loaders (path -> loader). An `index.ts` must not export views (that would put them back into the
 * registry chunk); registry.test.ts holds every real group to that, so it costs the bundle nothing.
 */
export function buildRegistry(
  modules: Record<string, Partial<WidgetGroup>>,
  strict: boolean,
  viewModules: Record<string, WidgetViewsLoader> = {},
): WidgetRegistry {
  const out: WidgetRegistry = { descriptors: [], viewLoaders: {}, catalogOnly: new Set() };
  const rank = (p: string) => (p.includes("/legacy/") ? 0 : 1);
  const paths = Object.keys(modules).sort((a, b) => rank(a) - rank(b) || a.localeCompare(b, "en"));
  for (const path of paths) {
    const group = modules[path];
    if (!group || !Array.isArray(group.descriptors)) {
      console.warn(`dashboard widgets: ${path} exports no "descriptors"; skipped`);
      continue;
    }
    const load = viewModules[path.replace(/index\.ts$/, "views.tsx")];
    for (const d of group.descriptors) {
      if (out.descriptors.some((x) => x.id === d.id)) {
        const msg = `dashboard widgets: duplicate widget id "${d.id}" in ${path}`;
        if (strict) throw new Error(msg);
        console.error(`${msg}; skipped`);
        continue;
      }
      out.descriptors.push(d);
      if (group.catalogOnly === true) out.catalogOnly.add(d.id);
      if (load) out.viewLoaders[d.id] = load;
    }
  }
  return out;
}

const REGISTRY = buildRegistry(
  import.meta.glob<Partial<WidgetGroup>>("./widgets/*/index.ts", { eager: true }),
  import.meta.env.DEV,
  import.meta.glob<WidgetViewsModule>(["./widgets/*/views.tsx", "!./widgets/legacy/views.tsx", "!./widgets/comms/views.tsx"]),
);

/** Every registered widget, in registry order (the default order). */
export const WIDGETS: readonly WidgetDescriptor[] = REGISTRY.descriptors;
export const CORE_WIDGETS: readonly WidgetDescriptor[] = WIDGETS.filter((d) => d.origin === "core");
/** Widgets of the catalog-only groups: offered in the catalog, shown only where a dashboard picks them. */
export const CATALOG_ONLY: ReadonlySet<string> = REGISTRY.catalogOnly;

export interface WidgetViewCache {
  /** The view of a widget if it is already known: a view, null (the widget has none), undefined (not loaded yet). */
  peek(id: string): AnyView | null | undefined;
  /**
   * Loads the view of a widget (its group's views.tsx). Resolves to null when the widget has no view. A failed load
   * is not remembered: the next call tries again, which is what the widget's retry does.
   */
  load(id: string): Promise<AnyView | null>;
}

/** `preloaded`: views that ship with the host (the legacy and comms groups), known without a load. */
export function createViewCache(registry: WidgetRegistry, preloaded: Readonly<Record<string, AnyView>> = {}): WidgetViewCache {
  const known = new Map<string, AnyView | null>(Object.entries(preloaded));
  return {
    peek: (id) => (known.has(id) || !registry.viewLoaders[id] ? (known.get(id) ?? null) : undefined),
    async load(id) {
      const load = registry.viewLoaders[id];
      if (!known.has(id)) known.set(id, (load && (await load()).views?.[id]) || null);
      return known.get(id)!;
    },
  };
}

const VIEWS = createViewCache(REGISTRY, { ...LEGACY_VIEWS, ...COMMS_VIEWS });
/** See WidgetViewCache.peek. */
export const peekWidgetView = (id: string) => VIEWS.peek(id);
/** See WidgetViewCache.load. */
export const loadWidgetView = (id: string) => VIEWS.load(id);

export function clampSize(size: { w: number; h: number }, d: WidgetDescriptor): WidgetSize {
  const c = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
  return {
    w: c(size.w, d.size.min.w, d.size.max.w) as GridUnit,
    h: c(size.h, d.size.min.h, d.size.max.h) as GridUnit,
  };
}

/**
 * Default placement: widgets in the given order, `columns` per row (1..4, as the theme setting always had it), each
 * `12 / columns` wide and as tall as its default; a row is as tall as its tallest widget. Read back in `(y, x)` order
 * it is the order it came in, which is what the v1 grid draws.
 */
export function defaultInstances(shown: readonly WidgetDescriptor[], columns: number = 2): WidgetInstance[] {
  const cols = [1, 2, 3, 4].includes(columns) ? columns : 2;
  const cell = 12 / cols;
  let y = 0;
  let x = 0;
  let inRow = 0;
  let rowH = 0;
  return shown.map((d) => {
    // Width honours the descriptor's min/max; a widget that does not fit the rest of the row starts a new one.
    const w = Math.min(12, Math.max(d.size.min.w, Math.min(cell, d.size.max.w)));
    if (inRow > 0 && (inRow >= cols || x + w > 12)) {
      y += rowH;
      x = 0;
      inRow = 0;
      rowH = 0;
    }
    const h = Math.max(d.size.min.h, Math.min(d.size.default.h, d.size.max.h));
    const inst = { id: d.id, type: d.id, x, y, w, h, settings: {} } as WidgetInstance;
    x += w;
    inRow++;
    rowH = Math.max(rowH, h);
    return inst;
  });
}

/**
 * The default dashboard without a theme `widgets` setting (PRD §5.3): the four widgets of the old dashboard. The other
 * core groups (W1..W6) are in the catalogue (`available`) and are placed by the user, never added on their own.
 */
export const DEFAULT_CORE_WIDGET_IDS: readonly string[] = ["mentions", "triage", "my-tasks", "sessions"];

/**
 * Which widgets show, in which order: the theme's `widgets` setting picks and
 * orders (unknown ids are skipped, not fatal), then the user's permissions
 * filter. No setting = the default four (DEFAULT_CORE_WIDGET_IDS), then the
 * theme's own widgets; every other allowed widget, catalog-only groups included,
 * is available to add.
 */
export function resolveWidgets(opts: {
  settings?: DashboardLayoutSettings;
  registry?: readonly WidgetDescriptor[];
  can?: (p: Permission) => boolean;
  catalogOnly?: ReadonlySet<string>;
}): { shown: WidgetDescriptor[]; available: WidgetDescriptor[] } {
  const registry = opts.registry ?? WIDGETS;
  const can = opts.can ?? (() => true);
  const allowed = registry.filter((d) => (d.requires ?? []).every(can));
  // a theme's own widgets still show by default, after the core four, as before; a catalog-only group never does
  const catalogOnly = opts.catalogOnly ?? CATALOG_ONLY;
  const picked =
    opts.settings?.widgets ??
    [...DEFAULT_CORE_WIDGET_IDS, ...allowed.filter((d) => d.origin !== "core").map((d) => d.id)].filter((id) => !catalogOnly.has(id));
  const byId = new Map(allowed.map((d) => [d.id, d]));
  const shown: WidgetDescriptor[] = [];
  for (const id of picked) {
    const d = byId.get(id);
    if (d && !shown.includes(d)) shown.push(d);
  }
  return { shown, available: allowed.filter((d) => !shown.includes(d)) };
}
