import type { EditorState } from "@milkdown/kit/prose/state";
import { Plugin, PluginKey } from "@milkdown/kit/prose/state";
import { $prose } from "@milkdown/kit/utils";

/** The editor root attribute that carries where the editor's caret is. */
export const CARET_ATTRIBUTE = "data-caret";

/**
 * The page-level switch a browser test sets (before the app loads) to have
 * the editor report its caret. Off in the product: an attribute write is a DOM
 * mutation on every caret move, and the perf gate counts each one.
 */
export const CARET_PROBE_FLAG = "__meshCaretProbe";

function probeEnabled(): boolean {
  return (globalThis as Record<string, unknown>)[CARET_PROBE_FLAG] === true;
}

/**
 * Where the caret is, as the editor's own state has it: `<block>:<offset>` —
 * the index of the top-level block it is in and its offset inside the
 * textblock that holds it (an inline image counts as one). Null for a
 * selection that is not a plain caret.
 */
export function caretPosition(state: EditorState): string | null {
  const { selection } = state;
  if (!selection.empty) return null;
  const $head = selection.$head;
  if ($head.depth < 1) return null;
  return `${$head.index(0)}:${$head.parentOffset}`;
}

/**
 * Mirrors {@link caretPosition} onto the editor root.
 *
 * A browser test sets the caret through the DOM, and the editor takes it over
 * on its own `selectionchange`; a key pressed before that lands wherever the
 * editor's state still says, which is the start of the document.
 * The attribute is how the test knows the editor took the caret, without
 * sleeping and hoping.
 *
 * Only when the page set {@link CARET_PROBE_FLAG}: without it the plugin
 * writes nothing, so the product's DOM is what it was.
 *
 * A plugin view rather than the `attributes` prop: that prop is part of the
 * document's outer decoration, so a caret move would re-diff the whole
 * document. A root attribute written here is ignored by the editor's DOM
 * observer, and only written when it changes.
 */
export const caretAttribute = $prose(
  () =>
    new Plugin({
      key: new PluginKey("mesh-caret-attribute"),
      view: (view) => {
        if (!probeEnabled()) return {};
        let last: string | null | undefined;
        const write = (state: EditorState) => {
          const next = caretPosition(state);
          if (next === last) return;
          last = next;
          if (next === null) view.dom.removeAttribute(CARET_ATTRIBUTE);
          else view.dom.setAttribute(CARET_ATTRIBUTE, next);
        };
        write(view.state);
        return {
          update: (v) => write(v.state),
          destroy: () => view.dom.removeAttribute(CARET_ATTRIBUTE),
        };
      },
    }),
);
