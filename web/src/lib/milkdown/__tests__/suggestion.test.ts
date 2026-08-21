import { beforeEach, describe, expect, it } from "vitest";
import { Editor, editorViewCtx, serializerCtx } from "@milkdown/kit/core";
import type { EditorView } from "@milkdown/kit/prose/view";
import { TextSelection } from "@milkdown/kit/prose/state";
import { makeDocEditor } from "@/lib/milkdown/editor";
import {
  findSuggestion,
  replaceWithLink,
  replaceWithText,
} from "@/lib/milkdown/suggestion";

/**
 * The `[[` and `@` menus, at the layer where they now live.
 *
 * These assertions were previously made through the editors — one suite running
 * the same table against DescriptionEditor and MarkdownEditor, because the
 * affordance existed in the comment box only and the criterion was that all
 * three surfaces got it.
 *
 * There is now ONE surface (RichTextEditor), so that table has no rows left to
 * differ: the three editors were deleted along with the two markdown parsers
 * they carried. What remains worth asserting is what replaced the textarea
 * mechanics — deciding a trigger from a ProseMirror selection, and putting the
 * accepted suggestion back as a real link mark. That is what this file covers,
 * against a real Milkdown editor rather than a mock, so the positions it
 * computes are positions in a real document.
 *
 * The exact resulting markdown is asserted, not "something was inserted": the
 * saved body is the product, and a link that serialises wrong is invisible until
 * someone reads it back.
 */

let editor: Editor;
let view: EditorView;

async function mount(markdown: string): Promise<void> {
  const root = document.createElement("div");
  document.body.appendChild(root);
  editor = makeDocEditor({
    root,
    defaultValue: markdown,
    editable: true,
    editorClassName: "probe",
  });
  await editor.create();
  view = editor.ctx.get(editorViewCtx);
}

/** Put the caret at the end of the document's first text block. */
function caretAtEndOfFirstBlock(): void {
  const { doc } = view.state;
  const first = doc.firstChild;
  if (!first) throw new Error("empty document");
  const pos = 1 + first.content.size;
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, pos)));
}

function markdown(): string {
  return editor.action((ctx) => ctx.get(serializerCtx)(view.state.doc));
}

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("deciding which menu is open", () => {
  it("opens the document menu on [[ and reports what was typed after it", async () => {
    await mount("See [[run");
    caretAtEndOfFirstBlock();

    const found = findSuggestion(view);

    expect(found?.kind).toBe("doc");
    expect(found?.query).toBe("run");
  });

  it("opens the mention menu on @", async () => {
    await mount("ping @gar");
    caretAtEndOfFirstBlock();

    const found = findSuggestion(view);

    expect(found?.kind).toBe("mention");
    expect(found?.query).toBe("gar");
  });

  it("stays closed for ordinary prose", async () => {
    await mount("a normal sentence with no trigger in it");
    caretAtEndOfFirstBlock();

    expect(findSuggestion(view)).toBeNull();
  });

  it("closes once the writer has moved past the trigger", async () => {
    // `[[` followed by a closing bracket is not a writer still picking a
    // document. Leaving the menu open past that point is how it ends up
    // inserting where the caret no longer is.
    await mount("See [[run]] and more");
    caretAtEndOfFirstBlock();

    expect(findSuggestion(view)).toBeNull();
  });

  it("prefers [[ over @ when both could match", async () => {
    // `[[@foo` is someone typing a document title, not a mention.
    await mount("See [[@foo");
    caretAtEndOfFirstBlock();

    expect(findSuggestion(view)?.kind).toBe("doc");
  });
});

describe("accepting a suggestion", () => {
  it("replaces the trigger with a real link to the document's route", async () => {
    await mount("See [[run");
    caretAtEndOfFirstBlock();
    const found = findSuggestion(view)!;

    replaceWithLink(view, found, "Deploy runbook", "/w/acme/p/demo/docs/doc-1");

    // The whole point of the unit, asserted as the exact text the writer ends
    // up with.
    expect(markdown().trim()).toBe("See [Deploy runbook](/w/acme/p/demo/docs/doc-1)");
  });

  it("leaves the caret outside the link, so the next word is not swallowed", async () => {
    await mount("See [[run");
    caretAtEndOfFirstBlock();
    replaceWithLink(view, findSuggestion(view)!, "Runbook", "/w/acme/p/demo/docs/doc-1");

    // Typing continues after the link. Without the trailing space and the
    // cleared stored marks, this text joins the link mark and silently becomes
    // part of the link's label.
    view.dispatch(view.state.tr.insertText("next", view.state.selection.from));

    expect(markdown()).toContain("[Runbook](/w/acme/p/demo/docs/doc-1)");
    expect(markdown()).not.toContain("[Runbook next]");
  });

  it("replaces a mention trigger with plain text, not a link", async () => {
    // A mention is a bare word that the renderer links at read time. Storing it
    // as a markdown link would change what is saved and break that.
    await mount("ping @gar");
    caretAtEndOfFirstBlock();

    replaceWithText(view, findSuggestion(view)!, "@garfield");

    expect(markdown().trim()).toBe("ping @garfield");
  });

  it("does not double the space when inserting mid-sentence", async () => {
    await mount("See [[run more");
    // Caret right after "run", not at the end of the block.
    view.dispatch(
      view.state.tr.setSelection(TextSelection.create(view.state.doc, 1 + "See [[run".length)),
    );

    replaceWithLink(view, findSuggestion(view)!, "Runbook", "/w/acme/p/demo/docs/doc-1");

    expect(markdown().trim()).toBe("See [Runbook](/w/acme/p/demo/docs/doc-1) more");
  });
});
