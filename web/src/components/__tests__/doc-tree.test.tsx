import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DocTree, buildDocTree } from "@/components/doc-tree";
import type { ProjectDocument } from "@/types";

function makeDoc(overrides: Partial<ProjectDocument> & { id: string }): ProjectDocument {
  return {
    project_id: "p1",
    parent_id: null,
    slug: overrides.id,
    title: overrides.id,
    storage_key: `documents/p1/${overrides.id}.md`,
    position: 0,
    created_by: "u1",
    created_by_type: "user",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  };
}

// root
//  ├── child-a (position 0)
//  │    └── grandchild
//  └── child-b (position 1)
const DOCS: ProjectDocument[] = [
  makeDoc({ id: "child-b", title: "Child B", parent_id: "root", position: 1 }),
  makeDoc({ id: "grandchild", title: "Grandchild", parent_id: "child-a", position: 0 }),
  makeDoc({ id: "root", title: "Root", position: 0 }),
  makeDoc({ id: "child-a", title: "Child A", parent_id: "root", position: 0 }),
];

function renderTree(overrides: Partial<Parameters<typeof DocTree>[0]> = {}) {
  const props = {
    documents: DOCS,
    selectedId: null,
    onSelect: vi.fn(),
    onCreateChild: vi.fn(),
    onRename: vi.fn(),
    onDelete: vi.fn(),
    ...overrides,
  };
  render(<DocTree {...props} />);
  return props;
}

describe("buildDocTree", () => {
  it("nests by parent_id and orders siblings by position", () => {
    const tree = buildDocTree(DOCS);

    expect(tree).toHaveLength(1);
    const root = tree[0]!;
    expect(root.doc.id).toBe("root");
    expect(root.children.map((c) => c.doc.id)).toEqual(["child-a", "child-b"]);
    expect(root.children[0]!.children.map((c) => c.doc.id)).toEqual(["grandchild"]);
  });

  it("orders siblings sharing a position by title", () => {
    const tree = buildDocTree([
      makeDoc({ id: "b", title: "Beta", position: 0 }),
      makeDoc({ id: "a", title: "Alpha", position: 0 }),
    ]);

    expect(tree.map((n) => n.doc.id)).toEqual(["a", "b"]);
  });

  it("keeps a document whose parent is missing, as a root", () => {
    const tree = buildDocTree([
      makeDoc({ id: "orphan", title: "Orphan", parent_id: "gone" }),
    ]);

    expect(tree.map((n) => n.doc.id)).toEqual(["orphan"]);
  });

  it("does not hang or drop nodes when parents form a cycle", () => {
    const tree = buildDocTree([
      makeDoc({ id: "x", title: "X", parent_id: "y" }),
      makeDoc({ id: "y", title: "Y", parent_id: "x" }),
    ]);

    const flat: string[] = [];
    const walk = (nodes: ReturnType<typeof buildDocTree>) => {
      for (const n of nodes) {
        flat.push(n.doc.id);
        walk(n.children);
      }
    };
    walk(tree);

    expect(flat.sort()).toEqual(["x", "y"]);
  });
});

describe("DocTree", () => {
  it("renders the hierarchy with children visible by default", () => {
    renderTree();

    expect(screen.getByText("Root")).toBeInTheDocument();
    expect(screen.getByText("Child A")).toBeInTheDocument();
    expect(screen.getByText("Grandchild")).toBeInTheDocument();
  });

  it("collapses and expands a node", () => {
    renderTree();

    fireEvent.click(screen.getByLabelText("Collapse Root"));
    expect(screen.queryByText("Child A")).not.toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Expand Root"));
    expect(screen.getByText("Child A")).toBeInTheDocument();
  });

  it("selects a document when its title is clicked", () => {
    const props = renderTree();

    fireEvent.click(screen.getByText("Child B"));

    expect(props.onSelect).toHaveBeenCalledWith(
      expect.objectContaining({ id: "child-b" }),
    );
  });

  it("marks the selected document", () => {
    renderTree({ selectedId: "child-a" });

    const selected = screen
      .getAllByRole("treeitem")
      .filter((el) => el.getAttribute("aria-selected") === "true");

    expect(selected).toHaveLength(1);
    expect(selected[0]).toHaveTextContent("Child A");
  });

  it("offers creating a child under a node", () => {
    const props = renderTree();

    fireEvent.click(screen.getByLabelText("New page inside Root"));

    expect(props.onCreateChild).toHaveBeenCalledWith(
      expect.objectContaining({ id: "root" }),
    );
  });

  it("exposes rename and delete in the per-node menu", () => {
    const props = renderTree();

    fireEvent.click(screen.getByLabelText("Actions for Child B"));
    fireEvent.click(screen.getByText("Rename"));
    expect(props.onRename).toHaveBeenCalledWith(
      expect.objectContaining({ id: "child-b" }),
    );

    fireEvent.click(screen.getByLabelText("Actions for Child B"));
    fireEvent.click(screen.getByText("Delete"));
    expect(props.onDelete).toHaveBeenCalledWith(
      expect.objectContaining({ id: "child-b" }),
    );
  });
});
