import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { GripVertical, Library, MoreHorizontal, Plus, Users } from "lucide-react";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";
import type * as DndPrimitives from "@/core/primitives/dnd";
import { navIcon } from "@/core/icons/nav-icons";
import { EntityIcon } from "@/components/ui/entity-icon";
import { RAIL_BADGE, RAIL_LINK, RAIL_LINK_CURRENT, ROW_BADGE, ROW_LINK, ROW_LINK_CURRENT } from "./row-classes";
import type { NavItem, NavSidebarActions } from "./types";

type Group = "top" | "bottom";

export interface DashboardLists {
  top: NavItem[];
  bottom: NavItem[];
}

export function dashboardLists(items: NavItem[]): DashboardLists {
  return {
    top: items.filter((i) => i.group === "dashboards-top" && i.dashboard),
    bottom: items.filter((i) => i.group === "dashboards-bottom" && i.dashboard),
  };
}

const idsOf = (l: DashboardLists) => ({ top: l.top.map((i) => i.dashboard!.id), bottom: l.bottom.map((i) => i.dashboard!.id) });

/** The order after moving `id` into `group` at `index` (among that group without `id`). */
export function orderAfterMove(lists: DashboardLists, id: string, group: Group, index: number) {
  const cur = idsOf(lists);
  const top = cur.top.filter((x) => x !== id);
  const bottom = cur.bottom.filter((x) => x !== id);
  const target = group === "top" ? top : bottom;
  target.splice(Math.max(0, Math.min(index, target.length)), 0, id);
  return { top, bottom };
}

// The drag layer (dnd-kit and the DnD primitive) is not in the shell's first chunk: it loads once the menu has
// dashboards to drag, and until then the rows are the same rows without drag. Context = the loaded module.
type Dnd = typeof DndPrimitives;
const DndModule = createContext<Dnd | null>(null);
let dndModule: Promise<Dnd> | null = null;
const loadDnd = () => (dndModule ??= import("@/core/primitives/dnd"));

/** One DnD container for both groups: dragging with the mouse or the keyboard ends in one `reorderDashboards` call. */
export function DashboardDnd({
  lists,
  actions,
  enabled,
  children,
}: {
  lists: DashboardLists;
  actions: NavSidebarActions;
  /** Dashboards are on screen; without them no drag container (and no second live region) is mounted. */
  enabled: boolean;
  children: React.ReactNode;
}) {
  const on = enabled && !!actions.reorderDashboards;
  const [dnd, setDnd] = useState<Dnd | null>(null);
  useEffect(() => {
    if (!on || dnd) return;
    let alive = true;
    loadDnd().then(
      (m) => alive && setDnd(m),
      () => {}, // no drag layer: the move items of the row menu still reorder
    );
    return () => {
      alive = false;
    };
  }, [on, dnd]);
  if (!on || !dnd) return <>{children}</>;
  return (
    <DndModule.Provider value={dnd}>
      <dnd.DndProvider
        onMove={async (m) => {
          if (m.item.kind !== "dashboard" || m.to.kind !== "dashboard-group") return;
          await actions.reorderDashboards?.(orderAfterMove(lists, m.item.id, m.to.id, m.index));
        }}
      >
        {children}
      </dnd.DndProvider>
    </DndModule.Provider>
  );
}

// -- item menu ---------------------------------------------------------------------------------

interface MenuEntry {
  key: string;
  label: string;
  run: () => void;
  disabled?: boolean;
  title?: string;
  danger?: boolean;
  separatorBefore?: boolean;
}

function useMenuEntries(item: NavItem, group: Group, lists: DashboardLists, actions: NavSidebarActions): MenuEntry[] {
  const { t } = useTranslation("shell");
  const d = item.dashboard!;
  const list = group === "top" ? lists.top : lists.bottom;
  const at = list.findIndex((i) => i.id === item.id);
  const reorder = actions.reorderDashboards;
  const move = (g: Group, index: number) => () => void reorder?.(orderAfterMove(lists, d.id, g, index));
  const e: MenuEntry[] = [];
  if (d.canEdit && actions.renameDashboard) e.push({ key: "rename", label: t("dashboards.menu.rename"), run: () => actions.renameDashboard?.(d.id) });
  if (d.canEdit && actions.changeDashboardIcon) e.push({ key: "icon", label: t("dashboards.menu.changeIcon"), run: () => actions.changeDashboardIcon?.(d.id) });
  if (actions.duplicateDashboard) e.push({ key: "duplicate", label: t("dashboards.menu.duplicate"), run: () => actions.duplicateDashboard?.(d.id) });
  if (reorder) {
    e.push({ key: "up", label: t("dashboards.menu.moveUp"), run: move(group, at - 1), disabled: at <= 0, separatorBefore: true });
    e.push({ key: "down", label: t("dashboards.menu.moveDown"), run: move(group, at + 1), disabled: at === -1 || at >= list.length - 1 });
    e.push(
      group === "top"
        ? { key: "to-bottom", label: t("dashboards.menu.moveToBottom"), run: move("bottom", lists.bottom.length) }
        : { key: "to-top", label: t("dashboards.menu.moveToTop"), run: move("top", lists.top.length) },
    );
  }
  if (d.canManage && actions.shareDashboard) {
    e.push({
      key: "share",
      label: d.shared ? t("dashboards.menu.sharing") : t("dashboards.menu.share"),
      run: () => actions.shareDashboard?.(d.id),
      separatorBefore: true,
    });
  }
  if (d.shared && actions.hideDashboard) e.push({ key: "hide", label: t("dashboards.menu.hide"), run: () => actions.hideDashboard?.(d.id) });
  if (d.isDefault && d.canEdit && actions.resetDashboard) e.push({ key: "reset", label: t("dashboards.menu.reset"), run: () => actions.resetDashboard?.(d.id) });
  if (d.canManage && actions.deleteDashboard) {
    e.push({
      key: "delete",
      label: t("dashboards.menu.delete"),
      run: () => actions.deleteDashboard?.(d.id),
      disabled: !d.canDelete,
      title: d.canDelete ? undefined : t("dashboards.menu.deleteLast"),
      danger: true,
      separatorBefore: true,
    });
  }
  return e;
}

function ItemMenu({
  entries,
  anchor,
  onClose,
}: {
  entries: MenuEntry[];
  anchor: { x: number; y: number };
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState(anchor);
  // Fixed to the viewport: the nav list scrolls and would clip an absolutely positioned panel. Portaled to
  // document.body: inside the sidebar `fixed` + z-index stay in the sidebar's stacking context (and a dragged
  // row's transform re-anchors `fixed`), so page content such as kanban cards was drawn over the menu.
  useLayoutEffect(() => {
    const r = ref.current?.getBoundingClientRect();
    if (!r) return;
    const vw = document.documentElement.clientWidth;
    const vh = document.documentElement.clientHeight;
    setPos({ x: Math.max(8, Math.min(anchor.x, vw - r.width - 8)), y: Math.max(8, Math.min(anchor.y, vh - r.height - 8)) });
  }, [anchor]);
  useEffect(() => {
    const down = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("mousedown", down);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("keydown", key);
    };
  }, [onClose]);
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')?.focus();
  }, []);
  return createPortal(
    <div
      ref={ref}
      role="menu"
      data-testid="dashboard-menu"
      style={{ position: "fixed", left: pos.x, top: pos.y }}
      // React events still bubble through the portal to the row: a right click inside the menu must not re-open it.
      onContextMenu={(ev) => {
        ev.preventDefault();
        ev.stopPropagation();
      }}
      className="z-[9999] min-w-[12rem] max-w-[calc(100vw-1rem)] rounded-lg border border-border bg-popover p-1 text-sm text-popover-foreground shadow-lg"
      onKeyDown={(ev) => {
        if (ev.key !== "ArrowDown" && ev.key !== "ArrowUp") return;
        ev.preventDefault();
        const els = [...ref.current!.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')];
        const i = els.indexOf(document.activeElement as HTMLElement);
        els[(i + (ev.key === "ArrowDown" ? 1 : -1) + els.length) % els.length]?.focus();
      }}
    >
      {entries.map((m) => (
        <div key={m.key}>
          {m.separatorBefore && <div role="separator" className="my-1 border-t border-border" />}
          <button
            type="button"
            role="menuitem"
            aria-disabled={m.disabled || undefined}
            title={m.title}
            className={cn(
              "flex w-full items-center rounded-md px-2 py-1.5 text-left hover:bg-accent hover:text-accent-foreground focus:bg-accent focus:text-accent-foreground focus:outline-none",
              m.danger && "text-destructive",
              m.disabled && "cursor-not-allowed opacity-50 hover:bg-transparent hover:text-popover-foreground",
            )}
            onClick={() => {
              if (m.disabled) return;
              onClose();
              m.run();
            }}
          >
            {m.label}
          </button>
        </div>
      ))}
    </div>,
    document.body,
  );
}

// -- rows --------------------------------------------------------------------------------------

function DashboardIcon({ item, className }: { item: NavItem; className: string }) {
  // "" = the default icon; an emoji, `icon:<name>` (theme, then core) or a legacy text icon otherwise.
  return (
    <span aria-hidden="true" className="flex shrink-0 items-center justify-center text-sm leading-none">
      <EntityIcon value={item.dashboard!.emoji} fallback={navIcon(item.icon)} className={className} />
    </span>
  );
}

function DashboardRow({ item, group, lists, actions }: { item: NavItem; group: Group; lists: DashboardLists; actions: NavSidebarActions }) {
  const { t } = useTranslation("shell");
  const d = item.dashboard!;
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const entries = useMenuEntries(item, group, lists, actions);
  const count = item.badge?.count ?? 0;
  const btn = useRef<HTMLButtonElement>(null);
  const close = () => setMenu(null);
  const dnd = useContext(DndModule);
  const rowProps = {
    "data-testid": `dashboard-row-${d.id}`,
    "data-dashboard-group": group,
    onContextMenu: (e: React.MouseEvent) => {
      if (entries.length === 0) return;
      e.preventDefault();
      setMenu({ x: e.clientX, y: e.clientY });
    },
  };
  const grip = (
    <span
      title={t("dashboards.drag")}
      aria-label={t("dashboards.dragItem", { name: item.label })}
      className="flex h-6 w-5 cursor-grab touch-none items-center justify-center rounded text-muted-foreground hover:bg-sidebar-accent active:cursor-grabbing"
    >
      <GripVertical aria-hidden="true" className="h-3.5 w-3.5" />
    </span>
  );
  const body = (
      <div className="group relative">
      <Link
        to={item.href}
        aria-current={item.active ? "page" : undefined}
        data-slot-origin={item.origin}
        className={cn(ROW_LINK, ROW_LINK_CURRENT, "pr-14")}
      >
        <DashboardIcon item={item} className="h-4 w-4" />
        <span className="truncate">{item.label}</span>
        {d.shared && (
          <span role="img" aria-label={t("dashboards.shared")} title={t("dashboards.shared")} className="shrink-0 text-xs leading-none">
            <Users aria-hidden="true" className="h-3 w-3" />
          </span>
        )}
        {item.badge && count > 0 && (
          <span aria-label={t("sidebar.unread", { count })} className={cn(ROW_BADGE[item.badge.tone], "group-hover:hidden group-focus-within:hidden")}>
            {count > 99 ? "99+" : count}
          </span>
        )}
      </Link>
      <div className="absolute right-1 top-1/2 flex -translate-y-1/2 items-center opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 has-[[aria-expanded=true]]:opacity-100 [@media(hover:none)]:opacity-100">
        {actions.reorderDashboards && (dnd ? <dnd.Draggable.Handle>{grip}</dnd.Draggable.Handle> : grip)}
        {entries.length > 0 && (
          <button
            ref={btn}
            type="button"
            aria-haspopup="menu"
            aria-expanded={menu != null}
            aria-label={t("dashboards.menu.open", { name: item.label })}
            className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-sidebar-accent"
            onClick={() => {
              const r = btn.current?.getBoundingClientRect();
              setMenu(menu ? null : { x: r ? r.left : 0, y: r ? r.bottom + 4 : 0 });
            }}
          >
            <MoreHorizontal aria-hidden="true" className="h-4 w-4" />
          </button>
        )}
      </div>
      {menu && <ItemMenu entries={entries} anchor={menu} onClose={close} />}
      </div>
  );
  return dnd ? (
    <dnd.Draggable item={{ kind: "dashboard", id: d.id }} label={item.label} handle="custom" {...rowProps}>
      {body}
    </dnd.Draggable>
  ) : (
    <div {...rowProps}>{body}</div>
  );
}

export function DashboardGroupRows({
  group,
  title,
  lists,
  actions,
  state,
  allHref,
  allActive,
}: {
  group: Group;
  title: string;
  lists: DashboardLists;
  actions: NavSidebarActions;
  state?: "loading" | "ready" | "error";
  allHref?: string;
  allActive?: boolean;
}) {
  const { t } = useTranslation("shell");
  const items = group === "top" ? lists.top : lists.bottom;
  const zone = { kind: "dashboard-group" as const, id: group };
  const dnd = useContext(DndModule);
  const rows = (
    <>
      {items.map((item) => (
        <DashboardRow key={item.id} item={item} group={group} lists={lists} actions={actions} />
      ))}
      {items.length === 0 && state !== "loading" && (
        <p className="px-2 py-1 text-xs text-muted-foreground">{t("dashboards.groupEmpty")}</p>
      )}
    </>
  );
  const listClass = (isOver: boolean) => cn("space-y-0.5 rounded-lg", items.length === 0 && "min-h-7", isOver && "bg-sidebar-accent/50");
  return (
    <div data-testid={`dashboards-${group}`} className={group === "top" ? "mb-3" : "mt-3"}>
      <div className="flex items-center justify-between px-2 py-1">
        <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{title}</span>
        {actions.createDashboard && (
          <Button
            variant="ghost"
            size="icon"
            className="h-5 w-5"
            aria-label={t(group === "top" ? "dashboards.createTop" : "dashboards.createBottom")}
            onClick={() => actions.createDashboard?.(group)}
          >
            <Plus className="h-3 w-3" />
          </Button>
        )}
      </div>
      {dnd ? (
        <dnd.DropZone zone={zone} orientation="vertical" itemIds={items.map((i) => i.dashboard!.id)} className={({ isOver }) => listClass(isOver)}>
          {rows}
        </dnd.DropZone>
      ) : (
        <div className={listClass(false)}>{rows}</div>
      )}
      {allHref && (
        <Link
          to={allHref}
          aria-current={allActive ? "page" : undefined}
          className={cn(ROW_LINK, ROW_LINK_CURRENT, "mt-0.5 text-muted-foreground")}
        >
          <Library aria-hidden="true" className="h-4 w-4" />
          {t("dashboards.all")}
        </Link>
      )}
    </div>
  );
}

// -- collapsed rail ----------------------------------------------------------------------------

export function DashboardRailTile({ item }: { item: NavItem }) {
  const { t } = useTranslation("shell");
  const count = item.badge?.count ?? 0;
  // The rail has no room for the 👥 mark: the tile's name says it instead.
  const tileLabel = item.dashboard!.shared ? `${item.label} · ${t("dashboards.shared")}` : item.label;
  return (
    <Link
      to={item.href}
      aria-label={tileLabel}
      title={tileLabel}
      aria-current={item.active ? "page" : undefined}
      data-testid={`dashboard-tile-${item.dashboard!.id}`}
      className={cn("relative", RAIL_LINK, RAIL_LINK_CURRENT)}
    >
      <DashboardIcon item={item} className="h-4 w-4" />
      {item.badge && count > 0 && (
        <span aria-label={t("sidebar.unread", { count })} className={RAIL_BADGE[item.badge.tone]}>
          {count > 9 ? "9+" : count}
        </span>
      )}
    </Link>
  );
}

export function DashboardRailAll({ href, active }: { href: string; active: boolean }) {
  const { t } = useTranslation("shell");
  return (
    <Link to={href} aria-label={t("dashboards.all")} title={t("dashboards.all")} aria-current={active ? "page" : undefined} className={cn(RAIL_LINK, RAIL_LINK_CURRENT)}>
      <Library aria-hidden="true" className="h-4 w-4" />
    </Link>
  );
}
