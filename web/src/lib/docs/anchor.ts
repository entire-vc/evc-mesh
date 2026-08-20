/**
 * Anchors into a document — the thing a "copy link to this paragraph" link
 * carries, and the thing that has to still mean something after the document
 * has been edited.
 *
 * ## The problem, stated honestly
 *
 * There are two obvious schemes and both are wrong on their own:
 *
 *   - **By ordinal position** (third paragraph, or byte offset 412). Breaks on
 *     *any* edit above the anchor — and breaks *silently*, by landing the reader
 *     on a different paragraph that is now at that position. This is the worst
 *     available outcome: the link still "works", it just lies.
 *   - **By the text itself** (a quote). Survives edits above it, and breaks only
 *     when the anchored paragraph is edited — but then it breaks completely,
 *     with no idea where the paragraph used to be.
 *
 * So the anchor carries both, plus the two neighbouring paragraphs, and
 * resolution is a fixed ladder that prefers identity over position and refuses
 * to guess at the bottom:
 *
 *   1. `exact` still at `start`             → `exact`  (document unchanged here)
 *   2. `exact` found somewhere else         → `moved`  (text above was edited)
 *   3. quote gone, but both neighbours hold → `edited` (this paragraph was edited)
 *   4. nothing holds                        → `lost`   (say so; scroll nowhere)
 *
 * Steps 1-2 are what makes a link survive editing above it. Steps 3-4 are what
 * makes the other case *predictable*: the reader is either taken to the place
 * the paragraph occupied and told the text changed, or told the fragment is
 * gone. What must never happen — a quiet jump to a paragraph that is not the one
 * the link was made from — is exactly what step 2 outranking position prevents.
 *
 * ## Coordinate system
 *
 * `start`/`end` are character offsets into the document's *text projection*:
 * every top-level block's text, whitespace-collapsed, joined with `\n`. They are
 * a **hint** — used to pick between identical candidates and to notice that
 * nothing moved. Identity is carried by `exact`. That split is what lets the
 * scheme tolerate offsets going stale, which they do on the first edit.
 *
 * Deliberately *not* markdown byte offsets: the editor is Milkdown (ADR G1), so
 * the client holds a ProseMirror tree and not a byte range, and a bridge between
 * the two would be a load-bearing dependency on a mapping that has to be right
 * for every construct. The projection is derivable from either representation —
 * a Go resolver can produce the same one from the stored markdown with any
 * CommonMark parser — and, because resolution is quote-first, `exact` alone is
 * enough for a consumer that has only the text.
 */

/** A top-level block of a document, with its place in the text projection. */
export interface DocBlock {
  /** Whitespace-collapsed text of the block. Empty for e.g. an image-only paragraph. */
  text: string;
  /** Offset of `text` in the projection. */
  start: number;
  /** `start + text.length`. */
  end: number;
}

/**
 * A link to one paragraph. The shape is fixed by ADR G1 condition 5, which asks
 * for it from the start precisely so that D7's comment ranges do not have to
 * invent a second one.
 */
export interface DocAnchor {
  start: number;
  end: number;
  /** Leading slice of the anchored block's text. */
  exact: string;
  /** Trailing slice of the preceding block's text; "" when the anchor is first. */
  prefix: string;
  /** Leading slice of the following block's text; "" when the anchor is last. */
  suffix: string;
}

export type AnchorStatus = "exact" | "moved" | "edited" | "lost";

export interface AnchorMatch {
  status: AnchorStatus;
  /** Index of the block to reveal, or null when nothing could be located. */
  index: number | null;
}

/**
 * Caps on what travels in a URL. `exact` is a *leading* slice rather than a hash
 * so that a long paragraph is still matched: `end - start` carries the full
 * length, so "same first N characters and the same total length" is the test,
 * and for any block shorter than the cap it degrades to plain equality.
 */
export const EXACT_CAP = 96;
export const CONTEXT_CAP = 32;

/** Collapse every whitespace run to one space. Markdown wraps lines; readers do not. */
export function normalizeBlockText(raw: string): string {
  return raw.replace(/\s+/g, " ").trim();
}

/** Build the text projection from the top-level block texts, in document order. */
export function buildBlocks(rawTexts: readonly string[]): DocBlock[] {
  const blocks: DocBlock[] = [];
  let offset = 0;
  for (const raw of rawTexts) {
    const text = normalizeBlockText(raw);
    blocks.push({ text, start: offset, end: offset + text.length });
    // +1 for the "\n" the projection joins blocks with.
    offset += text.length + 1;
  }
  return blocks;
}

/** The anchor for block `index`, or null when there is no such block. */
export function makeAnchor(
  blocks: readonly DocBlock[],
  index: number,
): DocAnchor | null {
  const block = blocks[index];
  if (!block) return null;
  const prev = blocks[index - 1];
  const next = blocks[index + 1];
  return {
    start: block.start,
    end: block.end,
    exact: block.text.slice(0, EXACT_CAP),
    prefix: prev ? prev.text.slice(-CONTEXT_CAP) : "",
    suffix: next ? next.text.slice(0, CONTEXT_CAP) : "",
  };
}

/** Does this block still carry the anchored text? */
function matchesQuote(block: DocBlock, anchor: DocAnchor): boolean {
  if (anchor.exact === "") return false;
  return (
    block.text.length === anchor.end - anchor.start &&
    block.text.startsWith(anchor.exact)
  );
}

/** Do the blocks on either side of `index` still read as they did? */
function neighboursHold(
  blocks: readonly DocBlock[],
  index: number,
  anchor: DocAnchor,
): boolean {
  const prev = blocks[index - 1];
  const next = blocks[index + 1];

  // An empty prefix means the anchor was the first block, so the only position
  // consistent with it is the first block — not "any block, no constraint".
  if (anchor.prefix === "") {
    if (prev) return false;
  } else if (!prev || !prev.text.endsWith(anchor.prefix)) {
    return false;
  }

  if (anchor.suffix === "") {
    if (next) return false;
  } else if (!next || !next.text.startsWith(anchor.suffix)) {
    return false;
  }

  return true;
}

/** Higher is better: how well the surroundings of `index` agree with the anchor. */
function contextScore(
  blocks: readonly DocBlock[],
  index: number,
  anchor: DocAnchor,
): number {
  let score = 0;
  const prev = blocks[index - 1];
  const next = blocks[index + 1];
  if (anchor.prefix === "" ? !prev : !!prev && prev.text.endsWith(anchor.prefix)) {
    score += 1;
  }
  if (anchor.suffix === "" ? !next : !!next && next.text.startsWith(anchor.suffix)) {
    score += 1;
  }
  return score;
}

/** Pick the best of several candidates: context first, then nearest to the hint. */
function pickBest(
  blocks: readonly DocBlock[],
  candidates: readonly number[],
  anchor: DocAnchor,
): number {
  let best = candidates[0]!;
  let bestScore = contextScore(blocks, best, anchor);
  let bestDistance = Math.abs(blocks[best]!.start - anchor.start);
  for (const index of candidates.slice(1)) {
    const score = contextScore(blocks, index, anchor);
    const distance = Math.abs(blocks[index]!.start - anchor.start);
    if (score > bestScore || (score === bestScore && distance < bestDistance)) {
      best = index;
      bestScore = score;
      bestDistance = distance;
    }
  }
  return best;
}

/**
 * Where does this anchor point now?
 *
 * The ladder is ordered so that identity outranks position. Reversing those two
 * is the silent-wrong-paragraph bug: after an insertion above, the old offset
 * belongs to some other paragraph, and a position-first resolver would report it
 * with full confidence.
 */
export function resolveAnchor(
  blocks: readonly DocBlock[],
  anchor: DocAnchor,
): AnchorMatch {
  if (blocks.length === 0) return { status: "lost", index: null };

  // 1-2. The quote.
  const quoted: number[] = [];
  for (let i = 0; i < blocks.length; i += 1) {
    if (matchesQuote(blocks[i]!, anchor)) quoted.push(i);
  }
  if (quoted.length > 0) {
    const index = quoted.length === 1 ? quoted[0]! : pickBest(blocks, quoted, anchor);
    const status: AnchorStatus =
      blocks[index]!.start === anchor.start ? "exact" : "moved";
    return { status, index };
  }

  // 3. The quote is gone — this paragraph was edited, or it never had text
  // (an image-only paragraph, a rule). Its place is still identifiable as long
  // as both neighbours read as they did.
  const framed: number[] = [];
  for (let i = 0; i < blocks.length; i += 1) {
    if (neighboursHold(blocks, i, anchor)) framed.push(i);
  }
  if (framed.length > 0) {
    const index = pickBest(blocks, framed, anchor);
    // A block that never had text (an image-only paragraph, a rule) is matched
    // by its frame alone, so "the frame holds and it is still textless" is the
    // unchanged case for it — reporting that as edited would cry wolf on a
    // document nobody touched.
    if (anchor.exact === "" && blocks[index]!.text === "") {
      return {
        status: blocks[index]!.start === anchor.start ? "exact" : "moved",
        index,
      };
    }
    return { status: "edited", index };
  }

  // 4. Refuse to guess.
  return { status: "lost", index: null };
}

// ---------------------------------------------------------------------------
// URL fragment
// ---------------------------------------------------------------------------

/** The fragment key. `#p=<encoded>` — a document fragment belongs in the hash. */
export const ANCHOR_FRAGMENT_KEY = "p";

function toBase64Url(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64Url(value: string): Uint8Array | null {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/");
  try {
    const binary = atob(padded);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
    return bytes;
  } catch {
    return null;
  }
}

/** Encode for a URL hash. Keys are single letters because this rides in a link. */
export function encodeAnchor(anchor: DocAnchor): string {
  const payload = JSON.stringify({
    s: anchor.start,
    e: anchor.end,
    x: anchor.exact,
    p: anchor.prefix,
    f: anchor.suffix,
  });
  return toBase64Url(new TextEncoder().encode(payload));
}

/** Decode one, or null for anything that is not a well-formed anchor. */
export function decodeAnchor(encoded: string): DocAnchor | null {
  const bytes = fromBase64Url(encoded);
  if (!bytes) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(new TextDecoder().decode(bytes));
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const raw = parsed as Record<string, unknown>;
  const { s, e, x, p, f } = raw;
  if (typeof s !== "number" || typeof e !== "number") return null;
  if (!Number.isFinite(s) || !Number.isFinite(e) || s < 0 || e < s) return null;
  if (typeof x !== "string" || typeof p !== "string" || typeof f !== "string") {
    return null;
  }
  // A hand-edited link must not be able to widen the matching window past what
  // this module ever writes.
  return {
    start: s,
    end: e,
    exact: x.slice(0, EXACT_CAP),
    prefix: p.slice(-CONTEXT_CAP),
    suffix: f.slice(0, CONTEXT_CAP),
  };
}

/** `#p=<encoded>` out of a `location.hash`, or null when there is none. */
export function anchorFromHash(hash: string): DocAnchor | null {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  if (!raw) return null;
  const params = new URLSearchParams(raw);
  const encoded = params.get(ANCHOR_FRAGMENT_KEY);
  if (!encoded) return null;
  return decodeAnchor(encoded);
}

/** The hash to put on a document URL for this anchor. */
export function anchorToHash(anchor: DocAnchor): string {
  return `#${ANCHOR_FRAGMENT_KEY}=${encodeAnchor(anchor)}`;
}
