import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { DocBreadcrumbs } from "@/components/doc-breadcrumbs";
import { buildDocPath } from "@/components/doc-tree";
import type { ProjectDocument } from "@/types";

function makeDoc(
  overrides: Partial<ProjectDocument> & { id: string },
): ProjectDocument {
  return {
    project_id: "proj-1",
    parent_id: null,
    slug: overrides.id,
    title: overrides.id,
    storage_key: `documents/proj-1/${overrides.id}.md`,
    position: 0,
    created_by: "u1",
    created_by_type: "user",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    version: 1,
    ...overrides,
  };
}

const root = makeDoc({ id: "root", title: "Engineering" });
const mid = makeDoc({ id: "mid", title: "Decisions", parent_id: "root" });
const leaf = makeDoc({
  id: "leaf",
  title: "ADR-004 Document storage",
  parent_id: "mid",
});
const DOCS = [root, mid, leaf];

function renderCrumbs(
  current: ProjectDocument,
  documents: ProjectDocument[] = DOCS,
) {
  const onNavigate = vi.fn();
  const onNavigateRoot = vi.fn();
  render(
    <DocBreadcrumbs
      documents={documents}
      current={current}
      onNavigate={onNavigate}
      onNavigateRoot={onNavigateRoot}
    />,
  );
  return { onNavigate, onNavigateRoot };
}

const crumbs = () =>
  within(screen.getByRole("navigation", { name: "Breadcrumb" }))
    .getAllByRole("listitem")
    .map((li) => li.textContent?.trim());

describe("DocBreadcrumbs", () => {
  it("shows the whole chain, root first and this page last", () => {
    renderCrumbs(leaf);
    expect(crumbs()).toEqual([
      "Documents",
      "Engineering",
      "Decisions",
      "ADR-004 Document storage",
    ]);
  });

  it("is a labelled landmark, so it can be skipped", () => {
    renderCrumbs(leaf);
    expect(
      screen.getByRole("navigation", { name: "Breadcrumb" }),
    ).toBeInTheDocument();
  });

  it("navigates to the ancestor that was clicked", () => {
    const { onNavigate } = renderCrumbs(leaf);
    fireEvent.click(screen.getByRole("button", { name: "Decisions" }));
    expect(onNavigate).toHaveBeenCalledWith(mid);

    fireEvent.click(screen.getByRole("button", { name: "Engineering" }));
    expect(onNavigate).toHaveBeenCalledWith(root);
  });

  it("goes back to the docs root from the leading crumb", () => {
    const { onNavigateRoot, onNavigate } = renderCrumbs(leaf);
    fireEvent.click(screen.getByRole("button", { name: "Documents" }));
    expect(onNavigateRoot).toHaveBeenCalledTimes(1);
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("marks the current page and does not make it a link to itself", () => {
    renderCrumbs(leaf);
    const current = screen.getByText("ADR-004 Document storage");
    expect(current).toHaveAttribute("aria-current", "page");
    expect(
      screen.queryByRole("button", { name: "ADR-004 Document storage" }),
    ).not.toBeInTheDocument();
  });

  it("shows only root and self for a top-level page", () => {
    renderCrumbs(root);
    expect(crumbs()).toEqual(["Documents", "Engineering"]);
  });

  it("prefers the open document's title over the copy in the list", () => {
    // After a rename the open document is updated first; reading the stale
    // list entry would print the old title beside the new heading.
    renderCrumbs({ ...leaf, title: "ADR-004 Storage (renamed)" });
    expect(crumbs()).toEqual([
      "Documents",
      "Engineering",
      "Decisions",
      "ADR-004 Storage (renamed)",
    ]);
  });

  it("still names the page when the list has not arrived yet", () => {
    // What a deep link to /docs/:id renders before the tree loads.
    renderCrumbs(leaf, []);
    expect(crumbs()).toEqual(["Documents", "ADR-004 Document storage"]);
  });

  it("treats a page whose parent is missing as a root, exactly as the tree does", () => {
    renderCrumbs(leaf, [leaf]);
    expect(crumbs()).toEqual(["Documents", "ADR-004 Document storage"]);
  });
});

describe("buildDocPath", () => {
  it("returns the chain from a root down to the document, inclusive", () => {
    expect(buildDocPath(DOCS, "leaf").map((d) => d.id)).toEqual([
      "root",
      "mid",
      "leaf",
    ]);
  });

  it("returns just the document for a root", () => {
    expect(buildDocPath(DOCS, "root").map((d) => d.id)).toEqual(["root"]);
  });

  it("returns nothing for a document that is not in the list", () => {
    expect(buildDocPath(DOCS, "nope")).toEqual([]);
  });

  it("terminates on a parent cycle instead of hanging the tab", () => {
    const a = makeDoc({ id: "a", parent_id: "b" });
    const b = makeDoc({ id: "b", parent_id: "a" });
    // Whatever the walk decides the order is, it must finish and must include
    // the document being asked about.
    const path = buildDocPath([a, b], "a");
    expect(path[path.length - 1]?.id).toBe("a");
  });
});
