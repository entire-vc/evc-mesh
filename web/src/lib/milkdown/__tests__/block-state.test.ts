import { afterEach, describe, expect, it } from "vitest";
import { editorViewCtx, serializerCtx } from "@milkdown/kit/core";
import type { Editor } from "@milkdown/kit/core";
import { TextSelection } from "@milkdown/kit/prose/state";
import type { EditorView } from "@milkdown/kit/prose/view";
import { makeDocEditor } from "@/lib/milkdown/editor";
import {
  EMPTY_BLOCK_STATE,
  blockStatePlugin,
  computeBlockState,
  createBlockStateStore,
  cursorToCell,
  deleteTableColumn,
  deleteTableRow,
  deleteWholeTable,
  selectCellForMenu,
  setColumnAlignment,
  tableStartAt,
} from "@/lib/milkdown/block-state";
import { CellSelection } from "@milkdown/kit/prose/tables";

/**
 * The table edits the toolbar menu runs, against the real schema from the
 * factory the editors use — so "the table survives" means the GFM schema
 * (`table_header_row table_row+`) still holds and the markdown still is a
 * table, not that some hand-built fixture does.
 */

let editor: Editor | null = null;
afterEach(async () => {
  await editor?.destroy();
  editor = null;
});

async function open(markdown: string, store = createBlockStateStore()) {
  const root = document.createElement("div");
  document.body.appendChild(root);
  editor = makeDocEditor({
    root,
    defaultValue: markdown,
    editable: true,
    plugins: [blockStatePlugin(store)],
  });
  await editor.create();
  const view = editor.ctx.get(editorViewCtx);
  const md = () => editor!.ctx.get(serializerCtx)(view.state.doc);
  return { view, md, store };
}

/** Put the caret inside the cell whose text is `text`. */
function caretIn(view: EditorView, text: string) {
  let at = -1;
  view.state.doc.descendants((node, pos) => {
    if (at < 0 && node.isText && node.text === text) at = pos + 1;
    return at < 0;
  });
  if (at < 0) throw new Error(`no cell "${text}"`);
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, at)));
}

function rows(md: string): string[][] {
  return md
    .split("\n")
    .filter((l) => l.startsWith("|") && !/^\|\s*:?-/.test(l))
    .map((l) =>
      l
        .slice(1, -1)
        .split("|")
        .map((c) => c.trim()),
    );
}

const T = `| A | B | C |
| :- | :-: | -: |
| a1 | b1 | c1 |
| a2 | b2 | c2 |
`;

describe("deleteTableRow", () => {
  it("deletes a middle body row", async () => {
    const { view, md } = await open(T);
    caretIn(view, "a1");
    expect(deleteTableRow(view.state, view.dispatch)).toBe(true);
    expect(rows(md())).toEqual([
      ["A", "B", "C"],
      ["a2", "b2", "c2"],
    ]);
    // The caret stays in the table, on the row that moved up.
    expect(computeBlockState(view.state).inTable).toBe(true);
  });

  it("deletes the last body row and keeps the caret in the table", async () => {
    const { view, md } = await open(T);
    caretIn(view, "c2");
    expect(deleteTableRow(view.state, view.dispatch)).toBe(true);
    expect(rows(md())).toEqual([
      ["A", "B", "C"],
      ["a1", "b1", "c1"],
    ]);
    expect(computeBlockState(view.state).inTable).toBe(true);
  });

  it("deletes the header by promoting the first body row, keeping alignment", async () => {
    const { view, md } = await open(T);
    caretIn(view, "B");
    expect(deleteTableRow(view.state, view.dispatch)).toBe(true);
    const out = md();
    expect(rows(out)).toEqual([
      ["a1", "b1", "c1"],
      ["a2", "b2", "c2"],
    ]);
    // Alignment lives in the header's delimiter row in GFM: it must survive the
    // header being replaced.
    const delimiter = out.split("\n").find((l) => /^\|\s*:?-/.test(l)) ?? "";
    expect(delimiter.replace(/\s/g, "")).toMatch(/^\|:-+\|:-+:\|-+:\|$/);
    expect(view.state.doc.firstChild?.firstChild?.type.name).toBe("table_header_row");
    // Schema check: the document is still valid after the replacement.
    expect(() => view.state.doc.check()).not.toThrow();
  });

  it("refuses to leave a header-only table", async () => {
    const { view, md } = await open("| A | B |\n| - | - |\n| a | b |\n");
    const before = md();
    caretIn(view, "a");
    expect(deleteTableRow(view.state, view.dispatch)).toBe(false);
    caretIn(view, "A");
    expect(deleteTableRow(view.state, view.dispatch)).toBe(false);
    expect(md()).toBe(before);
  });

  it("is a no-op outside a table", async () => {
    const { view } = await open("Hello");
    expect(deleteTableRow(view.state, view.dispatch)).toBe(false);
  });
});

describe("deleteTableColumn / deleteWholeTable", () => {
  it("deletes a column from a body cell", async () => {
    const { view, md } = await open(T);
    caretIn(view, "b2");
    expect(deleteTableColumn(view.state, view.dispatch)).toBe(true);
    expect(rows(md())).toEqual([
      ["A", "C"],
      ["a1", "c1"],
      ["a2", "c2"],
    ]);
  });

  it("refuses to delete the only column", async () => {
    const { view, md } = await open("| A |\n| - |\n| a |\n");
    const before = md();
    caretIn(view, "a");
    expect(deleteTableColumn(view.state, view.dispatch)).toBe(false);
    expect(md()).toBe(before);
  });

  it("deletes the table and nothing around it", async () => {
    const { view, md } = await open(`Before\n\n${T}\nAfter\n`);
    caretIn(view, "b1");
    expect(deleteWholeTable(view.state, view.dispatch)).toBe(true);
    expect(md().trim()).toBe("Before\n\nAfter");
  });
});

describe("setColumnAlignment", () => {
  it("aligns the whole column from a body cell, so the markdown changes", async () => {
    // The preset's setAlignCommand only touches the selected cell; GFM writes
    // alignment from the header, so from a body cell the file never changed.
    const { view, md } = await open("| A | B |\n| - | - |\n| a | b |\n");
    caretIn(view, "b");
    setColumnAlignment("center")(view.state, view.dispatch);
    const delimiter = md().split("\n").find((l) => /^\|\s*:?-/.test(l)) ?? "";
    const cols = delimiter.slice(1, -1).split("|").map((c) => c.trim());
    expect(cols[1]).toMatch(/^:-+:$/);
    expect(cols[0]).not.toMatch(/:$/);
    expect(computeBlockState(view.state).align).toBe("center");
  });

  it("right-aligns as `---:`", async () => {
    const { view, md } = await open("| A | B |\n| - | - |\n| a | b |\n");
    caretIn(view, "a");
    setColumnAlignment("right")(view.state, view.dispatch);
    expect(md()).toMatch(/\|\s*-+:\s*\|/);
  });
});

describe("computeBlockState and the store", () => {
  it("reads paragraph, heading and code-block context", async () => {
    const { view } = await open("Para\n\n#### Four\n\n```\ncode\n```\n");
    caretIn(view, "Para");
    expect(computeBlockState(view.state)).toMatchObject({ level: 0, canSetLevel: true });
    caretIn(view, "Four");
    expect(computeBlockState(view.state)).toMatchObject({ level: 4, canSetLevel: true });
    caretIn(view, "code");
    expect(computeBlockState(view.state)).toMatchObject({ level: null, canSetLevel: false });
  });

  it("reads the table shape and the header", async () => {
    const { view } = await open(T);
    caretIn(view, "A");
    expect(computeBlockState(view.state)).toMatchObject({
      inTable: true,
      inHeader: true,
      rows: 3,
      cols: 3,
      selectedRows: 1,
      selectedCols: 1,
      align: "left",
      canSetLevel: false,
    });
    caretIn(view, "c1");
    expect(computeBlockState(view.state)).toMatchObject({ inHeader: false, align: "right" });
  });

  it("follows the caret through the plugin and notifies only on a real change", async () => {
    const store = createBlockStateStore();
    let notified = 0;
    store.subscribe(() => notified++);
    const { view } = await open(`Para\n\n${T}`, store);
    expect(store.get().level).toBe(0);

    caretIn(view, "b1");
    expect(store.get()).toMatchObject({ inTable: true, inHeader: false });
    const afterMove = notified;

    // Typing inside the same cell changes nothing the toolbar shows.
    view.dispatch(view.state.tr.insertText("x"));
    expect(notified).toBe(afterMove);

    await editor!.destroy();
    editor = null;
    expect(store.get()).toEqual(EMPTY_BLOCK_STATE);
  });
});

/** Position just inside the cell whose text is `text`. */
function posIn(view: EditorView, text: string): number {
  let at = -1;
  view.state.doc.descendants((node, pos) => {
    if (at < 0 && node.isText && node.text === text) at = pos + 1;
    return at < 0;
  });
  if (at < 0) throw new Error(`no cell "${text}"`);
  return at;
}

const cursorText = (view: EditorView) => view.state.selection.$from.parent.textContent;

describe("pointing the selection at a cell (right-click menu, + handles)", () => {
  it("tableStartAt finds the table around a position, and nothing outside one", async () => {
    const { view } = await open(`Intro\n\n${T}`);
    const start = tableStartAt(view.state, posIn(view, "b1"));
    expect(start).not.toBeNull();
    expect(view.state.doc.nodeAt(start!)?.type.name).toBe("table");
    expect(tableStartAt(view.state, posIn(view, "Intro"))).toBeNull();
  });

  it("cursorToCell counts from the end with negative indices", async () => {
    const { view } = await open(T);
    const start = tableStartAt(view.state, posIn(view, "A"))!;
    cursorToCell(start, -1, -1)(view.state, view.dispatch);
    expect(cursorText(view)).toBe("c2");
    cursorToCell(start, 0, -1)(view.state, view.dispatch);
    expect(cursorText(view)).toBe("C");
    expect(cursorToCell(start, 5, 0)(view.state, view.dispatch)).toBe(false);
  });

  it("selectCellForMenu moves the cursor into a cell outside the selection", async () => {
    const { view } = await open(T);
    caretIn(view, "A");
    expect(selectCellForMenu(posIn(view, "b2"))(view.state, view.dispatch)).toBe(true);
    expect(cursorText(view)).toBe("b2");
  });

  it("selectCellForMenu keeps a multi-cell selection that covers the clicked cell", async () => {
    const { view } = await open(T);
    const $a = view.state.doc.resolve(posIn(view, "a1") - 3);
    const $b = view.state.doc.resolve(posIn(view, "c2") - 3);
    view.dispatch(view.state.tr.setSelection(new CellSelection($a, $b)));
    const before = view.state.selection;
    expect(selectCellForMenu(posIn(view, "b2"))(view.state, view.dispatch)).toBe(true);
    expect(view.state.selection.eq(before)).toBe(true);
    // Both rows go at once, as the person selected them.
    expect(computeBlockState(view.state).selectedRows).toBe(2);
  });

  it("selectCellForMenu refuses a position outside any table", async () => {
    const { view } = await open(`Intro\n\n${T}`);
    expect(selectCellForMenu(posIn(view, "Intro"))(view.state, view.dispatch)).toBe(false);
  });
});
