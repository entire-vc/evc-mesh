// dashboard.layout v2 model for the view grid: the dashboards API document (PRD §5.5) and its
// conversion to the SDK's WidgetInstance (contract C) that widget views and theme layouts read.
import type { WidgetInstance as SdkWidgetInstance } from "../types";

export type DashboardPlacement = "top" | "bottom";

export interface WidgetInstance {
  id: string;
  widget_type: string;
  x: number;
  y: number;
  w: number;
  h: number;
  settings?: Record<string, unknown>;
}

export interface DashboardSummary {
  id: string;
  name: string;
  icon: string;
  placement: DashboardPlacement;
  position: number;
  virtual?: boolean;
}

export interface DashboardDoc extends DashboardSummary {
  version: number;
  /** Shared with the workspace (👥). */
  shared?: boolean;
  /** Only the owner edits (PRD §5.5); the server decides. */
  can_edit?: boolean;
  widgets: WidgetInstance[];
}

export const ROW_PX = 72;
export const GAP_PX = 16;
export const MAX_COLS = 12;

export type Cols = 12 | 6 | 1;

/** From 1280 px 12 columns, from 768 px 6, below that one column. */
export function colsForWidth(width: number): Cols {
  if (width >= 1280) return 12;
  if (width >= 768) return 6;
  return 1;
}

/** Reading order: row first, then column; id breaks exact ties so the order is stable. */
export function sortWidgets<T extends Pick<WidgetInstance, "x" | "y" | "id">>(widgets: readonly T[]): T[] {
  return [...widgets].sort((a, b) => a.y - b.y || a.x - b.x || a.id.localeCompare(b.id));
}

export interface Placed {
  widget: WidgetInstance;
  /** 1-based CSS grid lines; null on one column, where the order alone places the widget. */
  colStart: number | null;
  colEnd: number | null;
  rowStart: number;
  rowSpan: number;
}

const clampInt = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.trunc(Number.isFinite(v) ? v : lo)));

/**
 * Cells for a column count. 6 columns scale the 12-column edges (start and end rounded
 * separately, so neighbours that touched still touch); 1 column keeps only the order and the height.
 * Never drops a widget: bad numbers are clamped, not skipped.
 */
export function placeWidgets(widgets: readonly WidgetInstance[], cols: Cols): Placed[] {
  return sortWidgets(widgets).map((widget) => {
    const rowStart = clampInt(widget.y, 0, 10_000) + 1;
    const rowSpan = clampInt(widget.h, 1, MAX_COLS);
    if (cols === 1) return { widget, colStart: null, colEnd: null, rowStart, rowSpan };
    const x = clampInt(widget.x, 0, MAX_COLS - 1);
    const w = clampInt(widget.w, 1, MAX_COLS - x);
    const scale = cols / MAX_COLS;
    const start = Math.min(cols - 1, Math.round(x * scale));
    const end = Math.min(cols, Math.max(start + 1, Math.round((x + w) * scale)));
    return { widget, colStart: start + 1, colEnd: end + 1, rowStart, rowSpan };
  });
}

/** Pixel height of `h` rows, gaps included. */
export const spanPx = (h: number) => h * ROW_PX + (h - 1) * GAP_PX;

/**
 * Server types the registry knows under another id. Empty: triage-queue and agents-active have their own views (Move,
 * project, author and age live only in triage-queue's), and the v1 "triage" and "sessions" keep theirs.
 */
const TYPE_ALIASES: Record<string, string> = {};
export const coreWidgetId = (type: string) => TYPE_ALIASES[type] ?? type;

/** The API's instance as the SDK's: descriptor id under its current name, settings always an object. */
export const toSdkInstance = (w: WidgetInstance): SdkWidgetInstance => ({
  id: w.id, type: coreWidgetId(w.widget_type), x: w.x, y: w.y, w: w.w, h: w.h, settings: w.settings ?? {},
});
