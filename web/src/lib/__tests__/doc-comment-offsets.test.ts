import { describe, expect, it } from "vitest";
import {
  byteLength,
  byteToUtf16Offset,
  snapToCodePoint,
  utf16ToByteOffset,
} from "@/lib/doc-comments/offsets";

/**
 * The bug these exist to prevent: `anchor.start` is a UTF-8 byte offset and a
 * JavaScript string index is a UTF-16 code unit. They agree for ASCII and for
 * nothing else, so a test suite written only in English would pass while every
 * anchor in a Russian document landed in the wrong place — and most of our
 * documents are in Russian.
 */

const CYRILLIC = "Привет, мир! Это документ.";

describe("utf16ToByteOffset", () => {
  it("is the identity for ASCII", () => {
    const text = "hello world";
    expect(utf16ToByteOffset(text, 0)).toBe(0);
    expect(utf16ToByteOffset(text, 5)).toBe(5);
    expect(utf16ToByteOffset(text, text.length)).toBe(text.length);
  });

  it("counts two bytes per Cyrillic letter", () => {
    // "Привет" is six characters and twelve bytes.
    expect(utf16ToByteOffset(CYRILLIC, 6)).toBe(12);
    // Up to and including ", " — twelve bytes of letters plus two of ASCII.
    expect(utf16ToByteOffset(CYRILLIC, 8)).toBe(14);
    expect(utf16ToByteOffset(CYRILLIC, CYRILLIC.length)).toBe(byteLength(CYRILLIC));
  });

  it("does not confuse character count with byte count", () => {
    // The whole point, stated as one assertion: for this string the two numbers
    // differ, and using the JS index as the byte offset would be off by 20 —
    // one extra byte for each of the twenty Cyrillic letters.
    expect(CYRILLIC.length).toBe(26);
    expect(byteLength(CYRILLIC)).toBe(46);
  });

  it("counts four bytes for an astral character", () => {
    const text = "a😀b"; // one ASCII, one four-byte emoji (two UTF-16 units), one ASCII
    expect(text.length).toBe(4);
    expect(utf16ToByteOffset(text, 1)).toBe(1);
    expect(utf16ToByteOffset(text, 3)).toBe(5);
    expect(utf16ToByteOffset(text, 4)).toBe(6);
  });

  it("never splits a surrogate pair", () => {
    const text = "a😀b";
    // Index 2 is the low half of the emoji. Encoding a lone surrogate would
    // silently produce U+FFFD — three bytes of a character not in the document.
    expect(utf16ToByteOffset(text, 2)).toBe(1);
  });

  it("clamps out-of-range indices instead of throwing", () => {
    expect(utf16ToByteOffset(CYRILLIC, -5)).toBe(0);
    expect(utf16ToByteOffset(CYRILLIC, 9999)).toBe(byteLength(CYRILLIC));
  });
});

describe("byteToUtf16Offset", () => {
  it("inverts utf16ToByteOffset for every position in a Cyrillic string", () => {
    for (let i = 0; i <= CYRILLIC.length; i++) {
      const bytes = utf16ToByteOffset(CYRILLIC, i);
      expect(byteToUtf16Offset(CYRILLIC, bytes)).toBe(i);
    }
  });

  it("inverts utf16ToByteOffset across a mixed-width string", () => {
    const text = "abВГ😀дe";
    for (let i = 0; i <= text.length; i++) {
      const snapped = snapToCodePoint(text, i);
      const bytes = utf16ToByteOffset(text, i);
      expect(byteToUtf16Offset(text, bytes)).toBe(snapped);
    }
  });

  it("resolves a byte offset inside a character to that character's start", () => {
    // Byte 1 is the second half of "П". There is no such position in the string,
    // and answering "the start of П" beats refusing to draw the highlight.
    expect(byteToUtf16Offset(CYRILLIC, 1)).toBe(0);
    expect(byteToUtf16Offset(CYRILLIC, 3)).toBe(1);
  });

  it("clamps a byte offset past the end", () => {
    expect(byteToUtf16Offset(CYRILLIC, 9999)).toBe(CYRILLIC.length);
    expect(byteToUtf16Offset(CYRILLIC, -1)).toBe(0);
  });

  it("agrees with the server's view of a substring", () => {
    // What the API will do with the offsets we send: slice the markdown by
    // bytes. Round-tripping through the encoder proves our numbers name the
    // characters we think they do.
    const source = "Первый абзац.\n\nВторой абзац с важным словом.";
    const quote = "важным словом";
    const at = source.indexOf(quote);
    const start = utf16ToByteOffset(source, at);
    const end = utf16ToByteOffset(source, at + quote.length);

    const bytes = new TextEncoder().encode(source);
    const sliced = new TextDecoder().decode(bytes.slice(start, end));
    expect(sliced).toBe(quote);
  });
});
