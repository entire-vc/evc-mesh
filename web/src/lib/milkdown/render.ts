import { Editor, parserCtx, rootCtx, schemaCtx } from "@milkdown/kit/core";
import { DOMSerializer } from "@milkdown/kit/prose/model";
import { commonmark } from "@milkdown/kit/preset/commonmark";
import { gfm } from "@milkdown/kit/preset/gfm";
import {
  attachmentAwareImageSchema,
  safeLinkSchema,
} from "@/lib/milkdown/schema-overrides";

/**
 * Rendering markdown for reading, through the same schema that edits it.
 *
 * The editor (doc-editor.tsx) mounts a live ProseMirror view because it has to:
 * editing needs a view. Reading does not, and mounting one per rendered body
 * would be the wrong trade — a task page draws its description plus every
 * comment, and the activity feed draws dozens of previews at once. Each would
 * be a full EditorView with its own plugins, DOM observer and lifecycle, to
 * display text nobody can type into.
 *
 * So this parses with Milkdown's parser and serialises with ProseMirror's own
 * DOMSerializer, both taken from one headless editor created once per tab. What
 * it draws is what the editor draws, because it is the same schema object —
 * including our two overrides, which is how an attachment image and an unsafe
 * href stay handled identically in both places. That equivalence is the reason
 * this file exists instead of a third hand-written renderer, and it is pinned by
 * a test that renders the same markdown both ways and compares the DOM.
 *
 * The output is a real DocumentFragment built by the serializer, never an HTML
 * string. Nothing here concatenates markup, so there is no interpolation site
 * for content to escape through — the failure mode both deleted renderers had
 * by construction.
 */

let enginePromise: Promise<Editor> | null = null;

function createEngine(): Promise<Editor> {
  // Detached root: this editor is never shown and never focused. It exists to
  // own a parser and a schema.
  const root = document.createElement("div");
  return Editor.make()
    .config((ctx) => {
      ctx.set(rootCtx, root);
    })
    .use(commonmark)
    .use(gfm)
    // After the presets, for the same reason as in editor.ts: extendSchema
    // re-registers under the same id, so these replace the preset's link and
    // image rather than sitting beside them.
    .use(safeLinkSchema)
    .use(attachmentAwareImageSchema)
    .create();
}

/** The shared parse/serialise engine, created on first use. */
export function markdownEngine(): Promise<Editor> {
  enginePromise ??= createEngine();
  return enginePromise;
}

/**
 * Parse `markdown` and return it as DOM.
 *
 * Empty (or whitespace-only) input returns an empty fragment rather than a
 * paragraph, so a caller can tell "nothing to show" from "a blank line".
 */
export async function renderMarkdownFragment(
  markdown: string,
): Promise<DocumentFragment> {
  if (!markdown.trim()) return document.createDocumentFragment();
  const editor = await markdownEngine();
  return editor.action((ctx) => {
    const parser = ctx.get(parserCtx);
    const doc = parser(markdown);
    if (!doc) return document.createDocumentFragment();
    const out = DOMSerializer.fromSchema(ctx.get(schemaCtx)).serializeFragment(
      doc.content,
    );
    // serializeFragment is typed as returning an element OR a fragment (it
    // accepts a `document` option that changes which). Normalising here keeps
    // the DOM shape fixed: handing an element back to the caller would wrap the
    // whole body in a stray node that the stylesheet's child selectors would
    // then miss.
    if (out instanceof DocumentFragment) return out;
    const frag = document.createDocumentFragment();
    frag.append(...Array.from(out.childNodes));
    return frag;
  });
}

/** Testing seam: drop the cached engine so a test can build a fresh one. */
export function resetMarkdownEngineForTest(): void {
  enginePromise = null;
}
