import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Editor, editorViewCtx } from "@milkdown/kit/core";
import type { EditorView } from "@milkdown/kit/prose/view";
import { TextSelection } from "@milkdown/kit/prose/state";
import { makeDocEditor } from "@/lib/milkdown/editor";
import { CARET_ATTRIBUTE, CARET_PROBE_FLAG } from "@/lib/milkdown/caret-attribute";

/**
 * The editor reports its own caret on its root: the browser test
 * of the block-edge keys waits on it instead of a fixed pause.
 */

const open: Editor[] = [];
const flags = globalThis as Record<string, unknown>;

beforeEach(() => {
  flags[CARET_PROBE_FLAG] = true;
});

async function make(markdown: string, editable: boolean): Promise<EditorView> {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const editor = makeDocEditor({ root, defaultValue: markdown, editable });
  await editor.create();
  open.push(editor);
  return editor.action((ctx) => ctx.get(editorViewCtx));
}

afterEach(async () => {
  delete flags[CARET_PROBE_FLAG];
  await Promise.all(open.splice(0).map((e) => e.destroy()));
  document.body.innerHTML = "";
});

function select(view: EditorView, from: number, to = from): void {
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, from, to)));
}

describe("caret attribute", () => {
  const DOC = "## Title\n\nIntro paragraph.\n\n## Heading two\n";

  it("names the top-level block and the offset inside it", async () => {
    const view = await make(DOC, true);
    // "## Title" is 7 positions (open, 5 chars, close): the next block opens at 7.
    select(view, 8);
    expect(view.dom.getAttribute(CARET_ATTRIBUTE)).toBe("1:0");
    select(view, 8 + "Intro paragraph.".length);
    expect(view.dom.getAttribute(CARET_ATTRIBUTE)).toBe("1:16");
    select(view, 3);
    expect(view.dom.getAttribute(CARET_ATTRIBUTE)).toBe("0:2");
  });

  it("follows typing", async () => {
    const view = await make(DOC, true);
    select(view, 1);
    view.dispatch(view.state.tr.insertText("xy"));
    expect(view.dom.getAttribute(CARET_ATTRIBUTE)).toBe("0:2");
  });

  it("is absent for a range selection, and comes back with a caret", async () => {
    const view = await make(DOC, true);
    select(view, 1, 4);
    expect(view.dom.hasAttribute(CARET_ATTRIBUTE)).toBe(false);
    select(view, 4);
    expect(view.dom.getAttribute(CARET_ATTRIBUTE)).toBe("0:3");
  });

  it("is not on the viewer", async () => {
    const view = await make(DOC, false);
    expect(view.dom.hasAttribute(CARET_ATTRIBUTE)).toBe(false);
  });

  it("writes nothing unless the page asked for it", async () => {
    delete flags[CARET_PROBE_FLAG];
    const view = await make(DOC, true);
    select(view, 8);
    view.dispatch(view.state.tr.insertText("x"));
    expect(view.dom.hasAttribute(CARET_ATTRIBUTE)).toBe(false);
  });
});
