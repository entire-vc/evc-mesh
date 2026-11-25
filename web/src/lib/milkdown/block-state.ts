import type { EditorState, Transaction } from "@milkdown/kit/prose/state";
import { Plugin, PluginKey, TextSelection } from "@milkdown/kit/prose/state";
import type { Node as ProseNode } from "@milkdown/kit/prose/model";
import {
  deleteColumn,
  deleteTable,
  isInTable,
  selectedRect,
  TableMap,
} from "@milkdown/kit/prose/tables";
import { $prose } from "@milkdown/kit/utils";

/**
 * What the toolbar needs to know about where the cursor is.
 *
 * Kept to primitives so two snapshots can be compared field by field: the
 * store below only notifies when one of these actually changes, which is what
 * keeps typing from re-rendering the toolbar on every keystroke.
 */
export interface BlockState {
  /**
   * The block under the cursor: 0 for a paragraph, 1–6 for a heading, null for
   * anything a heading cannot replace (code block, a selection over mixed
   * blocks) — the picker shows that as "no single type" instead of lying.
   */
  level: number | null;
  /** False where a heading is not allowed — table cells hold paragraphs only. */
  canSetLevel: boolean;
  /** The cursor is inside a table. Everything below is meaningless otherwise. */
  inTable: boolean;
  /** The selection touches the header row. */
  inHeader: boolean;
  /** Rows including the header, and columns, of the table under the cursor. */
  rows: number;
  cols: number;
  /** Rows / columns the selection covers — one each for a plain cursor. */
  selectedRows: number;
  selectedCols: number;
  /** GFM column alignment of the column the selection starts in. */
  align: Alignment | null;
}

export type Alignment = "left" | "center" | "right";

export const EMPTY_BLOCK_STATE: BlockState = {
  level: null,
  canSetLevel: false,
  inTable: false,
  inHeader: false,
  rows: 0,
  cols: 0,
  selectedRows: 0,
  selectedCols: 0,
  align: null,
};

function blockLevel(node: ProseNode): number | null {
  if (node.type.name === "paragraph") return 0;
  if (node.type.name === "heading") return Number(node.attrs.level) || null;
  return null;
}

export function computeBlockState(state: EditorState): BlockState {
  const { $from, $to } = state.selection;
  const from = blockLevel($from.parent);
  const to = blockLevel($to.parent);
  const level = from === to ? from : null;

  if (!isInTable(state)) {
    // A heading replaces a paragraph or another heading in place; inside a
    // code block it would mean losing the fence, so the picker stays out.
    return { ...EMPTY_BLOCK_STATE, level, canSetLevel: level !== null };
  }

  const rect = selectedRect(state);
  const headerCell = rect.table.nodeAt(rect.map.map[rect.left] ?? 0);
  const align = (headerCell?.attrs.alignment as Alignment | undefined) ?? null;
  return {
    level,
    canSetLevel: false,
    inTable: true,
    inHeader: rect.top === 0,
    rows: rect.map.height,
    cols: rect.map.width,
    selectedRows: rect.bottom - rect.top,
    selectedCols: rect.right - rect.left,
    align,
  };
}

function sameState(a: BlockState, b: BlockState): boolean {
  return (Object.keys(a) as (keyof BlockState)[]).every((k) => a[k] === b[k]);
}

// ---------------------------------------------------------------------------
// The store the toolbar reads, fed by a ProseMirror plugin view
// ---------------------------------------------------------------------------

export interface BlockStateStore {
  get: () => BlockState;
  subscribe: (listener: () => void) => () => void;
  set: (next: BlockState) => void;
  /**
   * Record a reading without notifying anyone. Only for the editor's first
   * reading: see blockStatePlugin.
   */
  prime: (next: BlockState) => void;
}

export function createBlockStateStore(): BlockStateStore {
  let current = EMPTY_BLOCK_STATE;
  const listeners = new Set<() => void>();
  return {
    get: () => current,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    set: (next) => {
      if (sameState(current, next)) return;
      current = next;
      listeners.forEach((l) => l());
    },
    prime: (next) => {
      if (!sameState(current, next)) current = next;
    },
  };
}

const blockStateKey = new PluginKey("mesh-block-state");

/**
 * Reports the cursor's block and table context into `store` after every
 * transaction, selection-only ones included.
 *
 * A plugin view rather than the markdown listener: moving the cursor into a
 * table changes no markdown, and that is exactly when the table menu must wake
 * up. Created per editor instance — Milkdown plugins carry per-editor state.
 */
export function blockStatePlugin(store: BlockStateStore) {
  return $prose(
    () =>
      new Plugin({
        key: blockStateKey,
        view: (view) => {
          // The first reading is primed, not announced. The toolbar is already
          // mounted by now (the editor is created in an effect), and a store
          // notification would cost it a synchronous commit of its own, one
          // more on every task open. It does not need one: Milkdown
          // flips `loading` to false as soon as creation resolves, that commit
          // re-renders the host and with it the controls, and they read the
          // primed value then. Every later transaction notifies as usual.
          store.prime(computeBlockState(view.state));
          return {
            update: (v) => store.set(computeBlockState(v.state)),
            destroy: () => store.set(EMPTY_BLOCK_STATE),
          };
        },
      }),
  );
}

// ---------------------------------------------------------------------------
// Table edits the presets do not ship in a GFM-safe form
// ---------------------------------------------------------------------------

type Dispatch = ((tr: Transaction) => void) | undefined;

/**
 * Delete the selected row(s) without ever producing a table GFM cannot hold.
 *
 * The schema is `table_header_row table_row+`: a table is a header and at least
 * one body row. prosemirror-tables' `deleteRow` knows nothing about that, so
 * deleting the header would leave a headerless table and deleting the last
 * body row a header-only one — both outside the schema, which is "the table
 * breaks". So:
 * - a deletion that would leave fewer than two rows is refused (the menu
 *   disables it and offers "delete table" instead);
 * - deleting the header promotes the first surviving body row to header,
 *   keeping each column's alignment, because in GFM alignment lives in the
 *   header's delimiter row.
 */
export function deleteTableRow(state: EditorState, dispatch?: Dispatch): boolean {
  if (!isInTable(state)) return false;
  const rect = selectedRect(state);
  const { table, tableStart, map } = rect;
  if (map.height - (rect.bottom - rect.top) < 2) return false;
  if (!dispatch) return true;

  const tr = state.tr;
  if (rect.top === 0) {
    const header = table.child(0);
    const promoted = table.child(rect.bottom);
    const cells: ProseNode[] = [];
    promoted.forEach((cell, _offset, i) => {
      const headerCell = header.maybeChild(i);
      const type = headerCell?.type ?? header.type.contentMatch.defaultType;
      if (!type) return;
      cells.push(
        type.create(
          { ...cell.attrs, alignment: headerCell?.attrs.alignment ?? cell.attrs.alignment },
          cell.content,
        ),
      );
    });
    let to = tableStart;
    for (let i = 0; i <= rect.bottom; i++) to += table.child(i).nodeSize;
    tr.replaceWith(tableStart, to, header.type.create(header.attrs, cells));
    tr.setSelection(TextSelection.near(tr.doc.resolve(tableStart + 2)));
  } else {
    let from = tableStart;
    for (let i = 0; i < rect.top; i++) from += table.child(i).nodeSize;
    let to = from;
    for (let i = rect.top; i < rect.bottom; i++) to += table.child(i).nodeSize;
    tr.delete(from, to);
    // Land on the row that took the deleted one's place, or the one above it
    // when the last row went.
    const atEnd = from >= tr.doc.resolve(tableStart).end();
    tr.setSelection(TextSelection.near(tr.doc.resolve(from), atEnd ? -1 : 1));
  }
  dispatch(tr.scrollIntoView());
  return true;
}

/** Delete the selected column(s); refused when it would take the last one. */
export function deleteTableColumn(state: EditorState, dispatch?: Dispatch): boolean {
  if (!isInTable(state)) return false;
  const rect = selectedRect(state);
  if (rect.map.width - (rect.right - rect.left) < 1) return false;
  return deleteColumn(state, dispatch);
}

// ---------------------------------------------------------------------------
// Pointing the selection at a cell, for actions started from the table itself
// ---------------------------------------------------------------------------

/**
 * Where the table that contains `pos` starts, or null outside a table.
 * `pos` is any position inside it — what `view.posAtDOM(cellElement, 0)` gives.
 */
export function tableStartAt(state: EditorState, pos: number): number | null {
  return ancestorStart(state, pos, (role) => role === "table");
}

/** Start of the innermost node around `pos` whose table role passes `test`. */
function ancestorStart(
  state: EditorState,
  pos: number,
  test: (role: unknown) => boolean,
): number | null {
  if (pos < 0 || pos > state.doc.content.size) return null;
  const $pos = state.doc.resolve(pos);
  for (let d = $pos.depth; d > 0; d--) {
    if (test($pos.node(d).type.spec.tableRole)) return $pos.before(d);
  }
  return null;
}

/**
 * Put the cursor into the cell at (`row`, `col`) of the table at `tablePos`.
 * Negative indices count from the end: -1 is the last row / column.
 *
 * The "+" handles use it to aim the existing add-row / add-column commands at
 * the end of the table, whatever cell the cursor happened to be in, and then
 * to land the cursor in the cell they just made.
 */
export function cursorToCell(
  tablePos: number,
  row: number,
  col: number,
): (state: EditorState, dispatch?: Dispatch) => boolean {
  return (state, dispatch) => {
    const table = state.doc.nodeAt(tablePos);
    if (table?.type.spec.tableRole !== "table") return false;
    const { width, height, map } = TableMap.get(table);
    const r = row < 0 ? height + row : row;
    const c = col < 0 ? width + col : col;
    if (r < 0 || r >= height || c < 0 || c >= width) return false;
    const at = tablePos + 2 + map[r * width + c]!;
    dispatch?.(state.tr.setSelection(TextSelection.near(state.doc.resolve(at))));
    return true;
  };
}

/**
 * Before a right-click menu opens on a cell: move the cursor into that cell,
 * unless the cell is already part of the selection — a multi-cell selection
 * made to delete two rows at once must survive the right-click on it.
 *
 * `pos` is a position inside the cell. Returns false when it is not in one.
 */
export function selectCellForMenu(
  pos: number,
): (state: EditorState, dispatch?: Dispatch) => boolean {
  return (state, dispatch) => {
    const cellPos = ancestorStart(state, pos, (r) => r === "cell" || r === "header_cell");
    if (cellPos === null) return false;
    // Already in the selection: a caret in this cell, or one of the cells of
    // a multi-cell selection.
    const end = cellPos + state.doc.nodeAt(cellPos)!.nodeSize;
    if (state.selection.ranges.some((r) => r.$from.pos > cellPos && r.$to.pos < end)) return true;
    if (dispatch) {
      dispatch(state.tr.setSelection(TextSelection.near(state.doc.resolve(cellPos + 1))));
    }
    return true;
  };
}

export function deleteWholeTable(state: EditorState, dispatch?: Dispatch): boolean {
  return deleteTable(state, dispatch);
}

/**
 * Align the selected column(s), every cell of them.
 *
 * The preset's `setAlignCommand` sets the attribute on the selected cells only,
 * and GFM writes alignment from the header row — so aligning from a body cell
 * changed the screen but not the saved markdown. Writing the whole column keeps
 * the two in agreement, and the delimiter row (`:---:`) is what the file gets.
 */
export function setColumnAlignment(
  align: Alignment,
): (state: EditorState, dispatch?: Dispatch) => boolean {
  return (state, dispatch) => {
    if (!isInTable(state)) return false;
    if (!dispatch) return true;
    const rect = selectedRect(state);
    const { map, table, tableStart } = rect;
    const tr = state.tr;
    for (let row = 0; row < map.height; row++) {
      for (let col = rect.left; col < rect.right; col++) {
        const rel = map.map[row * map.width + col];
        if (rel === undefined) continue;
        const cell = table.nodeAt(rel);
        if (!cell || cell.attrs.alignment === align) continue;
        tr.setNodeMarkup(tableStart + rel, undefined, { ...cell.attrs, alignment: align });
      }
    }
    dispatch(tr);
    return true;
  };
}
