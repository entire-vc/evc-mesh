import { afterEach, describe, expect, it } from "vitest";
import { Editor, editorViewCtx } from "@milkdown/kit/core";
import { TextSelection } from "@milkdown/kit/prose/state";
import type { EditorView } from "@milkdown/kit/prose/view";
import { getMarkdown } from "@milkdown/kit/utils";
import { makeDocEditor } from "@/lib/milkdown/editor";

/**
 * Markdown typed or pasted into the Docs editor must become structure.
 *
 * Partner report: pasting a `.md` file's text left `### Title`,
 * `- item` and `| a | b |` on screen as raw characters, and typing `### ` did
 * not make a heading.
 *
 * The paste tests go through ProseMirror's own DOM `paste` handler — a real
 * `paste` event on the contenteditable with a clipboard carrying the flavours a
 * browser puts there — so what runs is the path a user's Cmd+V takes, not a
 * helper that only this test calls. Assertions read the ProseMirror tree: a
 * paste that left the text raw has the same characters on screen and would pass
 * any string comparison.
 */

const openEditors: Editor[] = [];

afterEach(async () => {
  await Promise.all(openEditors.splice(0).map((e) => e.destroy()));
  document.body.innerHTML = "";
});

interface TreeNode {
  type: string;
  attrs?: Record<string, unknown>;
  content?: TreeNode[];
  text?: string;
  marks?: { type: string; attrs?: Record<string, unknown> }[];
}

async function open(markdown = "") {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const editor = makeDocEditor({ root, defaultValue: markdown, editable: true });
  await editor.create();
  openEditors.push(editor);
  const view = editor.action((ctx) => ctx.get(editorViewCtx));
  return { editor, view };
}

function tree(view: EditorView): TreeNode {
  return view.state.doc.toJSON() as TreeNode;
}

function walk(node: TreeNode, visit: (n: TreeNode) => void) {
  visit(node);
  for (const child of node.content ?? []) walk(child, visit);
}

function nodesOf(root: TreeNode, type: string): TreeNode[] {
  const out: TreeNode[] = [];
  walk(root, (n) => {
    if (n.type === type) out.push(n);
  });
  return out;
}

function textOf(node: TreeNode): string {
  let s = "";
  walk(node, (n) => {
    if (n.text) s += n.text;
  });
  return s;
}

/** The only element of `list`; fails the test when there is not exactly one. */
function one(list: TreeNode[]): TreeNode {
  expect(list).toHaveLength(1);
  return list[0] as TreeNode;
}

/** The `i`th top-level block; fails the test when it is missing. */
function child(doc: TreeNode, i: number): TreeNode {
  const node = doc.content?.[i];
  expect(node, `block ${i}`).toBeDefined();
  return node as TreeNode;
}

/** Puts the cursor at `pos` (default: the end of the document). */
function placeCursor(view: EditorView, pos?: number) {
  const at = pos ?? TextSelection.atEnd(view.state.doc).from;
  view.dispatch(
    view.state.tr.setSelection(TextSelection.create(view.state.doc, at)),
  );
}

/**
 * Fires a real `paste` event on the editor DOM. ProseMirror's paste handler
 * reads `clipboardData` from it exactly as it does for Cmd+V.
 */
function paste(view: EditorView, flavours: Record<string, string>) {
  const data = {
    types: Object.keys(flavours),
    files: [] as File[],
    getData: (type: string) => flavours[type] ?? "",
  };
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", { value: data });
  view.dom.dispatchEvent(event);
}

/** What ProseMirror does with one typed character. */
function type(view: EditorView, text: string) {
  for (const ch of text) {
    const { from, to } = view.state.selection;
    const handled = view.someProp("handleTextInput", (f) =>
      f(view, from, to, ch, () => view.state.tr.insertText(ch, from, to)),
    );
    if (!handled) view.dispatch(view.state.tr.insertText(ch, from, to));
  }
}

function pressEnter(view: EditorView) {
  const event = new KeyboardEvent("keydown", {
    key: "Enter",
    bubbles: true,
    cancelable: true,
  });
  const handled = view.someProp("handleKeyDown", (f) => f(view, event));
  expect(handled, "Enter must be handled by some keymap").toBe(true);
}

// A `.md` file as it sits on disk: every block the partner listed.
const MD_FILE = `# Title one

## Title two

### Title three

A paragraph with **bold** and a [link](https://example.com/page).

- bullet one
- bullet two

1. first
2. second

> A quoted line.

\`\`\`ts
const x = 1;
\`\`\`

| a | b |
| - | - |
| 1 | 2 |

![diagram](https://example.com/diagram.png)
`;

describe("pasting markdown text (AC1)", () => {
  it("turns a pasted .md file into headings, lists, quote, code, table, image and link", async () => {
    const { view } = await open("");
    placeCursor(view);
    paste(view, { "text/plain": MD_FILE });

    const doc = tree(view);
    const headings = nodesOf(doc, "heading").map((h) => [
      h.attrs?.level,
      textOf(h),
    ]);
    expect(headings).toEqual([
      [1, "Title one"],
      [2, "Title two"],
      [3, "Title three"],
    ]);

    const bullets = nodesOf(doc, "bullet_list");
    expect(bullets).toHaveLength(1);
    expect(nodesOf(one(bullets), "list_item").map(textOf)).toEqual([
      "bullet one",
      "bullet two",
    ]);

    const ordered = nodesOf(doc, "ordered_list");
    expect(ordered).toHaveLength(1);
    expect(nodesOf(one(ordered), "list_item").map(textOf)).toEqual([
      "first",
      "second",
    ]);

    const quotes = nodesOf(doc, "blockquote");
    expect(quotes.map(textOf)).toEqual(["A quoted line."]);

    const code = nodesOf(doc, "code_block");
    expect(code).toHaveLength(1);
    expect(one(code).attrs?.language).toBe("ts");
    expect(textOf(one(code))).toBe("const x = 1;");

    const tables = nodesOf(doc, "table");
    expect(tables).toHaveLength(1);
    expect(nodesOf(one(tables), "table_header").map(textOf)).toEqual(["a", "b"]);
    expect(nodesOf(one(tables), "table_cell").map(textOf)).toEqual(["1", "2"]);

    const images = nodesOf(doc, "image");
    expect(images).toHaveLength(1);
    expect(one(images).attrs?.src).toBe("https://example.com/diagram.png");
    expect(one(images).attrs?.alt).toBe("diagram");

    let link: TreeNode | undefined;
    walk(doc, (n) => {
      if (n.marks?.some((m) => m.type === "link")) link = n;
    });
    expect(link?.text).toBe("link");
    expect(link?.marks?.find((m) => m.type === "link")?.attrs?.href).toBe(
      "https://example.com/page",
    );

    // Nothing of the source syntax is left behind as text.
    expect(textOf(doc)).not.toMatch(/###|^- |\| a \||!\[/m);
  });

  it("parses markdown copied from a code editor that also offers plain monospace HTML", async () => {
    // What VS Code puts on the clipboard: text/plain plus a styled <div>
    // rendering of the same characters, and its own editor-data flavour.
    const html =
      '<meta charset="utf-8"><div style="color: #d4d4d4;background-color: #1e1e1e;' +
      "font-family: Menlo, Monaco, 'Courier New', monospace;white-space: pre;\">" +
      "<div><span>### Heading</span></div><div><span>- item</span></div></div>";
    const { view } = await open("");
    placeCursor(view);
    paste(view, {
      "text/plain": "### Heading\n\n- item\n",
      "text/html": html,
      "vscode-editor-data": JSON.stringify({ mode: "markdown" }),
    });

    const doc = tree(view);
    expect(nodesOf(doc, "heading").map((h) => [h.attrs?.level, textOf(h)])).toEqual([
      [3, "Heading"],
    ]);
    expect(nodesOf(doc, "list_item").map(textOf)).toEqual(["item"]);
  });

  it("pastes a single line of markdown inline, into the paragraph the cursor is in", async () => {
    const { view } = await open("Before after");
    placeCursor(view, 1 + "Before ".length);
    paste(view, { "text/plain": "**bold** " });

    const doc = tree(view);
    expect(doc.content).toHaveLength(1);
    expect(child(doc, 0).type).toBe("paragraph");
    expect(textOf(doc)).toBe("Before bold after");
    const bold = child(doc, 0).content?.find((n) =>
      n.marks?.some((m) => m.type === "strong"),
    );
    expect(bold?.text).toBe("bold");
  });

  it("keeps markdown pasted inside a code block as literal text", async () => {
    const { view } = await open("```\nx\n```\n");
    placeCursor(view, 2); // inside the code block, after "x"
    paste(view, { "text/plain": "### not a heading\n- not a list" });

    const doc = tree(view);
    expect(nodesOf(doc, "heading")).toHaveLength(0);
    expect(nodesOf(doc, "bullet_list")).toHaveLength(0);
    const code = nodesOf(doc, "code_block");
    expect(code).toHaveLength(1);
    expect(textOf(one(code))).toContain("### not a heading\n- not a list");
  });

  it("does not parse code copied from a code editor in another language as markdown", async () => {
    const { view } = await open("");
    placeCursor(view);
    paste(view, {
      "text/plain": "# a python comment\nx = 1",
      "text/html":
        '<div style="font-family: Menlo, monospace;white-space: pre;"><div><span># a python comment</span></div><div><span>x = 1</span></div></div>',
      "vscode-editor-data": JSON.stringify({ mode: "python" }),
    });
    expect(nodesOf(tree(view), "heading")).toHaveLength(0);
  });
});

describe("pasting rendered HTML is unchanged (AC3)", () => {
  // A fragment as a browser or Google Docs puts it on the clipboard: the
  // text/plain flavour is the *rendered* text, so it carries no markdown at all.
  const HTML =
    "<h2>Rendered heading</h2><p>Text with <strong>bold</strong> and " +
    '<a href="https://example.com/x">a link</a>.</p>' +
    "<ul><li>one</li><li>two</li></ul>" +
    "<blockquote><p>quoted</p></blockquote>" +
    "<table><tr><th>h</th></tr><tr><td>c</td></tr></table>";
  const PLAIN = "Rendered heading\nText with bold and a link.\none\ntwo\nquoted\nh\nc";

  it("builds the same nodes from text/html as before the markdown paste plugin", async () => {
    const { view } = await open("");
    placeCursor(view);
    paste(view, { "text/plain": PLAIN, "text/html": HTML });
    const doc = tree(view);

    expect(nodesOf(doc, "heading").map((h) => [h.attrs?.level, textOf(h)])).toEqual([
      [2, "Rendered heading"],
    ]);
    expect(nodesOf(doc, "list_item").map(textOf)).toEqual(["one", "two"]);
    expect(nodesOf(doc, "blockquote").map(textOf)).toEqual(["quoted"]);
    expect(nodesOf(doc, "table")).toHaveLength(1);
    let href: unknown;
    walk(doc, (n) => {
      const link = n.marks?.find((m) => m.type === "link");
      if (link) href = link.attrs?.href;
    });
    expect(href).toBe("https://example.com/x");
  });

  it("produces exactly the tree origin/main produced for the same paste", async () => {
    // Recorded on origin/main (cc71f04df), before the markdown paste plugin
    // existed: an HTML paste must keep taking ProseMirror's own path.
    const BEFORE = {"type": "doc", "content": [{"type": "heading", "attrs": {"id": "rendered-heading", "level": 2}, "content": [{"type": "text", "text": "Rendered heading"}]}, {"type": "paragraph", "content": [{"type": "text", "text": "Text with "}, {"type": "text", "marks": [{"type": "strong", "attrs": {"marker": "*"}}], "text": "bold"}, {"type": "text", "text": " and "}, {"type": "text", "marks": [{"type": "link", "attrs": {"href": "https://example.com/x", "title": null}}], "text": "a link"}, {"type": "text", "text": "."}]}, {"type": "bullet_list", "attrs": {"spread": false}, "content": [{"type": "list_item", "attrs": {"label": "•", "listType": "bullet", "spread": false, "checked": null}, "content": [{"type": "paragraph", "content": [{"type": "text", "text": "one"}]}]}, {"type": "list_item", "attrs": {"label": "•", "listType": "bullet", "spread": false, "checked": null}, "content": [{"type": "paragraph", "content": [{"type": "text", "text": "two"}]}]}]}, {"type": "blockquote", "content": [{"type": "paragraph", "content": [{"type": "text", "text": "quoted"}]}]}, {"type": "table", "content": [{"type": "table_header_row", "content": [{"type": "table_header", "attrs": {"colspan": 1, "rowspan": 1, "colwidth": null, "alignment": "left"}, "content": [{"type": "paragraph", "content": [{"type": "text", "text": "h"}]}]}]}, {"type": "table_row", "content": [{"type": "table_cell", "attrs": {"colspan": 1, "rowspan": 1, "colwidth": null, "alignment": "left"}, "content": [{"type": "paragraph", "content": [{"type": "text", "text": "c"}]}]}]}]}]};
    const { view } = await open("");
    placeCursor(view);
    paste(view, { "text/plain": PLAIN, "text/html": HTML });
    expect(tree(view)).toEqual(BEFORE);
  });

  it("leaves markdown-looking characters in ordinary rendered HTML alone", async () => {
    // Copied from a web page: the asterisks are the page's text, not syntax.
    const { view } = await open("");
    placeCursor(view);
    paste(view, { "text/plain": "2 * 3 * 4", "text/html": "<p>2 * 3 * 4</p>" });
    const doc = tree(view);
    expect(textOf(doc)).toBe("2 * 3 * 4");
    walk(doc, (n) => expect(n.marks ?? []).toEqual([]));
  });
});

describe("markdown input rules while typing (AC2)", () => {
  it.each([1, 2, 3, 4, 5, 6])("'%s × #' + space makes a heading of that level", async (level) => {
    const { view } = await open("");
    placeCursor(view);
    type(view, `${"#".repeat(level)} Title`);
    const doc = tree(view);
    expect(child(doc, 0).type).toBe("heading");
    expect(child(doc, 0).attrs?.level).toBe(level);
    expect(textOf(doc)).toBe("Title");
  });

  it("'### ' typed on a new line after text makes an H3 there", async () => {
    const { view } = await open("First paragraph");
    placeCursor(view);
    pressEnter(view);
    type(view, "### Third");
    const doc = tree(view);
    expect(doc.content?.map((n) => n.type)).toEqual(["paragraph", "heading"]);
    expect(child(doc, 1).attrs?.level).toBe(3);
    expect(textOf(child(doc, 1))).toBe("Third");
  });

  it.each(["- ", "* "])("'%s' starts a bullet list", async (marker) => {
    const { view } = await open("");
    placeCursor(view);
    type(view, `${marker}item`);
    const doc = tree(view);
    expect(child(doc, 0).type).toBe("bullet_list");
    expect(textOf(doc)).toBe("item");
  });

  it("'1. ' starts an ordered list", async () => {
    const { view } = await open("");
    placeCursor(view);
    type(view, "1. item");
    const doc = tree(view);
    expect(child(doc, 0).type).toBe("ordered_list");
    expect(textOf(doc)).toBe("item");
  });

  it("'> ' starts a blockquote", async () => {
    const { view } = await open("");
    placeCursor(view);
    type(view, "> quoted");
    const doc = tree(view);
    expect(child(doc, 0).type).toBe("blockquote");
    expect(textOf(doc)).toBe("quoted");
  });

  it("'```' + space starts a code block", async () => {
    const { view } = await open("");
    placeCursor(view);
    type(view, "``` ");
    expect(child(tree(view), 0).type).toBe("code_block");
  });

  it("'```ts' + Enter starts a code block with that language", async () => {
    const { view } = await open("");
    placeCursor(view);
    type(view, "```ts");
    pressEnter(view);
    const doc = tree(view);
    expect(child(doc, 0).type).toBe("code_block");
    expect(child(doc, 0).attrs?.language).toBe("ts");
    expect(textOf(doc)).toBe("");
  });

  it("'[ ] ' at the start of a bullet item makes a task item", async () => {
    const { view } = await open("");
    placeCursor(view);
    type(view, "- [ ] todo");
    const items = nodesOf(tree(view), "list_item");
    expect(items).toHaveLength(1);
    expect(one(items).attrs?.checked).toBe(false);
    expect(textOf(one(items))).toBe("todo");
  });

  it("serialises what was typed back to the same markdown", async () => {
    const { editor, view } = await open("");
    placeCursor(view);
    type(view, "### Title");
    expect(editor.action(getMarkdown()).trim()).toBe("### Title");
  });
});
