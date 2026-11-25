import {
  type CSSProperties,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import type { EditorView } from "@milkdown/kit/prose/view";
import { addColAfterCommand, addRowAfterCommand } from "@milkdown/kit/preset/gfm";
import { cn } from "@/lib/cn";
import { cursorToCell, selectCellForMenu, tableStartAt } from "@/lib/milkdown/block-state";
import {
  type EditorBlockControlsProps,
  MENU_PANEL_CLASS,
  TableMenuItems,
  menuItems,
  menuKeyHandler,
  useTableMenuGroups,
} from "@/components/editor-block-controls";

/**
 * Table actions offered from the table itself, for people who never find the
 * toolbar's table button (reported as "rows and columns cannot be added at
 * all"): a right-click menu inside the table, and "+" strips along the right
 * and bottom edges that appear only while the pointer is over the table.
 *
 * Nothing here is visible until the table is hovered or right-clicked, and
 * nothing is mounted in a read-only editor — the host does not render this.
 *
 * Cost, on purpose: two delegated `mouseover` / `mouseleave` listeners on the
 * editor body (they fire when the pointer crosses an element boundary, not on
 * every pixel), one ResizeObserver on the hovered table while it is hovered,
 * and no ProseMirror decorations. Typing re-renders none of it: the handles
 * read no editor state, and the menu reads the block store, which notifies only
 * when the cursor's table context changes.
 */
export interface TableAffordancesProps extends EditorBlockControlsProps {
  /** The editor body: the element that holds the ProseMirror root. */
  containerRef: RefObject<HTMLElement | null>;
}

const VIEWPORT_MARGIN = 8;
/** Thickness of a "+" strip, px. Fits the editor body's 12px side padding. */
const STRIP = 12;

interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}

/** The cell element the caret is in, if any. */
function caretCell(view: EditorView): Element | null | undefined {
  const { node } = view.domAtPos(view.state.selection.from);
  return (node instanceof Element ? node : node.parentElement)?.closest("td, th");
}

export function TableAffordances(props: TableAffordancesProps) {
  const { containerRef, withView } = props;
  const [menuAt, setMenuAt] = useState<{ x: number; y: number } | null>(null);

  // ---- right-click ------------------------------------------------------
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const onContextMenu = (e: MouseEvent) => {
      const target = e.target as Element | null;
      if (!target) return;
      // Links and images keep the browser's own menu: "open in new tab" and
      // "copy image" are why people right-click those.
      if (target.closest("a, img")) return;
      // A long-press on a touch screen also fires `contextmenu`; there the
      // browser's own menu is how text gets selected and pasted, so it stays.
      // Phones add rows and columns with the strips (a tap shows them) and
      // the toolbar menu.
      if ((e as PointerEvent).pointerType === "touch") return;
      const opened = withView((view) => {
        if (!view.editable || !view.dom.contains(target)) return null;
        const cell = target.closest("td, th");
        if (cell && view.dom.contains(cell)) {
          const ok = selectCellForMenu(view.posAtDOM(cell, 0))(view.state, view.dispatch);
          return ok ? { x: e.clientX, y: e.clientY } : null;
        }
        // The keyboard's menu key / Shift+F10 fires on the editor root, not
        // on a cell: open at the caret when the caret is in a table.
        // Anchored under the caret's cell: a cell always has a box, a caret in
        // an empty cell may not.
        if (target === view.dom && tableStartAt(view.state, view.state.selection.from) !== null) {
          const box = caretCell(view)?.getBoundingClientRect();
          return box ? { x: box.left, y: box.bottom } : null;
        }
        return null;
      });
      if (!opened) return;
      e.preventDefault();
      setMenuAt(opened);
    };
    container.addEventListener("contextmenu", onContextMenu);
    return () => container.removeEventListener("contextmenu", onContextMenu);
  }, [containerRef, withView]);

  return (
    <>
      <TableHandles {...props} />
      {menuAt && (
        <TableContextMenu {...props} x={menuAt.x} y={menuAt.y} onClose={() => setMenuAt(null)} />
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Right-click menu
// ---------------------------------------------------------------------------

function TableContextMenu(
  props: TableAffordancesProps & { x: number; y: number; onClose: () => void },
) {
  const { ns, store, withView, x, y, onClose } = props;
  const { t } = useTranslation(ns);
  const state = useSyncExternalStore(store.subscribe, store.get);
  const panelRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<CSSProperties>({ left: x, top: y, visibility: "hidden" });
  const groups = useTableMenuGroups(props, onClose);

  // The cursor left the table (an edit elsewhere, a deleted table): nothing
  // left to act on.
  useEffect(() => {
    if (!state.inTable) onClose();
  }, [state.inTable, onClose]);

  useEffect(() => {
    const inPanel = (n: EventTarget | null) => !!panelRef.current?.contains(n as Node);
    const onDown = (e: Event) => {
      if (!inPanel(e.target)) onClose();
    };
    // Focus usually stays in the editor after a right-click, so the keys are
    // caught on the document, in the capture phase, before the editor acts on
    // them: Escape closes, Up / Down step into the menu.
    const onKey = (e: KeyboardEvent) => {
      const down = e.key === "ArrowDown";
      const esc = e.key === "Escape";
      if (!esc && !((down || e.key === "ArrowUp") && !inPanel(document.activeElement))) return;
      e.preventDefault();
      e.stopPropagation();
      if (esc) {
        onClose();
        withView((view) => view.focus());
      } else {
        const items = menuItems(panelRef.current);
        (down ? items[0] : items[items.length - 1])?.focus();
      }
    };
    // A fixed panel would drift away from the cell it was opened on. Window
    // scrolls and inner scrolls both reach the document's capture phase.
    const types = ["mousedown", "scroll"] as const;
    for (const type of types) document.addEventListener(type, onDown, true);
    document.addEventListener("keydown", onKey, true);
    return () => {
      for (const type of types) document.removeEventListener(type, onDown, true);
      document.removeEventListener("keydown", onKey, true);
    };
  }, [onClose, withView]);

  // Open at the pointer, flipped and clamped to stay on screen.
  useLayoutEffect(() => {
    const panel = panelRef.current;
    if (!panel) return;
    const { width, height } = panel.getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    const vh = window.innerHeight;
    const left = x + width > vw - VIEWPORT_MARGIN ? Math.max(VIEWPORT_MARGIN, x - width) : x;
    const top = y + height > vh - VIEWPORT_MARGIN ? Math.max(VIEWPORT_MARGIN, y - height) : y;
    const room = vh - top - VIEWPORT_MARGIN;
    setPos({
      left,
      top,
      ...(height > room ? { maxHeight: Math.max(160, room), overflowY: "auto" as const } : {}),
    });
  }, [x, y]);

  return createPortal(
    <div
      ref={panelRef}
      role="menu"
      aria-label={t("editor.tableMenu")}
      data-table-context-menu=""
      onKeyDown={menuKeyHandler(panelRef, onClose)}
      onContextMenu={(e) => e.preventDefault()}
      style={pos}
      className={cn("fixed z-50", MENU_PANEL_CLASS)}
    >
      <TableMenuItems groups={groups} />
    </div>,
    document.body,
  );
}

// ---------------------------------------------------------------------------
// "+" strips
// ---------------------------------------------------------------------------

function TableHandles({ ns, containerRef, withView, runCommand }: TableAffordancesProps) {
  const { t } = useTranslation(ns);
  const [table, setTable] = useState<HTMLElement | null>(null);
  const [box, setBox] = useState<Box | null>(null);
  const anchorRef = useRef<HTMLDivElement>(null);
  const tableRef = useRef<HTMLElement | null>(null);

  // Which table is under the pointer. Delegated on the editor body, so tables
  // created or replaced by the editor need no wiring of their own.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const hover = (next: HTMLElement | null) => {
      if (tableRef.current === next) return;
      tableRef.current = next;
      setTable(next);
    };
    const onOver = (e: MouseEvent) => {
      const target = e.target as Element | null;
      if (!target) return;
      // Moving from the table onto its own strip keeps the strips.
      if (anchorRef.current?.contains(target)) return;
      const found = target.closest("table");
      const editable = withView((view) => view.editable && view.dom.contains(found)) ?? false;
      hover(found && editable ? (found as HTMLElement) : null);
    };
    const onLeave = () => hover(null);
    container.addEventListener("mouseover", onOver);
    container.addEventListener("mouseleave", onLeave);
    return () => {
      container.removeEventListener("mouseover", onOver);
      container.removeEventListener("mouseleave", onLeave);
    };
  }, [containerRef, withView]);

  // Where the hovered table is, relative to the anchor the strips hang from.
  // Recomputed when the table or the editor changes size — a row added, the
  // window resized — and never on a keystroke that changes neither.
  useLayoutEffect(() => {
    if (!table) {
      setBox(null);
      return;
    }
    const measure = () => {
      const anchor = anchorRef.current;
      if (!anchor || !table.isConnected) {
        tableRef.current = null;
        setTable(null);
        return;
      }
      const a = anchor.getBoundingClientRect();
      const r = table.getBoundingClientRect();
      setBox({
        left: r.left - a.left,
        top: r.top - a.top,
        width: r.width,
        height: r.height,
      });
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(table);
    if (containerRef.current) ro.observe(containerRef.current);
    return () => ro.disconnect();
  }, [table, containerRef]);

  const append = (what: "row" | "col") => {
    const el = tableRef.current;
    if (!el) return;
    const cell = el.querySelector("td, th");
    if (!cell) return;
    const tablePos = withView((view: EditorView) => {
      const start = tableStartAt(view.state, view.posAtDOM(cell, 0));
      if (start === null) return null;
      // Aim the existing command at the end of the table, whatever cell the
      // cursor was in: the last row for a row; for a column, the last column
      // of the row the cursor is in, or of the header row.
      cursorToCell(start, what === "row" ? -1 : 0, -1)(view.state, view.dispatch);
      return start;
    });
    if (tablePos === null || tablePos === undefined) return;
    runCommand(what === "row" ? addRowAfterCommand.key : addColAfterCommand.key);
    // Land in the new cell, ready to type.
    withView((view) => {
      cursorToCell(
        tablePos,
        what === "row" ? -1 : 0,
        what === "row" ? 0 : -1,
      )(view.state, view.dispatch);
      // Show the whole new cell, also inside the table's own sideways
      // scroll: a column appended to a wide table on a phone lands
      // off-screen, and an empty one is too narrow for the caret alone to
      // bring it into view — the click would look like it did nothing.
      caretCell(view)?.scrollIntoView?.({
        block: "nearest",
        inline: "nearest",
      });
      view.focus();
    });
  };

  return (
    // A zero-size, in-flow anchor: the strips are positioned from it, so they
    // scroll with the text without a scroll listener and without making the
    // editor body a positioning context. Inline styles, because the body's
    // fill rule turns its direct `div` children into flex boxes.
    <div
      ref={anchorRef}
      data-table-handles=""
      style={{
        position: "relative",
        display: "block",
        flex: "none",
        height: 0,
        width: 0,
      }}
    >
      {box &&
        (["col", "row"] as const).map((what) => {
          const col = what === "col";
          const label = t(col ? "editor.addColumnEnd" : "editor.addRowEnd");
          return (
            <button
              key={what}
              type="button"
              tabIndex={-1}
              aria-label={label}
              title={label}
              data-table-handle={what}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => append(what)}
              // Look in doc-editor.css: a line along the edge, a round "+" on it.
              className="mesh-table-strip"
              style={{
                left: col ? box.left + box.width - 1 : box.left,
                top: col ? box.top : box.top + box.height - 1,
                width: col ? STRIP : box.width,
                height: col ? box.height : STRIP,
              }}
            >
              <span aria-hidden="true">
                <Plus className="h-3 w-3" strokeWidth={2.5} />
              </span>
            </button>
          );
        })}
    </div>
  );
}
