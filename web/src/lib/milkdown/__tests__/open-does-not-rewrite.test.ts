import { afterEach, describe, expect, it } from "vitest";
import { Editor, editorViewCtx } from "@milkdown/kit/core";
import { makeDocEditor } from "@/lib/milkdown/editor";

/**
 * PROBE (D5 #4b9ffc9b): does merely OPENING a document emit a changed body?
 *
 * docs.tsx wires `onChange={setDraft}` and autosaves whenever the draft differs
 * from what the server last confirmed. So if the editor re-serialises on load
 * and reports markdown that differs from what it was given, every document is
 * rewritten the first time someone opens it in edit mode — a diff nobody asked
 * for, on content nobody touched. That is invisible in a round-trip test that
 * only compares parse→serialise once.
 */

const openEditors: Editor[] = [];

/**
 * The listener plugin debounces its callback by 200ms
 * (`@milkdown/plugin-listener/lib/index.js`). Waiting less than that makes
 * "nothing was emitted" true of every editor, working or not — the assertion
 * would be vacuous. This margin is what gives the empty result meaning.
 */
const DEBOUNCE_WAIT = 400;

/** Opens `markdown` and returns every onChange the editor emitted by itself. */
async function openAndWatch(markdown: string) {
  const root = document.createElement("div");
  document.body.appendChild(root);
  const emitted: string[] = [];
  const editor = makeDocEditor({
    root,
    defaultValue: markdown,
    editable: true,
    onMarkdown: (md) => emitted.push(md),
  });
  await editor.create();
  openEditors.push(editor);
  await new Promise((r) => setTimeout(r, DEBOUNCE_WAIT));
  return emitted;
}

afterEach(async () => {
  await Promise.all(openEditors.splice(0).map((e) => e.destroy()));
  document.body.innerHTML = "";
});

const CASES: Record<string, string> = {
  "dash bullets (what we write)": "- one\n- two\n",
  "star bullets (what another tool writes)": "* one\n* two\n",
  "table with alignment": "| A | B |\n| :- | -: |\n| 1 | 2 |\n",
  "task list": "- [ ] todo\n- [x] done\n",
  "nested list": "- outer\n  - inner\n",
  "setext heading": "Title\n=====\n",
  "underscore emphasis": "_em_ and __strong__\n",
};

describe("opening a document does not rewrite it", () => {
  // Without this, every assertion below is satisfied by an editor that reports
  // nothing at all — which would mean edits are never saved. This pins the
  // other half: the channel the assertions rely on being silent is live.
  it("POSITIVE CONTROL: a real edit is still reported", async () => {
    const root = document.createElement("div");
    document.body.appendChild(root);
    const emitted: string[] = [];
    const editor = makeDocEditor({
      root,
      defaultValue: "hello\n",
      editable: true,
      onMarkdown: (md) => emitted.push(md),
    });
    await editor.create();
    openEditors.push(editor);
    await new Promise((r) => setTimeout(r, DEBOUNCE_WAIT));
    expect(emitted, "opening alone must be silent").toEqual([]);

    editor.action((ctx) => {
      const view = ctx.get(editorViewCtx);
      view.dispatch(view.state.tr.insertText("XYZ", 1));
    });
    await new Promise((r) => setTimeout(r, DEBOUNCE_WAIT));

    expect(emitted.length, "a typed edit must reach onChange").toBeGreaterThan(0);
    expect(emitted[emitted.length - 1]).toContain("XYZ");
  });

  for (const [name, md] of Object.entries(CASES)) {
    it(`emits no self-inflicted change for: ${name}`, async () => {
      const emitted = await openAndWatch(md);
      const drifted = emitted.filter((m) => m !== md);
      expect(
        drifted,
        `opening rewrote the body: ${JSON.stringify(drifted)}`,
      ).toEqual([]);
    });
  }
});
