/**
 * Conversion between the two ways this codebase counts positions in a document
 * body, which are not the same number and are never interchangeable.
 *
 * The API stores `anchor.start` / `anchor.end` as **byte** offsets into the
 * document's markdown, half-open [start, end) — the same convention Postgres
 * `substring()`, Go's `len(string)` and memory_chunks all use.
 *
 * JavaScript indexes strings in **UTF-16 code units**. For ASCII the two agree,
 * which is exactly what makes this dangerous: everything looks correct until a
 * document is written in Russian, and then every anchor in it is silently
 * misplaced by roughly its own length. A Cyrillic letter is one UTF-16 unit and
 * two UTF-8 bytes; an emoji is two UTF-16 units and four bytes.
 *
 * So: nothing outside this module may do arithmetic on `anchor.start`. Convert
 * at the boundary, in both directions, and keep the units in the names.
 */

const encoder = new TextEncoder();

/** Byte length of `text` when encoded as UTF-8. */
export function byteLength(text: string): number {
  return encoder.encode(text).length;
}

/**
 * Snap a UTF-16 index to the nearest code-point boundary at or below it.
 *
 * A selection can only ever land between code points in practice, but an index
 * arriving from arithmetic can land between the halves of a surrogate pair, and
 * encoding a lone surrogate silently produces U+FFFD — three bytes of a
 * character that is not in the document.
 */
export function snapToCodePoint(text: string, index: number): number {
  if (index <= 0) return 0;
  if (index >= text.length) return text.length;
  const code = text.charCodeAt(index);
  // A low surrogate at `index` means `index - 1` is its high half.
  if (code >= 0xdc00 && code <= 0xdfff) {
    const prev = text.charCodeAt(index - 1);
    if (prev >= 0xd800 && prev <= 0xdbff) return index - 1;
  }
  return index;
}

/**
 * UTF-16 index -> UTF-8 byte offset.
 *
 * This is what turns a browser selection into something the API will accept.
 */
export function utf16ToByteOffset(text: string, index: number): number {
  const at = snapToCodePoint(text, Math.max(0, Math.min(index, text.length)));
  if (at === 0) return 0;
  return encoder.encode(text.slice(0, at)).length;
}

/**
 * Snap a UTF-16 index back to the nearest grapheme-cluster boundary at or below
 * it.
 *
 * `snapToCodePoint` is not enough, and the difference is measurable rather than
 * theoretical. A code point is not a character a reader can see:
 *
 *   - `"café"` written as `e` + U+0301 is two code points and one glyph. An index
 *     landing between them is a legal code-point boundary, and a range starting
 *     there begins with a bare combining acute — which renders attached to
 *     whatever precedes it, so the highlight starts on the wrong letter.
 *   - `👩‍👧` is three code points (woman, ZWJ, girl) and one glyph. An index after
 *     the woman is again a legal code-point boundary, and a range from there
 *     begins with a zero-width joiner and half a family.
 *
 * Both were measured against this module before this function existed: an offset
 * inside `"café latte"` came back pointing at the combining mark, and one inside
 * the family emoji came back pointing at the joiner.
 *
 * `Intl.Segmenter` is the only correct way to do this — the rules are Unicode
 * data, not a regex — and it is available in every browser this app supports.
 * Where it is absent the code-point snap is kept rather than a hand-rolled
 * approximation: a wrong grapheme rule is harder to notice than a missing one.
 */
/** The slice of `Intl.Segmenter` this module uses. See snapToGrapheme. */
interface GraphemeSegmenterCtor {
  new (
    locales: undefined,
    options: { granularity: "grapheme" },
  ): { segment(input: string): Iterable<{ index: number }> };
}

export function snapToGrapheme(text: string, index: number): number {
  const at = snapToCodePoint(text, Math.max(0, Math.min(index, text.length)));
  if (at <= 0 || at >= text.length) return at;

  // Typed locally rather than from the standard library: tsconfig.app targets
  // lib ES2020, where Intl.Segmenter is not declared. Raising the whole app's
  // lib for one call is a change with a blast radius; this is the part of the
  // API actually used, and nothing else has to move.
  const Segmenter = (Intl as unknown as { Segmenter?: GraphemeSegmenterCtor })
    .Segmenter;
  if (!Segmenter) return at;

  // Segment only the neighbourhood: a cluster is bounded in practice, and
  // segmenting a whole 5 MiB body to place one offset would be paid on every
  // anchor of every render.
  const WINDOW = 64;
  const from = Math.max(0, at - WINDOW);
  const segmenter = new Segmenter(undefined, { granularity: "grapheme" });

  let boundary = from;
  for (const { index: offset } of segmenter.segment(text.slice(from, at + WINDOW))) {
    const absolute = from + offset;
    if (absolute > at) break;
    boundary = absolute;
  }
  return boundary;
}

/**
 * UTF-8 byte offset -> UTF-16 index.
 *
 * A byte offset that lands inside a multi-byte character resolves to the start
 * of that character rather than throwing: the stored offset may predate an edit,
 * and answering "here, roughly" beats refusing to draw the highlight at all.
 *
 * "Roughly" stops at a grapheme boundary, not a code-point one — see
 * snapToGrapheme for what the difference looks like on screen.
 */
export function byteToUtf16Offset(text: string, byteOffset: number): number {
  if (byteOffset <= 0) return 0;

  let bytes = 0;
  let i = 0;
  while (i < text.length) {
    const cp = text.codePointAt(i);
    if (cp === undefined) break;
    const units = cp > 0xffff ? 2 : 1;
    const width = cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;

    if (bytes + width > byteOffset) return snapToGrapheme(text, i);
    bytes += width;
    i += units;
    if (bytes === byteOffset) return snapToGrapheme(text, i);
  }
  return text.length;
}
