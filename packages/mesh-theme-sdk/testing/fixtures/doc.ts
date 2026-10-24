import type { DocNodeData, DocTreeData, DocViewDoc, Id } from "../../src/index";
import { NOW } from "./common";

/** One visible node of `doc.tree` (a leaf at the root, nothing expanded or selected). */
export function docNode(id: Id, over: Partial<DocNodeData> = {}): DocNodeData {
  return {
    id, title: `Doc ${id}`, slug: id, depth: 0, hasChildren: false, expanded: false, selected: false,
    updatedAt: NOW, href: `/docs/${id}`, ...over,
  };
}

/**
 * `doc.tree` data: root (expanded, selected) > kid-a (has children, closed), kid-b (leaf).
 * The list is what the container hands the view: visible nodes only, in display order.
 */
export function docTree(): DocTreeData {
  return {
    projectId: "p1",
    nodes: {
      state: "ready",
      value: [
        docNode("root", { hasChildren: true, expanded: true }),
        docNode("kid-a", { depth: 1, hasChildren: true }),
        docNode("kid-b", { depth: 1, selected: true }),
      ],
    },
  };
}

/** The open document of `doc.view` (no author of the last change: the empty baseline). */
export function doc(over: Partial<DocViewDoc> = {}): DocViewDoc {
  return {
    id: "d1", title: "Runbook", slug: "runbook",
    path: [{ label: "Ops", href: "/docs/ops" }, { label: "Runbook", href: "/docs/d1" }],
    updatedAt: NOW, updatedBy: null, version: 3, ...over,
  };
}
