import {
  type DocBlock,
  buildBlocks,
  normalizeWithMap,
} from "@/lib/docs/anchor";

/**
 * The bridge between an anchor's coordinates and the text on screen.
 *
 * An anchor lives in the document's *text projection* — block texts with
 * whitespace collapsed, joined with newlines (see anchor.ts). What a reader sees
 * is a tree of DOM nodes whose text still has the original whitespace in it. To
 * draw a highlight over a commented range, or to turn a reader's selection into
 * an anchor, those two coordinate systems have to be converted into each other,
 * exactly, in both directions.
 *
 * Doing it through the DOM rather than through ProseMirror positions is
 * deliberate: the selection a reader makes IS a DOM range, the highlight is drawn
 * over DOM rects, and a detour through editor positions would add a mapping that
 * has to be right for every node type — including ones a plugin might introduce
 * later. The measured fact this rests on (`anchor-in-editor.test.ts`) is that the
 * editor renders exactly one child element per top-level block, in order, so an
 * index names the same block on both sides.
 */

/** The blocks a rendered document is made of, in the anchor's coordinates. */
export function blocksOfElement(root: HTMLElement): DocBlock[] {
  return buildBlocks(Array.from(root.children).map((el) => el.textContent ?? ""));
}

/** A position inside the rendered text: which DOM text node, and how far in. */
interface DomPoint {
  node: Text;
  offset: number;
}

/**
 * Walk a block's text nodes to the given offset in its RAW text.
 *
 * The walker's concatenated output is the element's textContent by construction,
 * which is the same string `blocksOfElement` normalised — so the two agree
 * without either having to know about the other.
 */
function pointInElement(el: Element, rawOffset: number): DomPoint | null {
  const walker = el.ownerDocument.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  let seen = 0;
  let last: Text | null = null;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = node as Text;
    const length = text.data.length;
    // `<=` rather than `<`: an offset at the very end of a text node is a valid
    // position, and it is the one every range's end lands on.
    if (rawOffset <= seen + length) {
      return { node: text, offset: rawOffset - seen };
    }
    seen += length;
    last = text;
  }
  // Past the end — clamp to the end of the last text node rather than failing, so
  // a range whose end sits on a trailing whitespace character still resolves.
  if (last) return { node: last, offset: last.data.length };
  return null;
}

/** Which block contains this projection offset, and how far into it. */
function locate(
  blocks: readonly DocBlock[],
  offset: number,
): { index: number; within: number } | null {
  for (let i = 0; i < blocks.length; i += 1) {
    const block = blocks[i]!;
    if (offset >= block.start && offset <= block.end) {
      return { index: i, within: offset - block.start };
    }
  }
  return null;
}

/** Turn a projection offset into a point in the rendered text. */
function pointAt(
  root: HTMLElement,
  blocks: readonly DocBlock[],
  offset: number,
): DomPoint | null {
  const found = locate(blocks, offset);
  if (!found) return null;
  const el = root.children[found.index];
  if (!el) return null;

  const raw = el.textContent ?? "";
  const { rawIndex } = normalizeWithMap(raw);
  // At the end of a block there is no character to map, so the position is the
  // end of its raw text.
  const rawOffset = found.within < rawIndex.length ? rawIndex[found.within]! : raw.length;
  return pointInElement(el, rawOffset);
}

/**
 * A DOM Range covering [start, end) of the projection, or null when the range
 * cannot be placed.
 *
 * Null is returned rather than a partial range on purpose: half a highlight over
 * the wrong words is worse than no highlight, because the reader has no way to
 * tell it apart from a correct one.
 */
export function domRangeFor(
  root: HTMLElement,
  blocks: readonly DocBlock[],
  start: number,
  end: number,
): Range | null {
  const from = pointAt(root, blocks, start);
  const to = pointAt(root, blocks, end);
  if (!from || !to) return null;

  const range = root.ownerDocument.createRange();
  try {
    range.setStart(from.node, from.offset);
    range.setEnd(to.node, to.offset);
  } catch {
    return null;
  }
  // A range whose end precedes its start means the two offsets landed in an order
  // the document does not have — refuse rather than draw something arbitrary.
  if (range.collapsed && start !== end) return null;
  return range;
}

/** Turn a point in the rendered text back into a projection offset. */
function offsetOfPoint(
  root: HTMLElement,
  blocks: readonly DocBlock[],
  node: Node,
  offset: number,
): number | null {
  // Which top-level block is this inside?
  let el: Node | null = node;
  while (el && el.parentNode !== root) el = el.parentNode;
  if (!el) return null;
  const index = Array.prototype.indexOf.call(root.children, el);
  if (index < 0) return null;
  const block = blocks[index];
  if (!block) return null;

  // How far into the block's RAW text does the point sit?
  const walker = root.ownerDocument.createTreeWalker(el as Element, NodeFilter.SHOW_TEXT);
  let raw = 0;
  let reached = false;
  for (let current = walker.nextNode(); current; current = walker.nextNode()) {
    if (current === node) {
      raw += offset;
      reached = true;
      break;
    }
    raw += (current as Text).data.length;
  }
  // A selection boundary can land on an element rather than a text node — at the
  // very start or end of a block. Anything else is a point this code cannot
  // place, and guessing would put a comment on words nobody selected.
  if (!reached) {
    if (node === el && offset === 0) raw = 0;
    else if (node === el) raw = ((el as Element).textContent ?? "").length;
    else return null;
  }

  // Raw offset back to normalised: the first normalised character at or after it.
  const { rawIndex } = normalizeWithMap((el as Element).textContent ?? "");
  let within = rawIndex.findIndex((i) => i >= raw);
  if (within === -1) within = rawIndex.length;
  return block.start + within;
}

/** The projection range a reader's selection covers, or null if it is not one. */
export function projectionRangeOfSelection(
  root: HTMLElement,
  blocks: readonly DocBlock[],
  selection: Selection | null,
): { start: number; end: number } | null {
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) return null;
  const range = selection.getRangeAt(0);
  if (!root.contains(range.commonAncestorContainer)) return null;

  const start = offsetOfPoint(root, blocks, range.startContainer, range.startOffset);
  const end = offsetOfPoint(root, blocks, range.endContainer, range.endOffset);
  if (start === null || end === null || end <= start) return null;
  return { start, end };
}

// ---------------------------------------------------------------------------
// Painting
// ---------------------------------------------------------------------------

/** Highlight registry name; the matching ::highlight() rule is in doc-editor.css. */
export const COMMENT_HIGHLIGHT = "mesh-doc-comment";
export const COMMENT_HIGHLIGHT_ACTIVE = "mesh-doc-comment-active";

/**
 * Whether ranges can be painted without touching the document.
 *
 * The CSS Custom Highlight API draws over a Range and mutates nothing, which is
 * the only safe way to mark up text that a rich-text editor owns: wrapping the
 * words in `<mark>` elements would have the editor's own view reconcile them
 * away, or worse, keep them and write them into the saved body.
 */
export function highlightsSupported(): boolean {
  return typeof CSS !== "undefined" && "highlights" in CSS;
}

/**
 * Paint the given ranges. Callers pass every range each time — the registry is
 * replaced, not added to, so a comment that was deleted stops being painted
 * without anyone having to remember to unpaint it.
 */
export function paintHighlights(name: string, ranges: readonly Range[]): void {
  if (!highlightsSupported()) return;
  const registry = (CSS as unknown as { highlights: Map<string, unknown> }).highlights;
  if (ranges.length === 0) {
    registry.delete(name);
    return;
  }
  const Ctor = (globalThis as unknown as { Highlight?: new (...r: Range[]) => unknown })
    .Highlight;
  if (!Ctor) return;
  registry.set(name, new Ctor(...ranges));
}
