// doc.tree, mesh-default: the page tree. Takes only what the contract hands it (a flat list of
// the visible nodes and the actions); expansion, server sync and navigation live in the container.
import { useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronRight, FileText, MoreHorizontal, Plus } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/cn";
import type { DocNodeData, DocTreeActions, DocTreeView } from "@/core/slots/types";

export const DocTreeMeshDefault: DocTreeView = ({ data, actions, themeSettings }) => {
  const { t } = useTranslation("docs");
  const { nodes } = data;

  if (nodes.state === "loading") {
    return (
      <div className="space-y-2 p-1" data-testid="doc-tree-loading">
        <Skeleton className="h-5 w-full" />
        <Skeleton className="h-5 w-4/5" />
        <Skeleton className="h-5 w-3/5" />
      </div>
    );
  }
  if (nodes.state === "error" && !nodes.value) {
    return (
      <div className="p-2 text-xs text-destructive" role="alert">
        <p>{nodes.error.message}</p>
        <button type="button" onClick={nodes.retry} className="mt-1 underline">
          {t("save.retry")}
        </button>
      </div>
    );
  }
  const list = nodes.value ?? [];
  return (
    <TreeList
      nodes={list}
      actions={actions}
      label={t("tree.heading")}
      showIcons={themeSettings.show_icons !== false}
    />
  );
};

function TreeList({
  nodes,
  actions,
  label,
  showIcons,
}: {
  nodes: DocNodeData[];
  actions: DocTreeActions;
  label: string;
  showIcons: boolean;
}) {
  const treeRef = useRef<HTMLUListElement>(null);
  // Roving tabindex: one row is the tab stop, the arrows move it.
  const [activeId, setActiveId] = useState<string | null>(null);
  const tabStop = nodes.some((n) => n.id === activeId)
    ? activeId
    : (nodes.find((n) => n.selected) ?? nodes[0])?.id;

  const focusRow = (id: string) => {
    setActiveId(id);
    treeRef.current?.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(id)}"]`)?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLElement>, index: number) => {
    // Only the row itself: Enter / Space on the buttons inside keep their own meaning.
    if (e.target !== e.currentTarget) return;
    const node = nodes[index]!;
    switch (e.key) {
      case "ArrowDown":
        if (nodes[index + 1]) focusRow(nodes[index + 1]!.id);
        break;
      case "ArrowUp":
        if (nodes[index - 1]) focusRow(nodes[index - 1]!.id);
        break;
      case "ArrowRight":
        if (node.hasChildren && !node.expanded) actions.toggleExpanded(node.id);
        else if (node.hasChildren && nodes[index + 1]) focusRow(nodes[index + 1]!.id);
        break;
      case "ArrowLeft":
        if (node.hasChildren && node.expanded) actions.toggleExpanded(node.id);
        else {
          for (let i = index - 1; i >= 0; i -= 1) {
            if (nodes[i]!.depth < node.depth) {
              focusRow(nodes[i]!.id);
              break;
            }
          }
        }
        break;
      case "Home":
        if (nodes[0]) focusRow(nodes[0].id);
        break;
      case "End":
        if (nodes.length) focusRow(nodes[nodes.length - 1]!.id);
        break;
      case "Enter":
        actions.select(node.id);
        break;
      default:
        return;
    }
    e.preventDefault();
  };

  return (
    <ul role="tree" aria-label={label} ref={treeRef}>
      {nodes.map((node, index) => (
        <TreeRow
          key={node.id}
          node={node}
          // The first child sits directly under its parent row; every other row is 2px below its predecessor.
          spaced={index > 0 && node.depth <= nodes[index - 1]!.depth}
          tabStop={tabStop === node.id}
          showIcons={showIcons}
          actions={actions}
          onFocusRow={() => setActiveId(node.id)}
          onKeyDown={(e) => onKeyDown(e, index)}
        />
      ))}
    </ul>
  );
}

function TreeRow({
  node,
  spaced,
  tabStop,
  showIcons,
  actions,
  onFocusRow,
  onKeyDown,
}: {
  node: DocNodeData;
  spaced: boolean;
  tabStop: boolean;
  showIcons: boolean;
  actions: DocTreeActions;
  onFocusRow: () => void;
  onKeyDown: (e: KeyboardEvent<HTMLElement>) => void;
}) {
  const { t } = useTranslation("docs");
  const { id, title, depth, hasChildren, expanded, selected } = node;

  // A plain left click is in-app; a modified or middle click is the browser's (a real href is there for it).
  const onLink = (e: MouseEvent<HTMLAnchorElement>) => {
    if (e.defaultPrevented || e.button !== 0) return;
    e.preventDefault();
    actions.select(id, e.metaKey || e.ctrlKey || e.shiftKey || e.altKey ? { newTab: true } : undefined);
  };

  return (
    <li role="none" className={spaced ? "mt-0.5" : undefined}>
      <div
        role="treeitem"
        data-node-id={id}
        aria-selected={selected}
        aria-current={selected ? "page" : undefined}
        aria-level={depth + 1}
        aria-expanded={hasChildren ? expanded : undefined}
        tabIndex={tabStop ? 0 : -1}
        onFocus={(e) => e.target === e.currentTarget && onFocusRow()}
        onKeyDown={onKeyDown}
        className={cn(
          "group relative flex items-center gap-0.5 rounded-md pr-1 text-sm",
          // Hover is a neutral tint. It used to be --accent, which is a
          // saturated teal, not a tint: every row the pointer crossed turned
          // into a solid brand-coloured block with near-black text on it.
          "hover:bg-muted",
          selected && [
            // --secondary is the palette's light teal-green (teal-100 in light
            // mode, the raised dark-teal surface in dark) and it ships with a
            // foreground that reads on it: 16.3:1 light, 11.4:1 dark. Both are
            // computed, not eyeballed — see the doc-tree tests.
            "bg-secondary font-medium text-secondary-foreground",
          ],
        )}
        style={{ paddingLeft: `${depth * 12}px` }}
      >
        {/* --secondary and --muted are close in lightness by design, so the
            selected row does not rely on its fill to be told apart from the one
            under the pointer: it also carries a brand-coloured bar at the
            column edge (5.8:1 against the fill in light, 6.6:1 in dark). A real
            element rather than a border, because the row's left padding encodes
            tree depth and a border would shift every nested title by 3px. */}
        {selected && (
          <span
            aria-hidden="true"
            data-selected-bar=""
            className="absolute inset-y-1 left-0 w-[3px] rounded-full bg-primary"
          />
        )}

        {hasChildren ? (
          <button
            type="button"
            aria-label={expanded ? t("tree.collapse", { title }) : t("tree.expand", { title })}
            onClick={() => actions.toggleExpanded(id)}
            className="flex h-5 w-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
          >
            {expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
          </button>
        ) : (
          <span className="h-5 w-5 shrink-0" />
        )}

        <a
          href={node.href}
          onClick={onLink}
          className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
        >
          {showIcons && <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />}
          <span className="break-words md:truncate">{title}</span>
        </a>

        {(actions.createChild || actions.rename || actions.move || actions.remove) && (
          /* Touch actions stay visible on mobile. Desktop reveals them on
             hover or keyboard focus. */
          <div className="flex shrink-0 items-center opacity-100 md:opacity-0 focus-within:opacity-100 group-hover:opacity-100">
            {actions.createChild && (
              <button
                type="button"
                title={t("tree.newInside")}
                aria-label={t("tree.newInsideOf", { title })}
                onClick={() => actions.createChild?.(id)}
                className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
              >
                <Plus className="h-3.5 w-3.5" />
              </button>
            )}
            {(actions.rename || actions.move || actions.remove) && (
              <DropdownMenu>
                <DropdownMenuTrigger
                  title={t("tree.pageActions")}
                  aria-label={t("tree.actionsFor", { title })}
                  className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
                >
                  <MoreHorizontal className="h-3.5 w-3.5" />
                </DropdownMenuTrigger>
                <DropdownMenuContent className="min-w-[9rem]">
                  {actions.rename && (
                    <DropdownMenuItem onClick={() => actions.rename?.(id)}>{t("actions.rename")}</DropdownMenuItem>
                  )}
                  {actions.move && (
                    <DropdownMenuItem onClick={() => actions.move?.(id)}>{t("tree.moveTo")}</DropdownMenuItem>
                  )}
                  {actions.remove && (
                    <DropdownMenuItem onClick={() => actions.remove?.(id)} className="text-destructive">
                      {t("actions.delete")}
                    </DropdownMenuItem>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        )}
      </div>
    </li>
  );
}
