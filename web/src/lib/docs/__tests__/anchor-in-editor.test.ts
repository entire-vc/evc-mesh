import { afterEach, describe, expect, it } from "vitest";
import { type Editor, editorViewCtx } from "@milkdown/kit/core";
import { makeDocEditor } from "@/lib/milkdown/editor";
import { buildBlocks, makeAnchor, resolveAnchor } from "@/lib/docs/anchor";

/**
 * The one claim the pure anchor tests cannot make.
 *
 * `lib/docs/anchor.ts` works on a list of block texts, and the viewer turns an
 * index in that list into a DOM element with `view.dom.children[index]`. That
 * step rests on ProseMirror rendering exactly one child element per top-level
 * node, in order — which is true, and is also precisely the kind of assumption
 * that fails silently: an off-by-one would scroll the reader to the paragraph
 * *next to* the linked one, which looks like a working link.
 *
 * So this asserts the correspondence against a real editor, over a document
 * containing every block construct Docs supports, and then walks the whole
 * make-anchor → edit → resolve → element path the same way the viewer does.
 */

const FIXTURE = `# Deploy runbook

The migration is applied before the image swap, never after.

| Step | Owner |
| :--- | ----: |
| migrate | CI |

- [ ] check the gate
- [x] read the log

> Rollback is: revert the image first.

\`\`\`bash
gh-merge 42 entire-vc/evc-mesh
\`\`\`

Contact the on-call lead if the gate refuses.
`;

const openEditors: Editor[] = [];

async function open(markdown: string) {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const editor = makeDocEditor({
    root,
    defaultValue: markdown,
    editable: false,
    editorClassName: "mesh-doc-prose",
  });
  await editor.create();
  openEditors.push(editor);
  return editor;
}

function blockTexts(editor: Editor): string[] {
  return editor.action((ctx) => {
    const texts: string[] = [];
    ctx.get(editorViewCtx).state.doc.forEach((node) => {
      texts.push(node.textContent);
    });
    return texts;
  });
}

function elementAt(editor: Editor, index: number): Element | null {
  return editor.action(
    (ctx) => ctx.get(editorViewCtx).dom.children[index] ?? null,
  );
}

function normalize(raw: string): string {
  return raw.replace(/\s+/g, " ").trim();
}

afterEach(async () => {
  await Promise.all(openEditors.splice(0).map((e) => e.destroy()));
  document.body.innerHTML = "";
});

describe("block index ↔ rendered element", () => {
  it("names the same block on both sides, for every construct", async () => {
    const editor = await open(FIXTURE);
    const texts = blockTexts(editor);

    expect(texts.length).toBeGreaterThan(5);

    // Every construct, no exemptions: heading, paragraph, table, task list,
    // blockquote, fenced code. Measured rather than assumed — all six render
    // their text into the element, so there is nothing here to excuse.
    for (let i = 0; i < texts.length; i += 1) {
      const el = elementAt(editor, i);
      expect(el, `no element for block ${i}`).not.toBeNull();
      expect(normalize(el!.textContent ?? ""), `block ${i}`).toBe(
        normalize(texts[i]!),
      );
    }

    // The load-bearing half: same count, so no index can be off by one.
    const childCount = editor.action(
      (ctx) => ctx.get(editorViewCtx).dom.children.length,
    );
    expect(childCount).toBe(texts.length);
  });

  it("an anchor made from a paragraph resolves back to that element", async () => {
    const editor = await open(FIXTURE);
    const texts = blockTexts(editor);
    const index = texts.findIndex((t) => t.includes("on-call lead"));
    expect(index).toBeGreaterThan(-1);

    const anchor = makeAnchor(buildBlocks(texts), index);
    expect(anchor).not.toBeNull();

    const match = resolveAnchor(buildBlocks(texts), anchor!);
    expect(match).toEqual({ status: "exact", index });
    expect(normalize(elementAt(editor, match.index!)!.textContent ?? "")).toContain(
      "on-call lead",
    );
  });

  it("still resolves to that element after a paragraph is inserted above it", async () => {
    const before = await open(FIXTURE);
    const originalTexts = blockTexts(before);
    const index = originalTexts.findIndex((t) => t.includes("on-call lead"));
    const anchor = makeAnchor(buildBlocks(originalTexts), index)!;

    // A real edit above, through the same parser the viewer uses.
    const after = await open(
      FIXTURE.replace(
        "# Deploy runbook\n",
        "# Deploy runbook\n\nA paragraph someone added at the top.\n",
      ),
    );
    const editedTexts = blockTexts(after);
    const match = resolveAnchor(buildBlocks(editedTexts), anchor);

    expect(match.status).toBe("moved");
    expect(match.index).not.toBe(index);
    expect(normalize(elementAt(after, match.index!)!.textContent ?? "")).toContain(
      "on-call lead",
    );
  });

  it("reports the paragraph as edited, at its own element, when its text changed", async () => {
    const before = await open(FIXTURE);
    const anchor = makeAnchor(
      buildBlocks(blockTexts(before)),
      blockTexts(before).findIndex((t) => t.includes("on-call lead")),
    )!;

    const after = await open(
      FIXTURE.replace(
        "Contact the on-call lead if the gate refuses.",
        "Page the on-call lead only after the gate has been read.",
      ),
    );
    const match = resolveAnchor(buildBlocks(blockTexts(after)), anchor);

    expect(match.status).toBe("edited");
    const text = normalize(elementAt(after, match.index!)!.textContent ?? "");
    expect(text).toContain("on-call lead");
    // And it is honestly not the linked text: the status is the only thing
    // telling the reader that, which is why the banner is not optional.
    expect(text).not.toBe(anchor.exact);
  });
});
