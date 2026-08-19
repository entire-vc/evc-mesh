import { describe, expect, it } from "vitest";
import {
  anchorHintFraction,
  buildAnchorFromSelection,
  locateQuoteInRendered,
  locateQuoteInSource,
} from "@/lib/doc-comments/anchor";
import { byteLength } from "@/lib/doc-comments/offsets";

/** What the API will do with the offsets: slice the markdown by bytes. */
function sliceBytes(source: string, start: number, end: number): string {
  const bytes = new TextEncoder().encode(source);
  return new TextDecoder().decode(bytes.slice(start, end));
}

/** A selection of `quote` in `rendered`, as a UTF-16 span. */
function select(rendered: string, quote: string, from = 0) {
  const start = rendered.indexOf(quote, from);
  if (start === -1) throw new Error(`"${quote}" is not in the rendered text`);
  return { start, end: start + quote.length };
}

describe("buildAnchorFromSelection — plain prose", () => {
  const source = "The first paragraph.\n\nThe second paragraph is longer.";

  it("produces byte offsets that slice back to the quote", () => {
    const built = buildAnchorFromSelection(source, source, select(source, "second paragraph"));
    expect(built).not.toBeNull();
    expect(built!.unplaceable).toBe(false);
    expect(built!.anchor.exact).toBe("second paragraph");
    expect(sliceBytes(source, built!.anchor.start!, built!.anchor.end!)).toBe(
      "second paragraph",
    );
  });

  it("keeps neighbouring text either side of the quote", () => {
    const built = buildAnchorFromSelection(source, source, select(source, "second"));
    expect(built!.anchor.prefix.endsWith("The ")).toBe(true);
    expect(built!.anchor.suffix.startsWith(" paragraph")).toBe(true);
  });

  it("trims whitespace the reader dragged over", () => {
    const built = buildAnchorFromSelection(source, source, { start: 3, end: 10 });
    expect(built!.anchor.exact).toBe("first");
  });

  it("refuses a selection that is only whitespace", () => {
    expect(buildAnchorFromSelection(source, source, { start: 20, end: 22 })).toBeNull();
  });
});

describe("buildAnchorFromSelection — Cyrillic", () => {
  // The case the whole offsets module exists for. Every character here is two
  // bytes, so a JS index used as a byte offset is off by a factor of two.
  const source =
    "# Кириллица и якоря\n\nПервый абзац документа.\n\nВторой абзац содержит важное слово.";

  it("sends byte offsets, not string indices", () => {
    const quote = "важное слово";
    const built = buildAnchorFromSelection(source, source, select(source, quote));
    expect(built!.unplaceable).toBe(false);

    const charIndex = source.indexOf(quote);
    // If this ever passes, the conversion has been removed: the byte offset for
    // a quote this far into a Cyrillic document is nearly twice its index.
    expect(built!.anchor.start).not.toBe(charIndex);
    expect(built!.anchor.start).toBeGreaterThan(charIndex);
  });

  it("produces offsets the server can slice back to the exact quote", () => {
    const quote = "важное слово";
    const built = buildAnchorFromSelection(source, source, select(source, quote));
    expect(sliceBytes(source, built!.anchor.start!, built!.anchor.end!)).toBe(quote);
  });

  it("is correct for a quote at the very start and the very end", () => {
    const first = buildAnchorFromSelection(source, source, select(source, "Кириллица"));
    expect(sliceBytes(source, first!.anchor.start!, first!.anchor.end!)).toBe("Кириллица");

    const last = buildAnchorFromSelection(source, source, select(source, "слово."));
    expect(sliceBytes(source, last!.anchor.start!, last!.anchor.end!)).toBe("слово.");
    expect(last!.anchor.end).toBe(byteLength(source));
  });

  it("tells two occurrences of the same word apart by their context", () => {
    const repeated =
      "Первый абзац содержит слово здесь.\n\nВторой абзац содержит слово там.";
    const secondOccurrence = repeated.indexOf("слово", repeated.indexOf("Второй"));
    const built = buildAnchorFromSelection(repeated, repeated, {
      start: secondOccurrence,
      end: secondOccurrence + "слово".length,
    });
    // Both occurrences are byte-identical; only the prefix/suffix distinguish
    // them, and picking the first would put the highlight in the wrong paragraph.
    expect(built!.anchor.start).toBe(
      new TextEncoder().encode(repeated.slice(0, secondOccurrence)).length,
    );
    expect(sliceBytes(repeated, built!.anchor.start!, built!.anchor.end!)).toBe("слово");
  });
});

describe("buildAnchorFromSelection — rendered text differs from the markdown", () => {
  it("places a quote that crossed a bold run", () => {
    const source = "Deploys are **blocked** until Friday.";
    const rendered = "Deploys are blocked until Friday.";
    const built = buildAnchorFromSelection(source, rendered, select(rendered, "blocked until"));

    expect(built!.unplaceable).toBe(false);
    // The stored quote is what the reader saw; the offsets bound the markdown
    // that produced it, asterisks and all.
    expect(built!.anchor.exact).toBe("blocked until");
    expect(sliceBytes(source, built!.anchor.start!, built!.anchor.end!)).toBe(
      "blocked** until",
    );
  });

  it("places a quote that crossed inline code", () => {
    const source = "Run `make ci` before pushing.";
    const rendered = "Run make ci before pushing.";
    const built = buildAnchorFromSelection(source, rendered, select(rendered, "make ci before"));
    expect(built!.unplaceable).toBe(false);
    expect(sliceBytes(source, built!.anchor.start!, built!.anchor.end!)).toBe(
      "make ci` before",
    );
  });

  it("places a quote that crossed a soft line break", () => {
    const source = "One sentence\nwrapped in the source.";
    const rendered = "One sentence wrapped in the source.";
    const built = buildAnchorFromSelection(source, rendered, select(rendered, "sentence wrapped"));
    expect(built!.unplaceable).toBe(false);
    expect(sliceBytes(source, built!.anchor.start!, built!.anchor.end!)).toBe(
      "sentence\nwrapped",
    );
  });

  it("stores the quote without offsets when it cannot be placed at all", () => {
    // A quote that is simply not in the source — an image's alt text rendered
    // as a caption, say. The API's third legal state, and better than inventing
    // a position.
    const built = buildAnchorFromSelection(
      "Nothing to see here.",
      "A caption that came from somewhere else",
      { start: 2, end: 9 },
    );
    expect(built!.unplaceable).toBe(true);
    expect(built!.anchor.start).toBeNull();
    expect(built!.anchor.end).toBeNull();
    expect(built!.anchor.exact).toBe("caption");
  });
});

describe("locateQuoteInSource", () => {
  it("prefers the occurrence whose context agrees", () => {
    const source = "alpha TARGET omega\n\nbeta TARGET gamma";
    const found = locateQuoteInSource(source, "TARGET", "beta ", " gamma");
    expect(source.slice(found!.start, found!.end)).toBe("TARGET");
    expect(found!.start).toBe(source.lastIndexOf("TARGET"));
  });

  it("returns null rather than guessing when the quote is gone", () => {
    expect(locateQuoteInSource("some other text", "vanished", "", "")).toBeNull();
  });
});

describe("locateQuoteInRendered", () => {
  const rendered = "Первый абзац со словом.\nВторой абзац со словом.";

  it("finds the quote and its span", () => {
    const span = locateQuoteInRendered(rendered, {
      exact: "Первый абзац",
      prefix: "",
      suffix: " со словом",
    });
    expect(rendered.slice(span!.start, span!.end)).toBe("Первый абзац");
  });

  it("uses context to pick between repeats", () => {
    const span = locateQuoteInRendered(rendered, {
      exact: "со словом",
      prefix: "Второй абзац ",
      suffix: ".",
    });
    expect(span!.start).toBe(rendered.lastIndexOf("со словом"));
  });

  it("returns null when the text is no longer in the document", () => {
    expect(
      locateQuoteInRendered("совершенно другой текст", {
        exact: "со словом",
        prefix: "",
        suffix: "",
      }),
    ).toBeNull();
  });
});

describe("anchorHintFraction", () => {
  it("is null for an orphaned anchor", () => {
    expect(anchorHintFraction("Привет", { start: null })).toBeNull();
  });

  it("expresses the byte offset as a share of the document's bytes", () => {
    const source = "Привет"; // 12 bytes
    expect(anchorHintFraction(source, { start: 6 })).toBeCloseTo(0.5);
  });
});
