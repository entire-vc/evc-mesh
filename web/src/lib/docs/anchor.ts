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
 * ## One scheme, two callers
 *
 * A paragraph link (D6) anchors a whole block; a comment (D7) anchors an
 * arbitrary selection that may start and end mid-block. They are the same thing:
 * a **range** in the document's text, and a linked paragraph is the range that
 * happens to cover one block exactly. So the range functions are the primitive
 * and the block ones are a thin wrapper — one ladder, one encoding, one answer to
 * "where was this pointing". Two schemes would eventually disagree with each
 * other about the same document, and there would be no way to tell which was
 * right.
 *
 * ## Coordinate system
 *
 * `start`/`end` are half-open character offsets into the document's *text
 * projection*: every top-level block's text, whitespace-collapsed, joined with
 * `\n`. They are a **hint** — used to pick between identical candidates and to
 * notice that nothing moved. Identity is carried by `exact`. That split is what
 * lets the scheme tolerate offsets going stale, which they do on the first
 * edit.
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

/**
 * Collapse every whitespace run to one space, and remember where each surviving
 * character came from.
 *
 * The map is what lets an offset in the projection be turned back into a
 * position in the text the reader can actually see, which is what drawing a
 * highlight over a commented range needs. `rawIndex[i]` is the index in `raw` of
 * the i-th character of `text`.
 *
 * Markdown wraps lines and readers do not, so a paragraph broken across three
 * source lines has to compare equal to the same paragraph rewrapped. That is the
 * whole reason for collapsing; the map is the price of doing it without losing
 * the ability to point at anything.
 */
export function normalizeWithMap(raw: string): { text: string; rawIndex: number[] } {
  const chars: string[] = [];
  const rawIndex: number[] = [];
  let inWhitespace = false;
  for (let i = 0; i < raw.length; i += 1) {
    const ch = raw[i]!;
    if (/\s/.test(ch)) {
      if (!inWhitespace) {
        chars.push(" ");
        rawIndex.push(i);
        inWhitespace = true;
      }
      continue;
    }
    chars.push(ch);
    rawIndex.push(i);
    inWhitespace = false;
  }
  let from = 0;
  let to = chars.length;
  while (from < to && chars[from] === " ") from += 1;
  while (to > from && chars[to - 1] === " ") to -= 1;
  return { text: chars.slice(from, to).join(""), rawIndex: rawIndex.slice(from, to) };
}

/**
 * Collapse every whitespace run to one space.
 *
 * Delegates rather than re-implementing: two spellings of "the same
 * normalisation" drift, and the drift would show up as a comment highlight
 * landing a few characters off — which reads as a rendering bug, not as two
 * functions disagreeing.
 */
export function normalizeBlockText(raw: string): string {
  return normalizeWithMap(raw).text;
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

/** The projection itself: block texts joined the way their offsets assume. */
export function projectionOf(blocks: readonly DocBlock[]): string {
  return blocks.map((b) => b.text).join("\n");
}

/** Where a range anchor points now. Offsets are into the current projection. */
export interface RangeMatch {
  status: AnchorStatus;
  /** Half-open range, or null when nothing could be located. */
  start: number | null;
  end: number | null;
}

/**
 * How far apart the surrounding context may drift before "the text here was
 * edited" stops being a claim worth making.
 *
 * Without a bound, a `prefix` and a `suffix` that both survive but now sit half a
 * document apart would be reported as one edited range covering everything
 * between them — technically consistent with the evidence and useless to a
 * reader. Four times the original length, floored so that short ranges are not
 * held to an absurdly tight budget.
 */
const GAP_SLACK = 200;

/** All indexes at which `needle` occurs in `haystack`. Empty needle → none. */
function allIndexesOf(haystack: string, needle: string): number[] {
  if (needle === "") return [];
  const found: number[] = [];
  for (let i = haystack.indexOf(needle); i !== -1; i = haystack.indexOf(needle, i + 1)) {
    found.push(i);
  }
  return found;
}

/**
 * The anchor for the half-open range [start, end) of `text`.
 *
 * An empty range is allowed here and refused at the API boundary, and the
 * asymmetry is deliberate: a paragraph with no text of its own (an image, a rule)
 * can still be linked to — it is located by its surroundings alone — but a
 * comment on nothing is not a comment, and an empty quote would match at every
 * position in the document.
 */
export function makeRangeAnchor(
  text: string,
  start: number,
  end: number,
): DocAnchor | null {
  if (start < 0 || end < start || end > text.length) return null;
  return {
    start,
    end,
    exact: text.slice(start, end).slice(0, EXACT_CAP),
    prefix: text.slice(Math.max(0, start - CONTEXT_CAP), start),
    suffix: text.slice(end, end + CONTEXT_CAP),
  };
}

/** Does the anchored quote sit at `pos`? */
function quoteSitsAt(text: string, anchor: DocAnchor, pos: number): boolean {
  if (anchor.exact === "") return false;
  const length = anchor.end - anchor.start;
  if (pos < 0 || pos + length > text.length) return false;
  // Leading slice plus total length, so a quote longer than EXACT_CAP is still
  // matched exactly — and a different passage that merely opens the same way is
  // still rejected.
  return text.startsWith(anchor.exact, pos);
}

/** How well the text either side of `pos` agrees with the anchor's context. */
function rangeContextScore(text: string, anchor: DocAnchor, pos: number): number {
  const length = anchor.end - anchor.start;
  let score = 0;
  if (text.slice(Math.max(0, pos - anchor.prefix.length), pos) === anchor.prefix) {
    score += 1;
  }
  if (text.slice(pos + length, pos + length + anchor.suffix.length) === anchor.suffix) {
    score += 1;
  }
  return score;
}

/**
 * Ranges whose surroundings still read as the anchor's, for when the quote
 * itself is gone. The gap between the surviving context is where the anchored
 * text used to be.
 */
function framedRanges(text: string, anchor: DocAnchor): { start: number; end: number }[] {
  const prefixEnds =
    anchor.prefix === ""
      ? // An empty prefix means the range began at the very start of the
        // document, so the only position consistent with it is 0 — not "anywhere,
        // no constraint", which is how a frame check quietly stops constraining.
        [0]
      : allIndexesOf(text, anchor.prefix).map((i) => i + anchor.prefix.length);

  const maxGap = Math.max(GAP_SLACK, (anchor.end - anchor.start) * 4);
  const found: { start: number; end: number }[] = [];
  for (const start of prefixEnds) {
    const end =
      anchor.suffix === "" ? text.length : text.indexOf(anchor.suffix, start);
    if (end < start) continue;
    if (end - start > maxGap) continue;
    found.push({ start, end });
  }
  return found;
}

/**
 * Where does this range anchor point now?
 *
 * The ladder is the same one block anchors use, and ordered the same way, because
 * it is literally the same function: identity first, position only as a
 * tie-break, and a refusal at the bottom rather than a guess.
 */
export function resolveRangeAnchor(
  text: string,
  anchor: DocAnchor,
  /**
   * An extra condition a candidate range must satisfy.
   *
   * It exists for one caller: a block anchor may only ever resolve to a WHOLE
   * block. Without it, a paragraph that shares its first `EXACT_CAP` characters
   * with the anchored one — the quote is capped, so that is all the identity
   * there is — matches at the same offset and is reported as the same paragraph,
   * with the range ending in the middle of it. The block caller knows something
   * the general resolver cannot: that the answer has to line up with a block
   * boundary, which rejects exactly that case.
   *
   * For a comment there is no such extra knowledge, and the residual limitation
   * is real and bounded: two ranges agreeing on their first `EXACT_CAP`
   * characters AND their total length are indistinguishable to this scheme.
   * Widening the cap moves that boundary without removing it; the honest fix
   * would be a second field carrying a digest of the whole quote, which the
   * anchor shape (ADR G1, and now a table) does not have.
   */
  isAcceptable?: (start: number, end: number) => boolean,
): RangeMatch {
  const length = anchor.end - anchor.start;
  const acceptable = (start: number, end: number) =>
    isAcceptable ? isAcceptable(start, end) : true;

  // 1-2. The quote.
  const candidates = allIndexesOf(text, anchor.exact).filter(
    (pos) => quoteSitsAt(text, anchor, pos) && acceptable(pos, pos + length),
  );
  if (candidates.length > 0) {
    let best = candidates[0]!;
    let bestScore = rangeContextScore(text, anchor, best);
    let bestDistance = Math.abs(best - anchor.start);
    for (const pos of candidates.slice(1)) {
      const score = rangeContextScore(text, anchor, pos);
      const distance = Math.abs(pos - anchor.start);
      if (score > bestScore || (score === bestScore && distance < bestDistance)) {
        best = pos;
        bestScore = score;
        bestDistance = distance;
      }
    }
    return {
      status: best === anchor.start ? "exact" : "moved",
      start: best,
      end: best + length,
    };
  }

  // 3. The quote is gone — the text was edited, or there never was any (an
  // image-only paragraph). Its place is still identifiable while the surrounding
  // context holds.
  const framed = framedRanges(text, anchor).filter((r) => acceptable(r.start, r.end));
  if (framed.length > 0) {
    let best = framed[0]!;
    for (const range of framed.slice(1)) {
      if (Math.abs(range.start - anchor.start) < Math.abs(best.start - anchor.start)) {
        best = range;
      }
    }
    // A range that never had text is matched by its frame alone, so "the frame
    // holds and it is still empty" is the unchanged case for it — reporting that
    // as edited would cry wolf on a document nobody touched.
    if (anchor.exact === "" && best.end === best.start) {
      return {
        status: best.start === anchor.start ? "exact" : "moved",
        start: best.start,
        end: best.end,
      };
    }
    return { status: "edited", start: best.start, end: best.end };
  }

  // 4. Refuse to guess.
  return { status: "lost", start: null, end: null };
}

/**
 * The anchor for block `index`, or null when there is no such block.
 *
 * A block anchor IS a range anchor over the block's own span — the wrapper exists
 * so a caller holding blocks does not have to build the projection itself, not
 * because the two are different kinds of thing.
 */
export function makeAnchor(
  blocks: readonly DocBlock[],
  index: number,
): DocAnchor | null {
  const block = blocks[index];
  if (!block) return null;
  return makeRangeAnchor(projectionOf(blocks), block.start, block.end);
}

/**
 * Which block contains `offset`? The block whose span covers it, or the one it
 * falls just after — an offset landing on a separator belongs to the block that
 * ended there.
 */
function blockContaining(blocks: readonly DocBlock[], offset: number): number | null {
  for (let i = 0; i < blocks.length; i += 1) {
    if (offset >= blocks[i]!.start && offset <= blocks[i]!.end) return i;
  }
  return null;
}

/**
 * Where does this anchor point now, in blocks?
 *
 * The whole ladder lives in `resolveRangeAnchor`; this maps its answer back to
 * the block a reader can be scrolled to. Identity outranks position there, which
 * is what makes this safe: reversing those two is the silent-wrong-paragraph bug,
 * where after an insertion above, the old offset belongs to some other paragraph
 * and a position-first resolver reports it with full confidence.
 */
export function resolveAnchor(
  blocks: readonly DocBlock[],
  anchor: DocAnchor,
): AnchorMatch {
  if (blocks.length === 0) return { status: "lost", index: null };

  // A block anchor resolves to a whole block or not at all — see the
  // `isAcceptable` note on resolveRangeAnchor for what this rejects.
  const spans = new Set(blocks.map((b) => `${b.start}:${b.end}`));
  const match = resolveRangeAnchor(projectionOf(blocks), anchor, (start, end) =>
    spans.has(`${start}:${end}`),
  );
  if (match.start === null) return { status: "lost", index: null };

  const index = blockContaining(blocks, match.start);
  if (index === null) return { status: "lost", index: null };
  return { status: match.status, index };
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
