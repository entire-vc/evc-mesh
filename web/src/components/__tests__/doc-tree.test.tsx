import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DocTree, buildDocTree, moveTargets } from "@/components/doc-tree";
import {
  AA_NON_TEXT,
  AA_TEXT,
  brandkitThemes,
  contrast,
} from "@/test-utils/brandkit-contrast";
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
    version: 1,
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

function baseProps(): Parameters<typeof DocTree>[0] {
  return {
    documents: DOCS,
    selectedId: null,
    onSelect: vi.fn(),
    onCreateChild: vi.fn(),
    onRename: vi.fn(),
    onMove: vi.fn(),
    onDelete: vi.fn(),
  };
}

function renderTree(overrides: Partial<Parameters<typeof DocTree>[0]> = {}) {
  const props = {
    documents: DOCS,
    selectedId: null,
    onSelect: vi.fn(),
    onCreateChild: vi.fn(),
    onRename: vi.fn(),
    onMove: vi.fn(),
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

describe("moveTargets", () => {
  it("excludes the document itself and its whole subtree", () => {
    const ids = moveTargets(DOCS, "child-a").map((t) => t.doc.id);

    // "grandchild" is the descendant: offering it would be offering a move the
    // server rejects, and one that would strand the subtree if it did not.
    expect(ids).not.toContain("child-a");
    expect(ids).not.toContain("grandchild");
  });

  it("offers every other document, in tree order with its depth", () => {
    expect(moveTargets(DOCS, "child-a")).toEqual([
      { doc: expect.objectContaining({ id: "root" }), depth: 0 },
      { doc: expect.objectContaining({ id: "child-b" }), depth: 1 },
    ]);
  });

  it("still offers the current parent, so the list does not shift under the user", () => {
    const ids = moveTargets(DOCS, "grandchild").map((t) => t.doc.id);

    expect(ids).toContain("child-a");
  });

  it("excludes a descendant reached through more than one level", () => {
    const ids = moveTargets(DOCS, "root").map((t) => t.doc.id);

    // Everything in this fixture sits under root, so a correct answer is empty.
    // A prune that only skipped direct children would return the grandchild.
    expect(ids).toEqual([]);
  });
});

describe("DocTree", () => {
  it("starts collapsed: roots only, no children rendered", () => {
    renderTree();

    expect(screen.getByText("Root")).toBeInTheDocument();
    expect(screen.queryByText("Child A")).not.toBeInTheDocument();
    expect(screen.queryByText("Grandchild")).not.toBeInTheDocument();
  });

  it("expands exactly the ancestors of the selected document", () => {
    renderTree({ selectedId: "grandchild" });

    expect(screen.getByText("Child A")).toBeInTheDocument();
    expect(screen.getByText("Grandchild")).toBeInTheDocument();
    // sibling branch stays closed, and so does the selected leaf's own subtree
    expect(screen.getByText("Child B")).toBeInTheDocument();
    for (const title of ["Root", "Child A"]) {
      const item = screen.getByLabelText(`Collapse ${title}`).closest('[role="treeitem"]');
      expect(item).toHaveAttribute("aria-expanded", "true");
    }
  });

  it("keeps sibling branches collapsed when a deep document is selected", () => {
    const docs = [
      ...DOCS,
      makeDoc({ id: "other", title: "Other", position: 1 }),
      makeDoc({ id: "other-kid", title: "Other Kid", parent_id: "other" }),
    ];
    renderTree({ documents: docs, selectedId: "grandchild" });

    expect(screen.getByText("Other")).toBeInTheDocument();
    expect(screen.queryByText("Other Kid")).not.toBeInTheDocument();
  });

  it("collapses and expands a node", () => {
    renderTree({ selectedId: "child-a" });

    fireEvent.click(screen.getByLabelText("Collapse Root"));
    expect(screen.queryByText("Child A")).not.toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Expand Root"));
    expect(screen.getByText("Child A")).toBeInTheDocument();
  });

  describe("remembering the expansion", () => {
    const KEY = "mesh.docs.tree.expanded.u1.p1";
    beforeEach(() => window.localStorage.clear());

    it("survives a remount", () => {
      const first = render(
        <DocTree {...baseProps()} storageKey={KEY} />,
      );
      fireEvent.click(screen.getByLabelText("Expand Root"));
      expect(screen.getByText("Child A")).toBeInTheDocument();
      first.unmount();

      render(<DocTree {...baseProps()} storageKey={KEY} />);
      expect(screen.getByText("Child A")).toBeInTheDocument();
      expect(screen.queryByText("Grandchild")).not.toBeInTheDocument();
    });

    it("drops ids of deleted documents on read", () => {
      window.localStorage.setItem(KEY, JSON.stringify(["root", "gone"]));
      render(<DocTree {...baseProps()} storageKey={KEY} />);
      expect(screen.getByText("Child A")).toBeInTheDocument();
      const saved = JSON.parse(window.localStorage.getItem(KEY) ?? "[]") as string[];
      expect(saved).toEqual(["root"]);
    });

    it("does not touch saved state or expand anything while the list is another project's", () => {
      window.localStorage.setItem(KEY, JSON.stringify(["p2-root"]));
      const props = baseProps(); // documents belong to project p1
      const { rerender } = render(
        <DocTree {...props} storageKey={KEY} projectId="p2" />,
      );
      expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["p2-root"]);
      // the other project's list is not drawn while it is the stale one
      expect(screen.queryByText("Root")).not.toBeInTheDocument();

      const own = [
        makeDoc({ id: "p2-root", project_id: "p2", title: "P2 Root" }),
        makeDoc({ id: "p2-kid", project_id: "p2", title: "P2 Kid", parent_id: "p2-root" }),
        makeDoc({ id: "p2-other", project_id: "p2", title: "P2 Other", position: 1 }),
        makeDoc({ id: "p2-other-kid", project_id: "p2", title: "P2 Other Kid", parent_id: "p2-other" }),
      ];
      rerender(<DocTree {...props} documents={own} storageKey={KEY} projectId="p2" />);

      // the saved branch is open, the other one is not (nothing was auto-expanded)
      expect(screen.getByText("P2 Kid")).toBeInTheDocument();
      expect(screen.queryByText("P2 Other Kid")).not.toBeInTheDocument();
      expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["p2-root"]);
    });

    it("clears the saved ids when the last document of the project is deleted", () => {
      const props = baseProps();
      const { rerender } = render(
        <DocTree {...props} storageKey={KEY} projectId="p1" selectedId="child-a" />,
      );
      expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["root"]);
      rerender(<DocTree {...props} documents={[]} storageKey={KEY} projectId="p1" />);
      expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual([]);
    });

    it("does not carry one project's expansion into another's key when not remounted", () => {
      const K2 = "mesh.docs.tree.expanded.u1.p2";
      const props = baseProps();
      const { rerender } = render(
        <DocTree {...props} storageKey={KEY} projectId="p1" selectedId="child-a" />,
      );
      expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["root"]);
      const own = [makeDoc({ id: "root", project_id: "p2", title: "Root" })];
      rerender(<DocTree {...props} documents={own} storageKey={K2} projectId="p2" />);
      expect(JSON.parse(window.localStorage.getItem(K2) ?? "[]")).toEqual([]);
      expect(JSON.parse(window.localStorage.getItem(KEY) ?? "[]")).toEqual(["root"]);
    });

    it("works without storage", () => {
      vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
        throw new Error("denied");
      });
      vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
        throw new Error("denied");
      });
      render(<DocTree {...baseProps()} storageKey={KEY} />);
      expect(screen.queryByText("Child A")).not.toBeInTheDocument();
      fireEvent.click(screen.getByLabelText("Expand Root"));
      expect(screen.getByText("Child A")).toBeInTheDocument();
      vi.restoreAllMocks();
    });
  });

  it("expands a collapsed parent when a child appears under it", () => {
    const props = baseProps();
    const { rerender } = render(<DocTree {...props} />);
    expect(screen.queryByText("Child A")).not.toBeInTheDocument();

    const created = makeDoc({ id: "fresh", title: "Fresh", parent_id: "root", position: 5 });
    rerender(<DocTree {...props} documents={[...DOCS, created]} />);

    expect(screen.getByText("Fresh")).toBeInTheDocument();
  });

  it("expands the new parent when a document is moved", () => {
    const props = baseProps();
    const { rerender } = render(<DocTree {...props} />);

    const moved = DOCS.map((d) => (d.id === "child-b" ? { ...d, parent_id: "child-a" } : d));
    rerender(<DocTree {...props} documents={moved} />);

    expect(screen.getByText("Child A")).toBeInTheDocument();
    expect(screen.getByText("Child B")).toBeInTheDocument();
  });

  it("selects a document when its title is clicked", () => {
    const props = renderTree({ selectedId: "child-b" });

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

  it("fills the selected row with the light secondary surface and its own foreground", () => {
    // Pinned deliberately. The selected row used to be --accent, a saturated
    // teal carrying the default near-black text, and "you cannot see what is
    // there" is exactly how it was reported. A refactor that drops either half
    // of this pairing reintroduces that.
    renderTree({ selectedId: "child-a" });

    const [row] = screen
      .getAllByRole("treeitem")
      .filter((el) => el.getAttribute("aria-selected") === "true");

    expect(row).toHaveClass("bg-secondary");
    expect(row).toHaveClass("text-secondary-foreground");
    expect(row).not.toHaveClass("bg-accent");
  });

  it("gives the selected row a second cue beyond its fill", () => {
    // --secondary and --muted (the hover fill) are close in lightness on
    // purpose, so selection cannot be carried by the fill alone: the row also
    // gets a brand-coloured bar at the column edge and heavier text.
    renderTree({ selectedId: "child-a" });

    const [row] = screen
      .getAllByRole("treeitem")
      .filter((el) => el.getAttribute("aria-selected") === "true");

    expect(row!.querySelector("[data-selected-bar]")).toBeInTheDocument();
    expect(row).toHaveClass("font-medium");
  });

  it("leaves the unselected rows to the neutral hover tint", () => {
    renderTree({ selectedId: "child-a" });

    const [other] = screen
      .getAllByRole("treeitem")
      .filter((el) => el.textContent?.includes("Child B"));

    expect(other).toHaveClass("hover:bg-muted");
    expect(other).not.toHaveClass("bg-secondary");
    expect(other!.querySelector("[data-selected-bar]")).toBeNull();
  });
});

/**
 * The selected row's colours are theme tokens, so the check that they are
 * readable belongs to the tokens rather than to a rendered pixel. These read
 * brandkit.css — the file the app actually ships — and compute WCAG contrast
 * for the pairing the tree uses, in both themes. Eyeballing is what produced
 * the bug this replaces.
 */
describe("selected-row contrast", () => {
  // The parse-and-measure machinery moved to @/test-utils/brandkit-contrast when
  // the document editor's toolbar turned out to have the same defect and needed
  // the same check. One implementation, so the two cannot disagree about what
  // the shipped tokens measure.
  const { light, dark } = brandkitThemes();

  it("reads on the light theme", () => {
    // WCAG AA for body text.
    expect(contrast(light, "--secondary", "--secondary-foreground")).toBeGreaterThanOrEqual(AA_TEXT);
  });

  it("reads on the dark theme", () => {
    expect(contrast(dark, "--secondary", "--secondary-foreground")).toBeGreaterThanOrEqual(AA_TEXT);
  });

  it("beats what it replaced, which is why this was reported", () => {
    // --accent with the inherited body foreground: the old selected row.
    const before = contrast(light, "--accent", "--foreground");
    const after = contrast(light, "--secondary", "--secondary-foreground");
    expect(before).toBeLessThan(AA_TEXT);
    expect(after).toBeGreaterThan(before);
  });

  it("keeps the accent bar visible against the row it sits on", () => {
    // Non-text contrast (WCAG 1.4.11) is 3:1.
    expect(contrast(light, "--secondary", "--primary")).toBeGreaterThanOrEqual(AA_NON_TEXT);
    expect(contrast(dark, "--secondary", "--primary")).toBeGreaterThanOrEqual(AA_NON_TEXT);
  });

  it("offers creating a child under a node", () => {
    const props = renderTree();

    fireEvent.click(screen.getByLabelText("New page inside Root"));

    expect(props.onCreateChild).toHaveBeenCalledWith(
      expect.objectContaining({ id: "root" }),
    );
  });

  it("exposes rename, move and delete in the per-node menu", () => {
    const props = renderTree({ selectedId: "child-b" });

    fireEvent.click(screen.getByLabelText("Actions for Child B"));
    fireEvent.click(screen.getByText("Rename"));
    expect(props.onRename).toHaveBeenCalledWith(
      expect.objectContaining({ id: "child-b" }),
    );

    fireEvent.click(screen.getByLabelText("Actions for Child B"));
    fireEvent.click(screen.getByText("Move to..."));
    expect(props.onMove).toHaveBeenCalledWith(
      expect.objectContaining({ id: "child-b" }),
    );

    fireEvent.click(screen.getByLabelText("Actions for Child B"));
    fireEvent.click(screen.getByText("Delete"));
    expect(props.onDelete).toHaveBeenCalledWith(
      expect.objectContaining({ id: "child-b" }),
    );
  });
});
