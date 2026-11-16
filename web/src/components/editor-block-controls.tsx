import {
  type KeyboardEvent as ReactKeyboardEvent,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import {
  BetweenHorizontalEnd,
  BetweenHorizontalStart,
  BetweenVerticalEnd,
  BetweenVerticalStart,
  ChevronDown,
  Columns3,
  Rows3,
  TableProperties,
  TextAlignCenter,
  TextAlignEnd,
  TextAlignStart,
  Trash2,
} from "lucide-react";
import type { EditorView } from "@milkdown/kit/prose/view";
import { turnIntoTextCommand, wrapInHeadingCommand } from "@milkdown/kit/preset/commonmark";
import {
  addColAfterCommand,
  addColBeforeCommand,
  addRowAfterCommand,
  addRowBeforeCommand,
} from "@milkdown/kit/preset/gfm";
import type { callCommand } from "@milkdown/kit/utils";
import { cn } from "@/lib/cn";
import {
  type Alignment,
  type BlockStateStore,
  deleteTableColumn,
  deleteTableRow,
  deleteWholeTable,
  setColumnAlignment,
} from "@/lib/milkdown/block-state";

/**
 * Block type and table controls, shared by the Docs editor and the task
 * description / comment editor.
 *
 * One component for both toolbars on purpose: the two editors are the same
 * engine and their toolbars have already drifted once. The strings live in each
 * editor's own namespace (`docs` / `misc`) under the same keys, because each
 * surface loads only its own chunk.
 */
export interface EditorBlockControlsProps {
  /** i18n namespace the hosting editor already loads. */
  ns: "docs" | "misc";
  store: BlockStateStore;
  runCommand: (key: Parameters<typeof callCommand>[0], payload?: unknown) => void;
  withView: <T>(fn: (view: EditorView) => T) => T | null;
}

/** Same pairings as the toolbar buttons (contrast numbers in doc-editor.tsx). */
const HOVER = "hover:bg-secondary hover:text-secondary-foreground";
const PRESSED =
  "active:bg-secondary active:text-secondary-foreground active:ring-1 active:ring-inset active:ring-primary";

const LEVELS = [1, 2, 3, 4, 5, 6] as const;

// ---------------------------------------------------------------------------
// Text / H1–H6
// ---------------------------------------------------------------------------

export function BlockTypeSelect({ ns, store, runCommand }: EditorBlockControlsProps) {
  const { t } = useTranslation(ns);
  const state = useSyncExternalStore(store.subscribe, store.get);

  // A native select: keyboard, screen readers and the phone's own picker for
  // free, and it is one control in both the wide and the compact toolbar.
  // Picking from it moves focus off the editor, but ProseMirror keeps its
  // selection in state and runCommand hands focus back.
  const value = state.level === null ? "" : String(state.level);
  return (
    <div className="relative">
      <select
        aria-label={t("editor.blockType")}
        title={t("editor.blockType")}
        value={value}
        disabled={!state.canSetLevel}
        onChange={(e) => {
          const level = Number(e.target.value);
          if (level === 0) runCommand(turnIntoTextCommand.key);
          else runCommand(wrapInHeadingCommand.key, level);
        }}
        className={cn(
          "h-7 w-[6.75rem] appearance-none rounded bg-transparent pl-2 pr-6 text-xs text-muted-foreground",
          "focus:outline-none focus-visible:ring-1 focus-visible:ring-ring",
          HOVER,
          "disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-muted-foreground",
        )}
      >
        {/* Shown, never picked: the cursor is in a code block or across
            blocks of different types, and claiming "Text" would be a lie. */}
        <option value="" disabled hidden>
          {t("editor.blockMixed")}
        </option>
        <option value="0">{t("editor.paragraph")}</option>
        {LEVELS.map((level) => (
          <option key={level} value={String(level)}>
            {t("editor.heading", { level })}
          </option>
        ))}
      </select>
      <ChevronDown className="pointer-events-none absolute right-1.5 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground" />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Table menu
// ---------------------------------------------------------------------------

const VIEWPORT_MARGIN = 8;

interface MenuEntry {
  id: string;
  label: string;
  icon: React.ReactNode;
  /** Why the action is unavailable here; the entry stays visible and says so. */
  disabledReason?: string;
  checked?: boolean;
  destructive?: boolean;
  run: () => void;
}

export function TableMenu({ ns, store, runCommand, withView }: EditorBlockControlsProps) {
  const { t } = useTranslation(ns);
  const state = useSyncExternalStore(store.subscribe, store.get);
  const [open, setOpen] = useState(false);
  const [shift, setShift] = useState(0);
  const [maxHeight, setMaxHeight] = useState<number | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  // Leaving the table closes the menu: its actions would have nothing to act on.
  useEffect(() => {
    if (!state.inTable) setOpen(false);
  }, [state.inTable]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    // Opened with the pointer, the menu never takes focus — the trigger keeps
    // the editor's selection by refusing it — so Escape lands in the editor,
    // not in the panel. Listened for on the document, in the capture phase, so
    // it closes the menu wherever focus is and the editor does not also act
    // on it.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      const fromMenu = panelRef.current?.contains(document.activeElement);
      setOpen(false);
      if (fromMenu) triggerRef.current?.focus();
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey, true);
    };
  }, [open]);

  // Keep the panel on screen — at 393px the trigger sits near the right edge
  // once the toolbar wraps.
  useLayoutEffect(() => {
    if (!open || !panelRef.current) return;
    const rect = panelRef.current.getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    let dx = 0;
    if (rect.right > vw - VIEWPORT_MARGIN) dx = vw - VIEWPORT_MARGIN - rect.right;
    if (rect.left + dx < VIEWPORT_MARGIN) dx = VIEWPORT_MARGIN - rect.left;
    setShift(dx);
    // And keep its bottom on screen: on a phone every item is a 44px touch
    // target and the ten of them outgrow the space under a toolbar that sits
    // mid-page — the last one, "delete table", was cut off. The sticky
    // toolbar cannot be scrolled away from to reveal it, so the panel scrolls
    // itself instead.
    const room = window.innerHeight - rect.top - VIEWPORT_MARGIN;
    setMaxHeight(rect.height > room ? Math.max(160, room) : null);
    return () => {
      setShift(0);
      setMaxHeight(null);
    };
  }, [open]);

  const apply = (fn: (view: EditorView) => void) => {
    withView((view) => {
      fn(view);
      view.focus();
    });
    setOpen(false);
  };
  const command = (key: Parameters<typeof callCommand>[0], payload?: unknown) => {
    runCommand(key, payload);
    setOpen(false);
  };
  const align = (a: Alignment) =>
    apply((view) => setColumnAlignment(a)(view.state, view.dispatch));

  const rowsLeft = state.rows - state.selectedRows;
  const colsLeft = state.cols - state.selectedCols;
  const groups: MenuEntry[][] = [
    [
      {
        id: "row-above",
        label: t("editor.rowAbove"),
        icon: <BetweenHorizontalStart className="h-3.5 w-3.5" />,
        disabledReason: state.inHeader ? t("editor.rowAboveHeader") : undefined,
        run: () => command(addRowBeforeCommand.key),
      },
      {
        id: "row-below",
        label: t("editor.rowBelow"),
        icon: <BetweenHorizontalEnd className="h-3.5 w-3.5" />,
        run: () => command(addRowAfterCommand.key),
      },
      {
        id: "col-left",
        label: t("editor.colLeft"),
        icon: <BetweenVerticalStart className="h-3.5 w-3.5" />,
        run: () => command(addColBeforeCommand.key),
      },
      {
        id: "col-right",
        label: t("editor.colRight"),
        icon: <BetweenVerticalEnd className="h-3.5 w-3.5" />,
        run: () => command(addColAfterCommand.key),
      },
    ],
    (
      [
        ["left", t("editor.alignLeft"), <TextAlignStart key="l" className="h-3.5 w-3.5" />],
        ["center", t("editor.alignCenter"), <TextAlignCenter key="c" className="h-3.5 w-3.5" />],
        ["right", t("editor.alignRight"), <TextAlignEnd key="r" className="h-3.5 w-3.5" />],
      ] as const
    ).map(([a, label, icon]) => ({
      id: `align-${a}`,
      label,
      icon,
      checked: state.align === a,
      run: () => align(a),
    })),
    [
      {
        id: "delete-row",
        label: t("editor.deleteRow"),
        icon: <Rows3 className="h-3.5 w-3.5" />,
        destructive: true,
        disabledReason: rowsLeft < 2 ? t("editor.deleteRowLast") : undefined,
        run: () => apply((view) => deleteTableRow(view.state, view.dispatch)),
      },
      {
        id: "delete-col",
        label: t("editor.deleteColumn"),
        icon: <Columns3 className="h-3.5 w-3.5" />,
        destructive: true,
        disabledReason: colsLeft < 1 ? t("editor.deleteColumnLast") : undefined,
        run: () => apply((view) => deleteTableColumn(view.state, view.dispatch)),
      },
      {
        id: "delete-table",
        label: t("editor.deleteTable"),
        icon: <Trash2 className="h-3.5 w-3.5" />,
        destructive: true,
        run: () => apply((view) => deleteWholeTable(view.state, view.dispatch)),
      },
    ],
  ];

  const onMenuKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(
      panelRef.current?.querySelectorAll<HTMLButtonElement>("[role^=menuitem]") ?? [],
    );
    const at = items.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      items[(at + step + items.length) % items.length]?.focus();
    } else if (e.key === "Tab") {
      setOpen(false);
    }
  };

  const triggerTitle = state.inTable ? t("editor.tableMenu") : t("editor.tableMenuOutside");
  return (
    <div ref={rootRef} className="relative">
      <button
        ref={triggerRef}
        type="button"
        title={triggerTitle}
        aria-label={triggerTitle}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        disabled={!state.inTable}
        // Keep the editor's selection: the menu acts on the cell under it.
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" && state.inTable) {
            e.preventDefault();
            setOpen(true);
            requestAnimationFrame(() =>
              panelRef.current?.querySelector<HTMLButtonElement>("[role^=menuitem]")?.focus(),
            );
          }
        }}
        className={cn(
          "flex h-7 items-center gap-0.5 rounded px-1 text-muted-foreground",
          HOVER,
          PRESSED,
          open && "bg-secondary text-secondary-foreground",
          "disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-muted-foreground",
        )}
      >
        <TableProperties className="h-3.5 w-3.5" />
        <ChevronDown className="h-3 w-3" />
      </button>

      {open && (
        <div
          ref={panelRef}
          id={menuId}
          role="menu"
          aria-label={t("editor.tableMenu")}
          onKeyDown={onMenuKey}
          // Inline, not a class: this is a popup's own scroll, not a scrollport
          // around the prose (see the doc-editor test that forbids those).
          style={{
            ...(shift ? { transform: `translateX(${shift}px)` } : {}),
            ...(maxHeight !== null ? { maxHeight, overflowY: "auto" as const } : {}),
          }}
          className="absolute left-0 top-full z-50 mt-1 w-max min-w-[13rem] max-w-[calc(100vw-1rem)] rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-md"
        >
          {groups.map((group, gi) => (
            <div key={gi} role="group">
              {gi > 0 && <div role="separator" className="-mx-1 my-1 h-px bg-border" />}
              {group.map((entry) => {
                const disabled = !!entry.disabledReason;
                return (
                  <button
                    key={entry.id}
                    type="button"
                    role={entry.checked === undefined ? "menuitem" : "menuitemradio"}
                    aria-checked={entry.checked}
                    aria-disabled={disabled || undefined}
                    data-action={entry.id}
                    title={entry.disabledReason}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => {
                      if (!disabled) entry.run();
                    }}
                    className={cn(
                      "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs",
                      entry.destructive ? "text-destructive" : "text-foreground",
                      disabled
                        ? "cursor-not-allowed opacity-50"
                        : "hover:bg-secondary hover:text-secondary-foreground focus-visible:bg-secondary focus-visible:text-secondary-foreground focus:outline-none",
                      entry.checked && "font-medium",
                    )}
                  >
                    {entry.icon}
                    <span className="flex-1">{entry.label}</span>
                    {entry.checked && <span aria-hidden="true">✓</span>}
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
