import type { EditorView } from "@milkdown/kit/prose/view";
import { findDocLinkTrigger } from "@/lib/docs/doc-link";

/**
 * The `[[` and `@` menus, over ProseMirror instead of over a textarea.
 *
 * Both menus already existed; they were written against `textarea.value` and
 * `selectionStart`, which a rich-text editor does not have. The logic that
 * decides WHETHER a menu is open, and on what query, is unchanged and still
 * lives where it did (`findDocLinkTrigger`) — what changes is how the text
 * before the caret is obtained and how the accepted suggestion is put back.
 *
 * Everything here works in one text block. That is not a simplification: a
 * trigger cannot span a paragraph, a list item or a table cell, and scanning the
 * whole document for `[[` would find one the writer left three paragraphs ago.
 */

export type SuggestionKind = "doc" | "mention";

export interface Suggestion {
  kind: SuggestionKind;
  query: string;
  /** Document position of the trigger's first character. */
  from: number;
  /** Document position of the caret. */
  to: number;
}

/**
 * `@` followed by anything that is not whitespace or another `@`.
 * Same expression the textarea composer used, so the two behave identically.
 */
const MENTION_TRIGGER = /@([^\s@]*)$/;

/**
 * The text of the current block up to the caret.
 *
 * `￼` (object replacement) stands in for inline leaf nodes — an image, say
 * — so that one leaf counts as exactly one character and offsets in this string
 * stay convertible to document positions by simple addition. Using the default
 * (drop them entirely) would silently shift every position after an image.
 */
function textBeforeCaret(view: EditorView): string | null {
  const { selection } = view.state;
  // A trigger belongs to a collapsed caret. With text selected the writer is
  // replacing something, not typing a query.
  if (!selection.empty) return null;
  const $from = selection.$from;
  if (!$from.parent.isTextblock) return null;
  return $from.parent.textBetween(0, $from.parentOffset, undefined, "￼");
}

/**
 * The menu that should be open right now, or null.
 *
 * `[[` wins over `@` when both could match, because `[[` is the longer and more
 * deliberate trigger: `[[@foo` is someone typing a document title, not a
 * mention.
 */
export function findSuggestion(view: EditorView): Suggestion | null {
  const before = textBeforeCaret(view);
  if (before === null) return null;

  const caret = view.state.selection.$from.pos;
  const blockStart = view.state.selection.$from.start();

  const doc = findDocLinkTrigger(before, before.length);
  if (doc) {
    return {
      kind: "doc",
      query: doc.query,
      from: blockStart + doc.start,
      to: caret,
    };
  }

  const mention = MENTION_TRIGGER.exec(before);
  if (mention) {
    const query = mention[1] ?? "";
    return {
      kind: "mention",
      query,
      from: caret - query.length - 1,
      to: caret,
    };
  }

  return null;
}

/** Where to draw the menu: the caret's position on screen. */
export function caretCoords(
  view: EditorView,
  pos: number,
): { left: number; top: number; bottom: number } {
  const { left, top, bottom } = view.coordsAtPos(pos);
  return { left, top, bottom };
}

/**
 * Should a separating space follow the insertion?
 *
 * Only when there is something after it to separate from. Three cases:
 *
 *  - next character is whitespace — already separated, adding one leaves a
 *    double space in the middle of the writer's sentence;
 *  - next character is text — needs the space, or the link runs into the word;
 *  - nothing follows (end of the block) — needs NOTHING, and adding a space is
 *    actively wrong: remark-stringify escapes trailing whitespace to preserve
 *    it, so the saved markdown ends up with a literal `&#x20;` after the link.
 *    Measured, not predicted — a test asserting the exact saved markdown caught
 *    it, and it would have shipped as visible garbage at the end of every task
 *    description where someone accepted a `[[` suggestion.
 *
 * Keeping the caret out of the link mark is handled separately, by clearing the
 * stored marks, so it does not depend on the space being there.
 */
function needsTrailingSpace(view: EditorView, at: number): boolean {
  const { doc } = view.state;
  const next = doc.textBetween(at, Math.min(at + 1, doc.content.size));
  if (next === "") return false;
  return !/^\s/.test(next);
}

/**
 * Replace the trigger with a link, and leave the caret past it.
 *
 * Stored marks are cleared explicitly: without it the link mark is still active
 * at the caret and the next character the writer types joins the link, silently
 * becoming part of its label.
 */
export function replaceWithLink(
  view: EditorView,
  range: { from: number; to: number },
  label: string,
  href: string,
): void {
  const { state } = view;
  const linkMark = state.schema.marks.link;
  if (!linkMark) return;

  const nodes = [state.schema.text(label, [linkMark.create({ href })])];
  if (needsTrailingSpace(view, range.to)) nodes.push(state.schema.text(" "));

  view.dispatch(state.tr.replaceWith(range.from, range.to, nodes).setStoredMarks([]));
  view.focus();
}

/** Replace the trigger with plain text (a mention is text, not a link). */
export function replaceWithText(
  view: EditorView,
  range: { from: number; to: number },
  text: string,
): void {
  const { state } = view;
  const value = needsTrailingSpace(view, range.to) ? `${text} ` : text;
  view.dispatch(state.tr.replaceWith(range.from, range.to, state.schema.text(value)));
  view.focus();
}
