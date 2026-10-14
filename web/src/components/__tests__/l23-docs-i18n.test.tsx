import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DocTree } from "@/components/doc-tree";
import { DocMeta } from "@/components/doc-meta";
import i18n, { setLanguage } from "@/i18n";
import type { ProjectDocument } from "@/types";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

const parent = {
  id: "d1",
  project_id: "p1",
  parent_id: null,
  slug: "runbook",
  title: "Runbook",
  storage_key: "documents/p1/d1.md",
  position: 0,
  created_by: "11111111-1111-1111-1111-111111111111",
  created_by_type: "user",
  created_by_name: "Pavel",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-14T09:30:00Z",
  version: 1,
} as ProjectDocument;
const child = { ...parent, id: "d2", parent_id: "d1", slug: "child", title: "Child" } as ProjectDocument;

function renderDocs() {
  return render(
    <>
      <DocTree
        documents={[parent, child]}
        selectedId={null}
        onSelect={vi.fn()}
        onCreateChild={vi.fn()}
        onRename={vi.fn()}
        onMove={vi.fn()}
        onDelete={vi.fn()}
      />
      <DocMeta doc={parent} />
    </>,
  );
}

// A raw key leaks as `tree.heading`, `meta.createdBy`, ... in the DOM or in a label.
const RAW_KEY = /\b(tree|meta|actions|comments|editor|watch)\.[a-zA-Z]+/;

describe("L2.3 docs: en by default, ru translated, no raw keys", () => {
  it("renders the document tree and meta line in en, then ru", async () => {
    const en = renderDocs();
    expect(screen.getByRole("tree", { name: "Documents" })).toBeTruthy();
    expect(screen.getByLabelText("Collapse Runbook")).toBeTruthy();
    expect(screen.getByLabelText("New page inside Runbook")).toBeTruthy();
    expect(document.body.textContent).toContain("Created by Pavel");
    expect(document.body.innerHTML).not.toMatch(RAW_KEY);
    en.unmount();

    await act(async () => {
      await setLanguage("ru");
    });
    renderDocs();
    expect(screen.getByRole("tree", { name: "Документы" })).toBeTruthy();
    expect(screen.getByLabelText("Свернуть «Runbook»")).toBeTruthy();
    expect(screen.getByLabelText("Новая страница внутри «Runbook»")).toBeTruthy();
    expect(document.body.textContent).toContain("Автор: Pavel");
    expect(document.body.textContent).not.toContain("Created by");
    expect(document.body.innerHTML).not.toMatch(RAW_KEY);
  });

  it("pluralises the nested-page delete confirmation in ru", async () => {
    await act(async () => {
      await setLanguage("ru");
    });
    const confirm = (count: number) =>
      i18n.t("docs:delete.confirmWithChildren", { title: "X", count });
    expect(confirm(1)).toBe("Удалить «X» и 1 вложенную страницу?");
    expect(confirm(3)).toBe("Удалить «X» и 3 вложенные страницы?");
    expect(confirm(5)).toBe("Удалить «X» и 5 вложенных страниц?");
  });
});
