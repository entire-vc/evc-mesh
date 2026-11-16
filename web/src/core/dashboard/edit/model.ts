// Pure layout operations of the edit mode: no React, no network. The same functions serve mouse drops
// (react-grid-layout hands back a layout), the keyboard and the phone buttons, so one rule set decides collisions.
import { cloneLayout, moveElement, verticalCompactor, type LayoutItem as RglItem } from "react-grid-layout/core";
import type { CatalogType, Cells, LayoutItem } from "./types";

export const COLS_DESKTOP = 12;
export const COLS_TABLET = 6;

export function clamp(n: number, lo: number, hi: number): number {
  return Math.min(Math.max(n, lo), hi);
}

/** Size limited to the type's min..max and to the grid width. A type unknown to the catalog keeps its size. */
export function clampCells(size: Cells, type: CatalogType | undefined, cols = COLS_DESKTOP): Cells {
  const minW = type?.size.min.w ?? 1;
  const minH = type?.size.min.h ?? 1;
  const maxW = Math.min(type?.size.max.w ?? cols, cols);
  const maxH = type?.size.max.h ?? 12;
  return { w: clamp(size.w, Math.min(minW, maxW), maxW), h: clamp(size.h, minH, Math.max(maxH, minH)) };
}

const toRgl = (items: LayoutItem[], types: Map<string, CatalogType>, cols: number): RglItem[] =>
  items.map((it) => {
    const t = types.get(it.type);
    return {
      i: it.id,
      x: it.x,
      y: it.y,
      w: it.w,
      h: it.h,
      minW: Math.min(t?.size.min.w ?? 1, cols),
      minH: t?.size.min.h ?? 1,
      maxW: Math.min(t?.size.max.w ?? cols, cols),
      maxH: t?.size.max.h,
    };
  });

/** Applies positions and sizes of an RGL layout onto the items, clamped, preserving everything else. */
export function applyGeometry(items: LayoutItem[], rgl: readonly RglItem[], types: Map<string, CatalogType>, cols = COLS_DESKTOP): LayoutItem[] {
  const byId = new Map(rgl.map((l) => [l.i, l]));
  return items.map((it) => {
    const l = byId.get(it.id);
    if (!l) return it;
    const { w, h } = clampCells({ w: l.w, h: l.h }, types.get(it.type), cols);
    return { ...it, x: clamp(l.x, 0, cols - w), y: Math.max(l.y, 0), w, h };
  });
}

/** Moves everything up until nothing can go higher: "gaps collapse upward". */
export function compactItems(items: LayoutItem[], types: Map<string, CatalogType>, cols = COLS_DESKTOP): LayoutItem[] {
  const out = verticalCompactor.compact(toRgl(items, types, cols), cols);
  return applyGeometry(items, out, types, cols);
}

function collides(a: { x: number; y: number; w: number; h: number }, b: { x: number; y: number; w: number; h: number }) {
  return a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
}

/** First free place for a w x h block, scanning rows top to bottom, left to right. */
export function firstFreeSpot(items: readonly LayoutItem[], size: Cells, cols = COLS_DESKTOP): { x: number; y: number } {
  const w = Math.min(size.w, cols);
  const bottom = items.reduce((m, it) => Math.max(m, it.y + it.h), 0);
  for (let y = 0; y <= bottom; y++) {
    for (let x = 0; x + w <= cols; x++) {
      if (!items.some((it) => collides({ x, y, w, h: size.h }, it))) return { x, y };
    }
  }
  return { x: 0, y: bottom };
}

let seq = 0;
export function newInstanceId(): string {
  const c = globalThis.crypto;
  return c?.randomUUID ? c.randomUUID() : `w${Date.now().toString(36)}${(seq++).toString(36)}`;
}

export function addItem(items: LayoutItem[], type: CatalogType, cols = COLS_DESKTOP): { items: LayoutItem[]; added: LayoutItem } {
  const size = clampCells(type.size.default, type, cols);
  const spot = firstFreeSpot(items, size, cols);
  const added: LayoutItem = { id: newInstanceId(), type: type.id, ...spot, ...size, fresh: true };
  return { items: [...items, added], added };
}

export function removeItem(items: LayoutItem[], id: string): LayoutItem[] {
  return items.filter((it) => it.id !== id);
}

/** Puts a removed item back where it was (same geometry if free, else the first free place). */
export function restoreItem(items: LayoutItem[], item: LayoutItem, index: number, cols = COLS_DESKTOP): LayoutItem[] {
  const free = !items.some((o) => collides(item, o));
  const placed = free ? item : { ...item, ...firstFreeSpot(items, item, cols) };
  const next = [...items];
  next.splice(Math.min(index, next.length), 0, placed);
  return next;
}

/** Keyboard / button move by whole cells; neighbours make way exactly as they do under the mouse. */
export function moveBy(items: LayoutItem[], id: string, dx: number, dy: number, types: Map<string, CatalogType>, cols = COLS_DESKTOP): LayoutItem[] {
  const rgl = cloneLayout(toRgl(items, types, cols));
  const l = rgl.find((r) => r.i === id);
  if (!l) return items;
  const x = clamp(l.x + dx, 0, cols - l.w);
  const y = Math.max(l.y + dy, 0);
  const moved = moveElement(rgl, l, x, y, true, false, "vertical", cols);
  return applyGeometry(items, verticalCompactor.compact(moved, cols), types, cols);
}

/** Keyboard / button resize by whole cells, limited to min..max. */
export function resizeBy(items: LayoutItem[], id: string, dw: number, dh: number, types: Map<string, CatalogType>, cols = COLS_DESKTOP): LayoutItem[] {
  const it = items.find((i) => i.id === id);
  if (!it) return items;
  const size = clampCells({ w: it.w + dw, h: it.h + dh }, types.get(it.type), cols);
  if (size.w === it.w && size.h === it.h) return items;
  const w = size.w;
  const rgl = cloneLayout(toRgl(items, types, cols));
  const l = rgl.find((r) => r.i === id)!;
  l.w = w;
  l.h = size.h;
  // push a neighbour that is now overlapped
  const moved = moveElement(rgl, l, Math.min(l.x, cols - w), l.y, true, false, "vertical", cols);
  return applyGeometry(items, verticalCompactor.compact(moved, cols), types, cols);
}

/** Reading order of the single phone column: by y, then x. */
export function readingOrder(items: readonly LayoutItem[]): LayoutItem[] {
  return [...items].sort((a, b) => a.y - b.y || a.x - b.x);
}

/**
 * Phone "up" / "down": swaps the item with its neighbour in reading order and re-flows the whole layout in that
 * order, each item keeping its own size. The desktop layout stays valid and now reads in the new order.
 */
export function moveInOrder(items: LayoutItem[], id: string, dir: -1 | 1, cols = COLS_DESKTOP): LayoutItem[] {
  const order = readingOrder(items);
  const i = order.findIndex((it) => it.id === id);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= order.length) return items;
  [order[i], order[j]] = [order[j]!, order[i]!];
  const placed: LayoutItem[] = [];
  for (const it of order) {
    placed.push({ ...it, ...firstFreeSpotBelowPrevious(placed, it, cols) });
  }
  const byId = new Map(placed.map((p) => [p.id, p]));
  return items.map((it) => byId.get(it.id) ?? it);
}

// Flow placement that never lets a later item jump above an earlier one, so reading order == array order.
function firstFreeSpotBelowPrevious(placed: readonly LayoutItem[], it: LayoutItem, cols: number): { x: number; y: number } {
  const minY = placed.length ? Math.max(...placed.map((p) => p.y)) : 0;
  const w = Math.min(it.w, cols);
  const bottom = placed.reduce((m, p) => Math.max(m, p.y + p.h), 0);
  for (let y = minY; y <= bottom; y++) {
    for (let x = 0; x + w <= cols; x++) {
      if (!placed.some((p) => collides({ x, y, w, h: it.h }, p))) {
        // must also not read before the previous item
        const prev = placed[placed.length - 1];
        if (!prev || y > prev.y || (y === prev.y && x >= prev.x + prev.w)) return { x, y };
      }
    }
  }
  return { x: 0, y: bottom };
}

/** 6-column view of a 12-column layout (§5.5: widths scale proportionally). */
export function scaleItems(items: LayoutItem[], from: number, to: number): LayoutItem[] {
  if (from === to) return items;
  const k = to / from;
  return items.map((it) => {
    const w = clamp(Math.max(1, Math.round(it.w * k)), 1, to);
    return { ...it, w, x: clamp(Math.round(it.x * k), 0, to - w) };
  });
}

/** Type limits as seen in a narrower grid: min rounds up, max rounds down (never below min), so a view-size within them maps back within the real ones. */
export function scaleTypes(types: Map<string, CatalogType>, fullCols: number, viewCols: number): Map<string, CatalogType> {
  if (fullCols === viewCols) return types;
  const k = viewCols / fullCols;
  const out = new Map<string, CatalogType>();
  for (const [id, t] of types) {
    const minW = clamp(Math.ceil(t.size.min.w * k), 1, viewCols);
    const maxW = clamp(Math.max(Math.floor(t.size.max.w * k), minW), 1, viewCols);
    out.set(id, { ...t, size: { ...t.size, min: { ...t.size.min, w: minW }, max: { ...t.size.max, w: maxW } } });
  }
  return out;
}

/**
 * Back from the tablet view: items whose view x/w did not change keep their exact full-width x/w; the rest are
 * scaled up and then clamped to the real min..max of their type (rounding must never break a limit).
 */
export function unscaleItems(view: LayoutItem[], original: LayoutItem[], viewCols: number, fullCols: number, types?: Map<string, CatalogType>): LayoutItem[] {
  if (viewCols === fullCols) return view;
  const k = fullCols / viewCols;
  const orig = new Map(original.map((o) => [o.id, o]));
  const origView = new Map(scaleItems(original, fullCols, viewCols).map((o) => [o.id, o]));
  return view.map((v) => {
    const o = orig.get(v.id);
    const ov = origView.get(v.id);
    if (o && ov && ov.x === v.x && ov.w === v.w) return { ...v, x: o.x, w: o.w };
    const { w } = clampCells({ w: Math.round(v.w * k), h: v.h }, types?.get(v.type), fullCols);
    return { ...v, w, x: clamp(Math.round(v.x * k), 0, fullCols - w) };
  });
}

export function sameLayout(a: readonly LayoutItem[], b: readonly LayoutItem[]): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/**
 * Pairs the items this client created (sent without an id) with the ids the server gave them.
 * Candidates are the returned widgets whose id was not among the sent ids; an exact match on
 * type and geometry wins, otherwise the first unclaimed one of the same type, in order.
 */
export function reconcileIds(sent: readonly LayoutItem[], returned: readonly LayoutItem[]): Map<string, string> {
  const known = new Set(sent.filter((i) => !i.fresh).map((i) => i.id));
  const pool = returned.filter((r) => !known.has(r.id));
  const map = new Map<string, string>();
  const take = (pred: (r: LayoutItem) => boolean, from: LayoutItem) => {
    const at = pool.findIndex(pred);
    if (at >= 0) map.set(from.id, pool.splice(at, 1)[0]!.id);
    return at >= 0;
  };
  const fresh = sent.filter((i) => i.fresh);
  const rest = fresh.filter((f) => !take((r) => r.type === f.type && r.x === f.x && r.y === f.y && r.w === f.w && r.h === f.h, f));
  for (const f of rest) take((r) => r.type === f.type, f);
  return map;
}

/** Replaces temporary ids by the server's and drops the `fresh` mark; other fields (newer edits) are kept. */
export function remapIds(items: readonly LayoutItem[], map: ReadonlyMap<string, string>): LayoutItem[] {
  if (!map.size) return items as LayoutItem[];
  return items.map((i) => {
    const id = map.get(i.id);
    if (!id) return i;
    const { fresh: _fresh, ...rest } = i;
    return { ...rest, id };
  });
}

/**
 * An item whose id the server does not know (deleted and saved, then restored by Undo or Cancel changes) is
 * a new instance for the server, which gives it a fresh id: send it as new so the answer can be matched back.
 */
export function markUnconfirmed(items: readonly LayoutItem[], knownIds: ReadonlySet<string>): LayoutItem[] {
  return items.map((i) => (i.fresh || knownIds.has(i.id) ? i : { ...i, fresh: true }));
}
