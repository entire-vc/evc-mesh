/**
 * A flat text view of a rendered document, and the map back to DOM positions.
 *
 * The anchoring code works on one string; the browser works on text nodes. This
 * is the only place that knows both. Nothing here mutates the DOM — the rendered
 * body belongs to ProseMirror, which watches its own subtree, and wrapping
 * ranges in `<mark>` elements would be a change it is entitled to undo. The
 * highlight is painted with the CSS Custom Highlight API instead, which needs
 * Ranges and no elements at all.
 */

/** One text node's contribution to the flattened string. */
interface TextPiece {
  /** Null for a synthetic block separator, which belongs to no node. */
  node: Text | null;
  /** Offset of this piece's first character in the flattened string. */
  start: number;
  length: number;
}

export interface FlatText {
  text: string;
  pieces: TextPiece[];
}

/**
 * Tags that end a line of prose. Two adjacent paragraphs contribute text nodes
 * with nothing between them, so without a separator "…end of one" and "start of
 * the next…" are glued into a word that exists on no screen — which then matches
 * a quote nobody selected.
 *
 * Tag names rather than computed styles on purpose: this runs on every render
 * and must not force layout.
 */
const BLOCK_TAGS = new Set([
  "ADDRESS", "ARTICLE", "ASIDE", "BLOCKQUOTE", "BR", "DD", "DIV", "DL", "DT",
  "FIELDSET", "FIGCAPTION", "FIGURE", "FOOTER", "FORM", "H1", "H2", "H3", "H4",
  "H5", "H6", "HEADER", "HR", "LI", "MAIN", "NAV", "OL", "P", "PRE", "SECTION",
  "TABLE", "TD", "TH", "TR", "UL",
]);

function nearestBlock(node: Node): Element | null {
  let el = node.parentElement;
  while (el) {
    if (BLOCK_TAGS.has(el.tagName)) return el;
    el = el.parentElement;
  }
  return null;
}

/**
 * Flatten a container's text, remembering which node each character came from.
 *
 * The result is stable for a given DOM: two calls on an unchanged subtree
 * produce the same string and the same offsets, which is what lets an anchor
 * located once be re-located after a re-render.
 */
export function flattenText(container: Node): FlatText {
  const pieces: TextPiece[] = [];
  let text = "";

  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
  let previousBlock: Element | null = null;
  let first = true;

  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const textNode = node as Text;
    const data = textNode.data;
    if (!data) continue;

    const block = nearestBlock(textNode);
    if (!first && block !== previousBlock) {
      pieces.push({ node: null, start: text.length, length: 1 });
      text += "\n";
    }
    previousBlock = block;
    first = false;

    pieces.push({ node: textNode, start: text.length, length: data.length });
    text += data;
  }

  return { text, pieces };
}

/** The DOM position — node plus offset — of a flattened-text index. */
function positionAt(
  flat: FlatText,
  index: number,
  preferEnd: boolean,
): { node: Text; offset: number } | null {
  const clamped = Math.max(0, Math.min(index, flat.text.length));

  let lastReal: { node: Text; offset: number } | null = null;
  for (const piece of flat.pieces) {
    if (!piece.node) continue;
    const end = piece.start + piece.length;
    if (clamped < piece.start) break;
    if (clamped < end || (clamped === end && preferEnd)) {
      return { node: piece.node, offset: clamped - piece.start };
    }
    lastReal = { node: piece.node, offset: piece.length };
  }
  return lastReal;
}

/** A DOM Range covering [start, end) of the flattened text, if it can be made. */
export function rangeFromSpan(
  flat: FlatText,
  span: { start: number; end: number },
): Range | null {
  const from = positionAt(flat, span.start, false);
  const to = positionAt(flat, span.end, true);
  if (!from || !to) return null;
  try {
    const range = document.createRange();
    range.setStart(from.node, from.offset);
    range.setEnd(to.node, to.offset);
    return range.collapsed ? null : range;
  } catch {
    return null;
  }
}

/** Flattened-text index of a DOM position, or null when it is outside. */
export function indexOfPosition(
  flat: FlatText,
  node: Node,
  offset: number,
): number | null {
  if (node.nodeType === Node.TEXT_NODE) {
    for (const piece of flat.pieces) {
      if (piece.node === node) return piece.start + Math.min(offset, piece.length);
    }
    return null;
  }

  // An element position: take the first text node at or after it.
  const child = node.childNodes[offset] ?? node.lastChild;
  if (!child) return null;
  for (const piece of flat.pieces) {
    if (piece.node && (piece.node === child || child.contains(piece.node))) {
      return piece.start;
    }
  }
  return null;
}

/** The flattened span a DOM Range covers, or null when it is not inside. */
export function spanFromRange(
  flat: FlatText,
  range: Range,
): { start: number; end: number } | null {
  const start = indexOfPosition(flat, range.startContainer, range.startOffset);
  const end = indexOfPosition(flat, range.endContainer, range.endOffset);
  if (start === null || end === null) return null;
  return start <= end ? { start, end } : { start: end, end: start };
}

/**
 * Which flattened index a click landed on. Used to turn a click on a highlight
 * into a thread, without the highlight being an element that could be clicked.
 */
export function indexFromPoint(
  flat: FlatText,
  x: number,
  y: number,
): number | null {
  const doc = document as Document & {
    caretPositionFromPoint?: (
      x: number,
      y: number,
    ) => { offsetNode: Node; offset: number } | null;
    caretRangeFromPoint?: (x: number, y: number) => Range | null;
  };

  if (typeof doc.caretPositionFromPoint === "function") {
    const pos = doc.caretPositionFromPoint(x, y);
    if (pos) return indexOfPosition(flat, pos.offsetNode, pos.offset);
  }
  if (typeof doc.caretRangeFromPoint === "function") {
    const range = doc.caretRangeFromPoint(x, y);
    if (range) return indexOfPosition(flat, range.startContainer, range.startOffset);
  }
  return null;
}
