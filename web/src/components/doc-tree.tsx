import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
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
import { loadServerExpanded, saveServerExpanded } from "@/lib/docs-tree-sync";
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
  /** localStorage key for the expanded set (per user and project). Without it
   *  the expansion is not remembered. */
  storageKey?: string | null;
  /** Project the tree is shown for. The document store keeps the previous
   *  project's list until the next fetch lands, so nothing is read against,
   *  expanded from or persisted with `documents` until they belong to it. */
  projectId?: string | null;
  selectedId?: string | null;
  onSelect: (doc: ProjectDocument) => void;
  onCreateChild: (parent: ProjectDocument) => void;
  onRename: (doc: ProjectDocument) => void;
  onMove: (doc: ProjectDocument) => void;
  onDelete: (doc: ProjectDocument) => void;
}

function readExpanded(key: string | null | undefined): Set<string> {
  if (!key) return new Set();
  try {
    const raw = window.localStorage.getItem(key);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((v): v is string => typeof v === "string"));
  } catch {
    return new Set();
  }
}

function writeExpanded(key: string | null | undefined, expanded: Set<string>) {
  if (!key) return;
  try {
    window.localStorage.setItem(key, JSON.stringify([...expanded]));
  } catch {
    // storage unavailable: the tree still works, just forgets.
  }
}

export function DocTree({
  documents,
  storageKey,
  projectId,
  selectedId,
  onSelect,
  onCreateChild,
  onRename,
  onMove,
  onDelete,
}: DocTreeProps) {
  // Expanded, not collapsed: everything is closed by default except the branch
  // down to the open document. Anything that makes a node's content appear (a
  // new child, a move) expands that node explicitly, see the effects below.
  const { t } = useTranslation("docs");
  const [expanded, setExpanded] = useState<Set<string>>(() =>
    readExpanded(storageKey),
  );

  // The expansion belongs to a storage key. Should the key change without a
  // remount (the page does remount, this keeps the component safe on its own),
  // the old key's set must neither leak into the new key nor survive in memory.
  const revealed = useRef<string | null>(null);
  const parents = useRef<Map<string, string | null>>(new Map());
  const [stateKey, setStateKey] = useState(storageKey);
  if (stateKey !== storageKey) {
    setStateKey(storageKey);
    setExpanded(readExpanded(storageKey));
    revealed.current = null;
    parents.current = new Map();
  }

  const synced =
    !projectId || (documents.length > 0 && documents.every((d) => d.project_id === projectId));

  const add = (ids: string[]) => {
    setExpanded((prev) => {
      if (ids.every((id) => prev.has(id))) return prev;
      const next = new Set(prev);
      ids.forEach((id) => next.add(id));
      return next;
    });
  };

  // Server sync (user_ui_state). localStorage above is the instant cache; the
  // server copy is what makes the tree the same on another device.
  //  - serverReady: the GET came back (a stored state or a clean "nothing yet").
  //    Until then nothing is pushed, so a device that cannot reach the server
  //    never overwrites the other device's state with its own cache.
  //  - userTouched: the person toggled something. That is a newer intent than
  //    whatever the GET returns, so a late response must not undo it (last
  //    write wins), and it also licenses pushing after a failed GET.
  //  - lastSynced: what the server is known to hold, to skip no-op writes. Only
  //    a successful PUT moves it, so a failed one is retried by the next change.
  const serverReady = useRef(false);
  const userTouched = useRef(false);
  const applyingServer = useRef(false);
  const lastSynced = useRef<string | null>(null);
  const pending = useRef<{ projectId: string; ids: string[] } | null>(null);
  const latest = useRef({ documents, selectedId });
  latest.current = { documents, selectedId };

  const toggle = (id: string) => {
    userTouched.current = true;
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  // Reveal the branch of the open document once per selection (documents may
  // arrive after selectedId on a direct /d/<id> visit).
  useEffect(() => {
    if (!selectedId) {
      revealed.current = null;
      return;
    }
    if (!synced || revealed.current === selectedId) return;
    const path = buildDocPath(documents, selectedId);
    if (path.length === 0) return;
    revealed.current = selectedId;
    add(path.slice(0, -1).map((d) => d.id));
  }, [selectedId, documents, synced]);

  // A document that appeared under, or moved to, a node expands that node.
  useEffect(() => {
    if (!synced) return;
    const prev = parents.current;
    const next = new Map(documents.map((d) => [d.id, d.parent_id] as const));
    if (prev.size > 0) {
      // The whole chain, so the new node is actually visible, not just its parent.
      const toOpen = documents
        .filter((d) => d.parent_id && prev.get(d.id) !== d.parent_id)
        .flatMap((d) => buildDocPath(documents, d.id).slice(0, -1).map((a) => a.id));
      if (toOpen.length > 0) add(toOpen);
    }
    parents.current = next;
  }, [documents, synced]);

  // Persist without ids of deleted documents, and only once the list is the
  // project's own — against a stale one every valid id would look deleted.
  // The last document of a project being deleted leaves an empty list: that is
  // authoritative only because this mount has already seen the project's own list.
  useEffect(() => {
    const emptied = documents.length === 0 && parents.current.size > 0;
    if (!synced && !emptied) return;
    const known = new Set(documents.map((d) => d.id));
    writeExpanded(storageKey, new Set([...expanded].filter((id) => known.has(id))));
  }, [storageKey, expanded, documents, synced]);

  useEffect(() => {
    if (!projectId || !storageKey) return;
    let cancelled = false;
    serverReady.current = false;
    userTouched.current = false;
    lastSynced.current = null;
    void loadServerExpanded(projectId).then((res) => {
      if (cancelled || res.kind === "error") return;
      serverReady.current = true;
      if (res.kind !== "ok") return;
      lastSynced.current = [...res.expanded].sort().join(",");
      if (userTouched.current) return;
      // Server state plus the branch of the open document, which is always shown.
      const { documents: docs, selectedId: sel } = latest.current;
      const path = sel ? buildDocPath(docs, sel) : [];
      revealed.current = path.length > 0 ? sel! : null;
      applyingServer.current = true;
      setExpanded(
        new Set([...res.expanded, ...path.slice(0, -1).map((d) => d.id)]),
      );
    });
    return () => {
      cancelled = true;
    };
  }, [projectId, storageKey]);

  // Debounced push, ~1 s after the last change.
  useEffect(() => {
    if (applyingServer.current) {
      applyingServer.current = false;
      return;
    }
    if (!projectId || !storageKey || !synced) return;
    if (!serverReady.current && !userTouched.current) return;
    const known = new Set(documents.map((d) => d.id));
    const ids = [...expanded].filter((id) => known.has(id));
    if (ids.slice().sort().join(",") === lastSynced.current) {
      pending.current = null;
      return;
    }
    pending.current = { projectId, ids };
    const timer = setTimeout(() => {
      const job = pending.current;
      pending.current = null;
      if (!job) return;
      void saveServerExpanded(job.projectId, job.ids).then((ok) => {
        if (ok) lastSynced.current = job.ids.slice().sort().join(",");
      });
    }, 1000);
    return () => clearTimeout(timer);
  }, [projectId, storageKey, expanded, documents, synced]);

  // Leaving the page inside the debounce window must not drop the last change.
  useEffect(
    () => () => {
      const job = pending.current;
      pending.current = null;
      if (job) void saveServerExpanded(job.projectId, job.ids);
    },
    [],
  );

  // Another project's list is not drawn at all, so it cannot be shown against this
  // project's saved expansion.
  const nodes = synced ? buildDocTree(documents) : [];

  return (
    <ul role="tree" aria-label={t("tree.heading")} className="space-y-0.5">
      {nodes.map((node) => (
        <DocTreeItem
          key={node.doc.id}
          node={node}
          depth={0}
          expanded={expanded}
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

interface DocTreeItemProps extends Omit<DocTreeProps, "documents" | "storageKey"> {
  node: DocTreeNode;
  depth: number;
  expanded: Set<string>;
  onToggle: (id: string) => void;
}

function DocTreeItem({
  node,
  depth,
  expanded,
  onToggle,
  selectedId,
  onSelect,
  onCreateChild,
  onRename,
  onMove,
  onDelete,
}: DocTreeItemProps) {
  const { t } = useTranslation("docs");
  const { doc, children } = node;
  const hasChildren = children.length > 0;
  const isOpen = hasChildren && expanded.has(doc.id);
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
            aria-label={isOpen ? t("tree.collapse", { title: doc.title }) : t("tree.expand", { title: doc.title })}
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
          <span className="break-words md:truncate">{doc.title}</span>
        </button>

        {/* Touch actions stay visible on mobile. Desktop reveals them on
            hover or keyboard focus. */}
        <div className="flex shrink-0 items-center opacity-100 md:opacity-0 focus-within:opacity-100 group-hover:opacity-100">
          <button
            type="button"
            title={t("tree.newInside")}
            aria-label={t("tree.newInsideOf", { title: doc.title })}
            onClick={() => onCreateChild(doc)}
            className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
          >
            <Plus className="h-3.5 w-3.5" />
          </button>
          <DropdownMenu>
            <DropdownMenuTrigger
              title={t("tree.pageActions")}
              aria-label={t("tree.actionsFor", { title: doc.title })}
              className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-background hover:text-foreground"
            >
              <MoreHorizontal className="h-3.5 w-3.5" />
            </DropdownMenuTrigger>
            <DropdownMenuContent className="min-w-[9rem]">
              <DropdownMenuItem onClick={() => onRename(doc)}>
                {t("actions.rename")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => onMove(doc)}>
                {t("tree.moveTo")}
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() => onDelete(doc)}
                className="text-destructive"
              >
                {t("actions.delete")}
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
              expanded={expanded}
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
