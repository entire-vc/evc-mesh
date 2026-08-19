import { afterEach, describe, expect, it } from "vitest";
import {
  Editor,
  defaultValueCtx,
  editorViewCtx,
  remarkStringifyOptionsCtx,
  rootCtx,
} from "@milkdown/kit/core";
import { commonmark } from "@milkdown/kit/preset/commonmark";
import { getMarkdown } from "@milkdown/kit/utils";
import { MESH_STRINGIFY_OPTIONS, makeDocEditor } from "@/lib/milkdown/editor";

/**
 * Round-trip fidelity for document bodies.
 *
 * ## Why this asserts on the tree and not on the string
 *
 * A markdown editor that does not parse a construct passes it through untouched,
 * so `serialize(parse(md)) === md` is satisfied *perfectly* by an editor that
 * understands nothing — it is the one result a do-nothing implementation is
 * guaranteed to produce. A string comparison would therefore have scored the
 * worst candidate in the evaluation as flawless, and the failure mode this whole
 * unit exists to fix (tables, task lists, nested lists and footnotes silently
 * being plain text) would have shipped with a green test.
 *
 * So there are two independent obligations here and both are required:
 *
 *   1. STRUCTURE — the parsed document actually contains a table node with
 *      per-column alignment, list items carrying a boolean `checked`, lists
 *      nested inside lists, footnote definition and reference nodes. If the
 *      editor degraded any of these to a paragraph, these assertions fail even
 *      though the markdown would round-trip byte-identically.
 *
 *   2. STABILITY — parsing, serialising and re-parsing yields the same tree.
 *      This is what catches a construct that is parsed but written back wrong.
 *
 * (1) without (2) misses corruption; (2) without (1) is the trap described
 * above. Neither is worth landing alone.
 */

// Every construct the Docs editor is required to support, plus the four
// paragraph openings that a line-based parser mistakes for block syntax.
const FIXTURE = `# Heading one

A paragraph with **bold**, *italic*, \`inline code\`, a [link](https://example.com)
and an ![image](https://example.com/i.png).

| Left | Center | Right |
| :--- | :----: | ----: |
| a | b | c |
| longer cell | x | 42 |

- [ ] unchecked task
- [x] checked task

1. first
   1. nested first
   2. nested second
2. second

- outer
  - inner
    - deepest

A statement needing support[^note], and another[^second].

[^note]: The footnote body.

[^second]: A second footnote.

> A blockquote.
>
> With two paragraphs.

\`\`\`ts
const x: number = 1;
\`\`\`

\\# not a heading

\\- not a list

1\\. not an ordered list

\\> not a blockquote
`;

const openEditors: Editor[] = [];

async function open(markdown: string) {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const editor = makeDocEditor({
    root,
    defaultValue: markdown,
    editable: true,
  });
  await editor.create();
  openEditors.push(editor);

  const doc = editor.action((ctx) => ctx.get(editorViewCtx).state.doc.toJSON());
  const serialized = editor.action(getMarkdown());
  return { doc: doc as TreeNode, serialized };
}

afterEach(async () => {
  await Promise.all(openEditors.splice(0).map((e) => e.destroy()));
  document.body.innerHTML = "";
});

// ---------------------------------------------------------------------------
// Tree helpers — the assertions read the ProseMirror JSON, never the markdown.
// ---------------------------------------------------------------------------

interface TreeNode {
  type: string;
  attrs?: Record<string, unknown>;
  content?: TreeNode[];
  text?: string;
  marks?: { type: string; attrs?: Record<string, unknown> }[];
}

function walk(node: TreeNode, visit: (n: TreeNode) => void) {
  visit(node);
  node.content?.forEach((child) => walk(child, visit));
}

function findAll(root: TreeNode, type: string): TreeNode[] {
  const found: TreeNode[] = [];
  walk(root, (n) => {
    if (n.type === type) found.push(n);
  });
  return found;
}

/** Deepest nesting of `type` within itself, e.g. 3 for a three-level list. */
function maxNesting(root: TreeNode, type: string): number {
  function depthOf(node: TreeNode, current: number): number {
    const next = node.type === type ? current + 1 : current;
    let best = next;
    for (const child of node.content ?? []) {
      best = Math.max(best, depthOf(child, next));
    }
    return best;
  }
  return depthOf(root, 0);
}

function textOf(node: TreeNode): string {
  let out = "";
  walk(node, (n) => {
    if (typeof n.text === "string") out += n.text;
  });
  return out;
}

describe("document round-trip fidelity", () => {
  // -------------------------------------------------------------------------
  // 1. STRUCTURE
  // -------------------------------------------------------------------------
  describe("the constructs are really parsed, not passed through as text", () => {
    it("parses a table, with each column's alignment on the cells", async () => {
      const { doc } = await open(FIXTURE);

      const tables = findAll(doc, "table");
      expect(tables).toHaveLength(1);

      const headers = findAll(doc, "table_header");
      expect(headers.map((h) => h.attrs?.alignment)).toEqual([
        "left",
        "center",
        "right",
      ]);

      // Body cells carry the alignment too — losing it there is how a table
      // renders left-aligned no matter what the source said.
      const bodyRows = findAll(doc, "table_row");
      expect(bodyRows).toHaveLength(2);
      expect(findAll(bodyRows[0]!, "table_cell").map((c) => c.attrs?.alignment)).toEqual([
        "left",
        "center",
        "right",
      ]);
    });

    it("parses task checkboxes as a boolean attribute, not as literal brackets", async () => {
      const { doc } = await open(FIXTURE);

      const checked = findAll(doc, "list_item").filter(
        (li) => li.attrs?.checked !== null && li.attrs?.checked !== undefined,
      );
      expect(checked.map((li) => li.attrs?.checked)).toEqual([false, true]);

      // The brackets must be gone from the text: if they survive, the "task
      // list" is a bullet list whose items happen to start with "[ ]".
      expect(textOf(checked[0]!)).toBe("unchecked task");
      expect(textOf(checked[1]!)).toBe("checked task");
    });

    it("parses nested ordered and unordered lists as nested nodes", async () => {
      const { doc } = await open(FIXTURE);

      expect(maxNesting(doc, "ordered_list")).toBe(2);
      expect(maxNesting(doc, "bullet_list")).toBe(3);
    });

    it("parses footnotes into definition and reference nodes", async () => {
      const { doc } = await open(FIXTURE);

      const definitions = findAll(doc, "footnote_definition");
      const references = findAll(doc, "footnote_reference");

      expect(definitions.map((d) => d.attrs?.label)).toEqual(["note", "second"]);
      expect(references).toHaveLength(2);
      expect(textOf(definitions[0]!)).toContain("The footnote body.");
    });

    it("parses blockquotes and fenced code, keeping the language", async () => {
      const { doc } = await open(FIXTURE);

      const quotes = findAll(doc, "blockquote");
      expect(quotes).toHaveLength(1);
      expect(findAll(quotes[0]!, "paragraph")).toHaveLength(2);

      const code = findAll(doc, "code_block");
      expect(code).toHaveLength(1);
      expect(code[0]?.attrs?.language).toBe("ts");
      expect(textOf(code[0]!)).toBe("const x: number = 1;");
    });

    it("keeps a paragraph a paragraph when it opens with #, -, 1. or >", async () => {
      const { doc } = await open(FIXTURE);

      const paragraphTexts = findAll(doc, "paragraph").map(textOf);
      for (const literal of [
        "# not a heading",
        "- not a list",
        "1. not an ordered list",
        "> not a blockquote",
      ]) {
        expect(paragraphTexts).toContain(literal);
      }

      // And the block-level nodes those characters would have produced are not
      // there: exactly one heading (the real one) and no stray list or quote.
      expect(findAll(doc, "heading")).toHaveLength(1);
      expect(findAll(doc, "blockquote")).toHaveLength(1);
    });

    it("keeps inline marks", async () => {
      const { doc } = await open(FIXTURE);
      const markTypes = new Set<string>();
      walk(doc, (n) => n.marks?.forEach((m) => markTypes.add(m.type)));

      expect(markTypes).toContain("strong");
      expect(markTypes).toContain("emphasis");
      expect(markTypes).toContain("inlineCode");
      expect(markTypes).toContain("link");
      expect(findAll(doc, "image")).toHaveLength(1);
    });
  });

  // -------------------------------------------------------------------------
  // 2. STABILITY
  // -------------------------------------------------------------------------
  describe("parse -> serialise -> parse is stable", () => {
    it("produces an identical document tree on the second parse", async () => {
      const first = await open(FIXTURE);
      const second = await open(first.serialized);

      expect(second.doc).toEqual(first.doc);
    });

    it("is idempotent at the markdown level once normalised", async () => {
      // Only checked AFTER the first normalising pass. The source a human wrote
      // is not expected to survive byte-for-byte — `* item` becomes `- item`,
      // for one — but the editor must reach a fixed point immediately rather
      // than rewriting the file a little more on every save.
      const first = await open(FIXTURE);
      const second = await open(first.serialized);

      expect(second.serialized).toBe(first.serialized);
    });
  });

  // -------------------------------------------------------------------------
  // 3. Negative control — proof the structural assertions above have teeth
  // -------------------------------------------------------------------------
  describe("a parser that does not understand the constructs is caught", () => {
    /**
     * The trap, made executable.
     *
     * This builds the *same* editor without the GFM preset — the stand-in for a
     * library that does not parse tables, task lists or footnotes. Its markdown
     * round-trip is flawless, because untouched text always is. Its document
     * tree is empty of every construct users asked for.
     *
     * If someone later loosens the structural assertions into a string
     * comparison, this test is what says why they must not.
     */
    async function openCommonmarkOnly(markdown: string) {
      const root = document.createElement("div");
      document.body.appendChild(root);
      const editor = Editor.make()
        .config((ctx) => {
          ctx.set(rootCtx, root);
          ctx.set(defaultValueCtx, markdown);
          ctx.set(remarkStringifyOptionsCtx, { ...MESH_STRINGIFY_OPTIONS });
        })
        .use(commonmark);
      await editor.create();
      openEditors.push(editor);
      return {
        doc: editor.action((ctx) =>
          ctx.get(editorViewCtx).state.doc.toJSON(),
        ) as TreeNode,
        serialized: editor.action(getMarkdown()),
      };
    }

    const TABLE_MD = "| Left | Right |\n| :--- | ----: |\n| a | b |\n";

    it("round-trips the markdown byte-identically while parsing nothing", async () => {
      const { doc, serialized } = await openCommonmarkOnly(TABLE_MD);

      // A string-only test scores this as perfect.
      expect(serialized).toBe(TABLE_MD);

      // The tree says what actually happened: no table, just text.
      expect(findAll(doc, "table")).toHaveLength(0);
      expect(findAll(doc, "table_header")).toHaveLength(0);
      expect(textOf(doc)).toContain("| Left | Right |");
    });

    it("shows the same blindness for task lists and footnotes", async () => {
      const { doc } = await openCommonmarkOnly(
        "- [ ] todo\n\nCite[^a].\n\n[^a]: Note.\n",
      );

      const withChecked = findAll(doc, "list_item").filter(
        (li) => li.attrs?.checked !== null && li.attrs?.checked !== undefined,
      );
      expect(withChecked).toHaveLength(0);
      expect(findAll(doc, "footnote_definition")).toHaveLength(0);

      // The brackets survive as literal text — the tell that "- [ ] todo" was
      // read as an ordinary bullet.
      expect(textOf(doc)).toContain("[ ] todo");
    });
  });

  // -------------------------------------------------------------------------
  // 4. The serialisation options that keep diffs honest
  // -------------------------------------------------------------------------
  describe("serialisation matches what humans and Obsidian write", () => {
    it("writes unordered lists with '-', not remark's default '*'", async () => {
      const { serialized } = await open("- one\n- two\n");

      expect(serialized).toContain("- one");
      expect(serialized).not.toContain("* one");
    });

    it("does not churn a document that is already in house style", async () => {
      // The nastiest regression this unit can cause is a no-op edit rewriting
      // every list in every document, so it gets its own assertion.
      const inHouseStyle = [
        "# Title",
        "",
        "- one",
        "- two",
        "  - nested",
        "",
        "1. first",
        "2. second",
        "",
        "- [ ] todo",
        "- [x] done",
        "",
      ].join("\n");

      const { serialized } = await open(inHouseStyle);
      expect(serialized).toBe(inHouseStyle);
    });
  });
});
