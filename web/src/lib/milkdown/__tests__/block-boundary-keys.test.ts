import { afterEach, describe, expect, it } from "vitest";
import { Editor, editorViewCtx } from "@milkdown/kit/core";
import type { EditorView } from "@milkdown/kit/prose/view";
import { TextSelection } from "@milkdown/kit/prose/state";
import { getMarkdown } from "@milkdown/kit/utils";
import { makeDocEditor } from "@/lib/milkdown/editor";

/**
 * Backspace / Delete at the edge of a block.
 *
 * Built through makeDocEditor and driven through the editor's own key handling,
 * so what passes here is the keymap that ships — not a command called by hand.
 * A key the keymap declines falls to the browser's native editing, which jsdom
 * does not perform; those cases assert only that the document was not
 * restructured.
 */

const open: Editor[] = [];

async function edit(markdown: string): Promise<{ editor: Editor; view: EditorView }> {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const editor = makeDocEditor({ root, defaultValue: markdown, editable: true });
  await editor.create();
  open.push(editor);
  return { editor, view: editor.action((ctx) => ctx.get(editorViewCtx)) };
}

afterEach(async () => {
  await Promise.all(open.splice(0).map((e) => e.destroy()));
  document.body.innerHTML = "";
});

/** Top-level blocks as `type[level]:text`, images as `[img]`. */
function blocks(view: EditorView): string[] {
  const out: string[] = [];
  view.state.doc.forEach((node) => {
    let text = "";
    node.descendants((child) => {
      if (child.isText) text += child.text;
      else if (child.type.name === "image") text += "[img]";
      return true;
    });
    const level = node.type.name === "heading" ? String(node.attrs.level) : "";
    out.push(`${node.type.name}${level}:${text}`);
  });
  return out;
}

/** Cursor at the start or end of the n-th top-level block. */
function caret(view: EditorView, index: number, where: "start" | "end"): void {
  let pos = 0;
  for (let i = 0; i < index; i++) pos += view.state.doc.child(i).nodeSize;
  const node = view.state.doc.child(index);
  const at = where === "start" ? pos + 1 : pos + 1 + node.content.size;
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, at)));
}

function press(view: EditorView, key: "Backspace" | "Delete"): boolean {
  const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
  return !!view.someProp("handleKeyDown", (f) => f(view, event));
}

/** Inserts an empty paragraph before the n-th top-level block. */
function insertEmptyBefore(view: EditorView, index: number): void {
  let pos = 0;
  for (let i = 0; i < index; i++) pos += view.state.doc.child(i).nodeSize;
  const paragraph = view.state.schema.nodes.paragraph!;
  view.dispatch(view.state.tr.insert(pos, paragraph.create()));
}

const HEADED = "Intro paragraph.\n\n## Heading two\n\nBody.\n";

describe("Backspace at the start of a heading", () => {
  it("turns the heading into a paragraph — never a bigger heading — and keeps its text", async () => {
    const { view } = await edit(HEADED);
    caret(view, 1, "start");
    expect(press(view, "Backspace")).toBe(true);
    expect(blocks(view)).toEqual([
      "paragraph:Intro paragraph.",
      "paragraph:Heading two",
      "paragraph:Body.",
    ]);
  });

  it("a second Backspace joins it with the block above, no text lost", async () => {
    const { view } = await edit(HEADED);
    caret(view, 1, "start");
    press(view, "Backspace");
    press(view, "Backspace");
    expect(blocks(view)).toEqual(["paragraph:Intro paragraph.Heading two", "paragraph:Body."]);
  });

  it("with an empty block above, removes only that block and keeps the heading", async () => {
    const { view } = await edit(HEADED);
    insertEmptyBefore(view, 1);
    caret(view, 2, "start");
    expect(press(view, "Backspace")).toBe(true);
    expect(blocks(view)).toEqual([
      "paragraph:Intro paragraph.",
      "heading2:Heading two",
      "paragraph:Body.",
    ]);
  });
});

describe("Delete next to a heading", () => {
  it("at the start of a heading does not change its level", async () => {
    const { view } = await edit(HEADED);
    caret(view, 1, "start");
    press(view, "Delete");
    expect(blocks(view)[1]).toBe("heading2:Heading two");
  });

  it("at the end of a paragraph before a heading keeps both texts on their own lines", async () => {
    const { view } = await edit(HEADED);
    caret(view, 0, "end");
    expect(press(view, "Delete")).toBe(true);
    expect(blocks(view)).toEqual([
      "paragraph:Intro paragraph.",
      "paragraph:Heading two",
      "paragraph:Body.",
    ]);
  });

  it("at the end of an empty paragraph before a heading removes only the empty one", async () => {
    const { view } = await edit(HEADED);
    insertEmptyBefore(view, 1);
    caret(view, 1, "end");
    expect(press(view, "Delete")).toBe(true);
    expect(blocks(view)).toEqual([
      "paragraph:Intro paragraph.",
      "heading2:Heading two",
      "paragraph:Body.",
    ]);
  });
});

describe("Backspace / Delete on an empty paragraph between two headings", () => {
  it("Backspace removes only the empty paragraph", async () => {
    const { view } = await edit("## Before\n\n## After\n");
    insertEmptyBefore(view, 1);
    expect(blocks(view)).toEqual(["heading2:Before", "paragraph:", "heading2:After"]);
    caret(view, 1, "start");
    expect(press(view, "Backspace")).toBe(true);
    expect(blocks(view)).toEqual(["heading2:Before", "heading2:After"]);
  });
});

const IMAGED = "## Title\n\n![](https://example.com/a.png)\n\nAfter image.\n";

describe("an image on its own line", () => {
  it("Backspace at the start of the paragraph under it removes the image whole", async () => {
    const { view } = await edit(IMAGED);
    caret(view, 2, "start");
    expect(press(view, "Backspace")).toBe(true);
    expect(blocks(view)).toEqual(["heading2:Title", "paragraph:After image."]);
  });

  it("Delete at the end of the block above it removes the image whole", async () => {
    const { view } = await edit("Before image.\n\n![](https://example.com/a.png)\n\nAfter image.\n");
    caret(view, 0, "end");
    expect(press(view, "Delete")).toBe(true);
    expect(blocks(view)).toEqual(["paragraph:Before image.", "paragraph:After image."]);
  });

  it("an image inside a line of text is not an image line: Backspace joins as usual", async () => {
    const { view } = await edit("text ![](https://example.com/a.png) text\n\nNext.\n");
    caret(view, 1, "start");
    press(view, "Backspace");
    expect(blocks(view)).toEqual(["paragraph:text [img] textNext."]);
  });
});

describe("an image inside a line of text (AC2)", () => {
  it("stays one paragraph with an inline image after opening and saving", async () => {
    const markdown = "текст ![](a.png) текст\n";
    const { editor, view } = await edit(markdown);
    expect(blocks(view)).toEqual(["paragraph:текст [img] текст"]);
    expect(editor.action(getMarkdown())).toBe(markdown);

    const paragraphs = view.dom.querySelectorAll(":scope > p");
    expect(paragraphs).toHaveLength(1);
    const img = view.dom.querySelector("img:not(.ProseMirror-separator)");
    expect(img?.parentElement).toBe(paragraphs[0]);
  });
});
