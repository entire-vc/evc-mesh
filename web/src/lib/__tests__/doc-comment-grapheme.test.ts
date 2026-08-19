import { describe, expect, it } from "vitest";
import {
  byteToUtf16Offset,
  snapToCodePoint,
  snapToGrapheme,
  utf16ToByteOffset,
} from "@/lib/doc-comments/offsets";

/**
 * Breaking inside a grapheme cluster.
 *
 * `snapToCodePoint` stops an index falling between the halves of a surrogate
 * pair, which is the case that produces U+FFFD and is already covered. It does
 * NOT stop an index falling between the parts of one visible character, because
 * those parts are separate code points and every boundary between them is legal.
 *
 * The two shapes below are the ones a document actually contains — an accented
 * letter written in decomposed form, and a family emoji — and both were measured
 * against this module before the snap existed:
 *
 *   "café latte", offset inside the é  -> came back on the combining acute
 *   "a 👩‍👧 b",    offset after the woman -> came back on the zero-width joiner
 *
 * A range starting at either point renders as a highlight that begins on the
 * wrong letter, or in the middle of one glyph.
 *
 * Note on reach: `byteToUtf16Offset` has no production caller today — the UI
 * finds a quote by searching rather than by converting a stored offset back. So
 * this is a latent gap rather than a live defect, which is exactly the kind
 * worth closing now: an exported, tested converter is what the first
 * re-anchoring pass will reach for, and it will trust these tests.
 */

// e + U+0301, not the precomposed é — the decomposed form is what a paste from
// macOS produces, and it is indistinguishable on screen.
const DECOMPOSED = "café latte";
// woman + ZWJ + girl: three code points, four UTF-16 units, one glyph.
const FAMILY = "\u{1F469}‍\u{1F467}";

describe("snapToGrapheme", () => {
  it("leaves an index that is already on a boundary alone", () => {
    // The positive control. A snap that moved everything would pass every test
    // below by accident.
    expect(snapToGrapheme(DECOMPOSED, 0)).toBe(0);
    expect(snapToGrapheme(DECOMPOSED, 3)).toBe(3); // before the "e"
    expect(snapToGrapheme("plain ascii", 4)).toBe(4);
  });

  it("moves an index off a combining mark onto its base letter", () => {
    const markIndex = DECOMPOSED.indexOf("́");
    expect(snapToGrapheme(DECOMPOSED, markIndex)).toBe(markIndex - 1);
    // And what it lands on is the whole visible character, not half of it.
    expect(DECOMPOSED.slice(snapToGrapheme(DECOMPOSED, markIndex), markIndex + 1)).toBe("é");
  });

  it("NEGATIVE CONTROL: the code-point snap leaves it on the mark", () => {
    // What this is buying, as a measurement rather than a claim in a comment. If
    // these two ever agree, the grapheme snap has stopped doing anything.
    const markIndex = DECOMPOSED.indexOf("́");
    expect(snapToCodePoint(DECOMPOSED, markIndex)).toBe(markIndex);
    expect(snapToGrapheme(DECOMPOSED, markIndex)).not.toBe(
      snapToCodePoint(DECOMPOSED, markIndex),
    );
  });

  it("moves an index out of a ZWJ sequence to the start of the glyph", () => {
    const text = `a ${FAMILY} b`;
    const start = text.indexOf(FAMILY);
    // Between the woman and the joiner: a legal code-point boundary, inside one
    // character.
    expect(snapToGrapheme(text, start + 2)).toBe(start);
    // As is every other interior position of the same cluster.
    expect(snapToGrapheme(text, start + 3)).toBe(start);
  });

  it("still refuses to split a surrogate pair", () => {
    const text = `a ${FAMILY} b`;
    const start = text.indexOf(FAMILY);
    // Inside the woman's own surrogate pair.
    expect(snapToGrapheme(text, start + 1)).toBe(start);
  });

  it("clamps rather than throwing at the edges", () => {
    expect(snapToGrapheme(DECOMPOSED, -5)).toBe(0);
    expect(snapToGrapheme(DECOMPOSED, 9999)).toBe(DECOMPOSED.length);
  });

  it("degrades to the code-point snap when Intl.Segmenter is absent", () => {
    // Not a hand-rolled grapheme rule in the fallback: a wrong rule is harder to
    // notice than a missing one.
    const intl = Intl as unknown as Record<string, unknown>;
    const original = intl.Segmenter;
    try {
      delete intl.Segmenter;
      const markIndex = DECOMPOSED.indexOf("́");
      expect(snapToGrapheme(DECOMPOSED, markIndex)).toBe(
        snapToCodePoint(DECOMPOSED, markIndex),
      );
    } finally {
      intl.Segmenter = original;
    }
  });
});

describe("byteToUtf16Offset lands on a whole character", () => {
  it("a byte offset inside a decomposed é resolves to the base letter", () => {
    const markIndex = DECOMPOSED.indexOf("́");
    const byteOfMark = utf16ToByteOffset(DECOMPOSED, markIndex);

    const back = byteToUtf16Offset(DECOMPOSED, byteOfMark);

    expect(back).toBe(markIndex - 1);
    // The observable consequence: the text from here begins with a letter, not
    // with a diacritic that will attach itself to the previous one.
    expect(DECOMPOSED.slice(back)).toMatch(/^é/);
  });

  it("a byte offset inside a family emoji resolves to the start of the glyph", () => {
    const text = `a ${FAMILY} b`;
    const start = text.indexOf(FAMILY);
    const byteAfterWoman = utf16ToByteOffset(text, start + 2);

    const back = byteToUtf16Offset(text, byteAfterWoman);

    expect(back).toBe(start);
    expect(text.slice(back)).toMatch(/^\u{1F469}/u);
  });

  it("POSITIVE CONTROL: plain text round-trips unchanged", () => {
    // The snap must not move offsets in the 99% case. Every position of an ASCII
    // string, both directions.
    const text = "The migration is applied before the image swap.";
    for (let i = 0; i <= text.length; i += 1) {
      expect(byteToUtf16Offset(text, utf16ToByteOffset(text, i))).toBe(i);
    }
  });

  it("POSITIVE CONTROL: Cyrillic round-trips unchanged", () => {
    // Two bytes per letter and no clusters: the snap has nothing to do, and must
    // do nothing.
    const text = "миграция применяется до подмены образа";
    for (let i = 0; i <= text.length; i += 1) {
      expect(byteToUtf16Offset(text, utf16ToByteOffset(text, i))).toBe(i);
    }
  });
});
