import { useState } from "react";
import {
  ChevronDown,
  ChevronRight,
  FileText,
  MoreHorizontal,
  Plus,
} from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/cn";
import type { ProjectDocument } from "@/types";

export interface DocTreeNode {
  doc: ProjectDocument;
  children: DocTreeNode[];
}

/** position first, then title, so siblings never reorder between renders. */
function compareSiblings(a: ProjectDocument, b: ProjectDocument): number {
  if (a.position !== b.position) return a.position - b.position;
  const byTitle = a.title.localeCompare(b.title);
  return byTitle !== 0 ? byTitle : a.id.localeCompare(b.id);
}

/**
 * Turns the flat list into a forest.
 *
 * Every document ends up on screen exactly once, whatever the data says. A node
 * whose parent_id names a document that is not in the list (deleted, or on a
 * page we did not fetch) becomes a root rather than vanishing, and the visited
 * set means a parent cycle — which the API rejects, but which a stale client
 * could still be holding — costs one misplaced node instead of an infinite
 * recursion that takes the tab down.
 */
export function buildDocTree(documents: ProjectDocument[]): DocTreeNode[] {
  const byId = new Map(documents.map((d) => [d.id, d]));
  const childrenOf = new Map<string, ProjectDocument[]>();
  const roots: ProjectDocument[] = [];

  for (const doc of documents) {
    if (doc.parent_id && byId.has(doc.parent_id)) {
      const siblings = childrenOf.get(doc.parent_id) ?? [];
      siblings.push(doc);
      childrenOf.set(doc.parent_id, siblings);
    } else {
      roots.push(doc);
    }
  }

  const visited = new Set<string>();
  const build = (doc: ProjectDocument): DocTreeNode => {
    visited.add(doc.id);
    const kids = (childrenOf.get(doc.id) ?? [])
      .filter((child) => !visited.has(child.id))
      .sort(compareSiblings)
      .map(build);
    return { doc, children: kids };
  };

  const tree = roots.sort(compareSiblings).map(build);

  // Anything a cycle kept out of the walk is surfaced at the top level. The
  // visited check has to happen inside the loop, not in a filter beforehand:
  // building one stranded node marks the rest of its cycle visited, and a list
  // computed up front would then render those a second time.
  const stranded: DocTreeNode[] = [];
  for (const doc of [...documents].sort(compareSiblings)) {
    if (visited.has(doc.id)) continue;
    stranded.push(build(doc));
  }

  return [...tree, ...stranded];
}

/** A candidate parent, carrying the depth the picker indents it by. */
export interface DocMoveTarget {
  doc: ProjectDocument;
  depth: number;
}

/**
 * The documents `docId` may legally be moved under, in tree order.
 *
 * Excludes the document itself and everything beneath it. The server rejects
 * both anyway (`document_service.go` — "cannot be its own parent" and "cannot be
 * moved under one of its own descendants"), but a destination that cannot be
 * chosen is better than one that is offered and then refused: the user finds out
 * before the click rather than after it.
 *
 * The current parent is deliberately still listed. Moving a document to where it
 * already is is a no-op, not an error, and hiding it would make the list shift
 * under the user depending on which node they opened the picker from.
 */
export function moveTargets(
  documents: ProjectDocument[],
  docId: string,
): DocMoveTarget[] {
  const targets: DocMoveTarget[] = [];

  const walk = (nodes: DocTreeNode[], depth: number) => {
    for (const node of nodes) {
      // Skipping the subtree wholesale is what excludes the descendants: they
      // are only reachable through this node, so not recursing is enough.
      if (node.doc.id === docId) continue;
      targets.push({ doc: node.doc, depth });
      walk(node.children, depth + 1);
    }
  };
  walk(buildDocTree(documents), 0);

  return targets;
}

/**
 * The chain of documents from a root down to `docId`, inclusive — what the
 * breadcrumb shows.
 *
 * Walks the forest buildDocTree already produced instead of following parent_id
 * a second time, so the breadcrumb and the tree agree in exactly the cases
 * where the data is odd: a document whose parent is missing is a root in both,
 * and a parent cycle costs the same one misplaced node rather than hanging the
 * tab in one place and not the other.
 *
 * Returns [] when `docId` is not in `documents` at all — the list has not
 * arrived yet, or the document belongs to another project.
 */
export function buildDocPath(
  documents: ProjectDocument[],
  docId: string,
): ProjectDocument[] {
  const walk = (
    nodes: DocTreeNode[],
    trail: ProjectDocument[],
  ): ProjectDocument[] | null => {
    for (const node of nodes) {
      const next = [...trail, node.doc];
      if (node.doc.id === docId) return next;
      const found = walk(node.children, next);
      if (found) return found;
    }
    return null;
  };

  return walk(buildDocTree(documents), []) ?? [];
}

interface DocTreeProps {
  documents: ProjectDocument[];
  selectedId?: string | null;
  onSelect: (doc: ProjectDocument) => void;
  onCreateChild: (parent: ProjectDocument) => void;
  onRename: (doc: ProjectDocument) => void;
  onMove: (doc: ProjectDocument) => void;
  onDelete: (doc: ProjectDocument) => void;
}

export function DocTree({
  documents,
  selectedId,
  onSelect,
  onCreateChild,
  onRename,
  onMove,
  onDelete,
}: DocTreeProps) {
  // Collapsed rather than expanded: a document created under a node is visible
  // the moment it appears, without the tree having to be told about it.
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  const toggle = (id: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const nodes = buildDocTree(documents);

  return (
    <ul role="tree" aria-label="Documents" className="space-y-0.5">
      {nodes.map((node) => (
        <DocTreeItem
          key={node.doc.id}
          node={node}
          depth={0}
          collapsed={collapsed}
          onToggle={toggle}
          selectedId={selectedId}
          onSelect={onSelect}
          onCreateChild={onCreateChild}
          onRename={onRename}
          onMove={onMove}
          onDelete={onDelete}
        />
      ))}
    </ul>
  );
}

interface DocTreeItemProps extends Omit<DocTreeProps, "documents"> {
  node: DocTreeNode;
  depth: number;
  collapsed: Set<string>;
  onToggle: (id: string) => void;
}

function DocTreeItem({
  node,
  depth,
  collapsed,
  onToggle,
  selectedId,
  onSelect,
  onCreateChild,
  onRename,
  onMove,
  onDelete,
}: DocTreeItemProps) {
  const { doc, children } = node;
  const hasChildren = children.length > 0;
  const isOpen = hasChildren && !collapsed.has(doc.id);
  const isSelected = selectedId === doc.id;

  return (
    <li role="none">
      <div
        role="treeitem"
        aria-selected={isSelected}
        aria-expanded={hasChildren ? isOpen : undefined}
        className={cn(
          "group relative flex items-center gap-0.5 rounded-md pr-1 text-sm",
          // Hover is a neutral tint. It used to be --accent, which is a
          // saturated teal, not a tint: every row the pointer crossed turned
          // into a solid brand-coloured block with near-black text on it.
          "hover:bg-muted",
          isSelected && [
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
        {isSelected && (
          <span
            aria-hidden="true"
            data-selected-bar=""
            className="absolute inset-y-1 left-0 w-[3px] rounded-full bg-primary"
          />
        )}

        {hasChildren ? (
          <button
            type="button"
            aria-label={isOpen ? `Collapse ${doc.title}` : `Expand ${doc.title}`}
            onClick={() => onToggle(doc.id)}
            className="flex h-5 w-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
          >
            {isOpen ? (
              <ChevronDown className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" />
            )}
          </button>
        ) : (
          <span className="h-5 w-5 shrink-0" />
        )}

        <button
          type="button"
          onClick={() => onSelect(doc)}
          className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
        >
          <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate">{doc.title}</span>
        </button>

        {/* Hover actions. focus-within keeps them reachable from the keyboard,
            where there is no hover to trigger. */}
        <div className="flex shrink-0 items-center opacity-0 focus-within:opacity-100 group-hover:opacity-100">
          <button
            type="button"
            title="New page inside"
            aria-label={`New page inside ${doc.title}`}
            onClick={() => onCreateChild(doc)}
            className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
          >
            <Plus className="h-3.5 w-3.5" />
          </button>
          <DropdownMenu>
            <DropdownMenuTrigger
              title="Page actions"
              aria-label={`Actions for ${doc.title}`}
              className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
            >
              <MoreHorizontal className="h-3.5 w-3.5" />
            </DropdownMenuTrigger>
            <DropdownMenuContent className="min-w-[9rem]">
              <DropdownMenuItem onClick={() => onRename(doc)}>
                Rename
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => onMove(doc)}>
                Move to...
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() => onDelete(doc)}
                className="text-destructive"
              >
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {isOpen && (
        <ul role="group" className="space-y-0.5">
          {children.map((child) => (
            <DocTreeItem
              key={child.doc.id}
              node={child}
              depth={depth + 1}
              collapsed={collapsed}
              onToggle={onToggle}
              selectedId={selectedId}
              onSelect={onSelect}
              onCreateChild={onCreateChild}
              onRename={onRename}
              onMove={onMove}
              onDelete={onDelete}
            />
          ))}
        </ul>
      )}
    </li>
  );
}
