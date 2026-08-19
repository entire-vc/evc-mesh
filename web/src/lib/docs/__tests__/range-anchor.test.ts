import { describe, expect, it } from "vitest";
import {
  CONTEXT_CAP,
  EXACT_CAP,
  type DocAnchor,
  buildBlocks,
  decodeAnchor,
  encodeAnchor,
  makeRangeAnchor,
  projectionOf,
  resolveAnchor,
  resolveRangeAnchor,
} from "@/lib/docs/anchor";

/**
 * Range anchors — what a comment hangs off.
 *
 * The acceptance criterion for the unit these serve is one sentence: after the
 * text around a comment is edited it either stays on its fragment or is honestly
 * marked as detached, and it must never quietly move onto someone else's words.
 * So the tests come in three groups — it survives edits around it, it says so
 * when its own text changed, and it refuses to answer when it cannot know — plus
 * negative controls proving each fixture actually exercises what it claims.
 */

const DOC = [
  "Deploy discipline",
  "The migration is applied before the image swap, never after.",
  "Rollback is: revert the image first, then migrate.",
  "Contact the on-call lead if the gate refuses.",
];

const TEXT = projectionOf(buildBlocks(DOC));

/** The anchor for a quoted phrase, found by searching for it. */
function anchorForPhrase(text: string, phrase: string): DocAnchor {
  const start = text.indexOf(phrase);
  if (start === -1) throw new Error(`phrase not in fixture: ${phrase}`);
  const anchor = makeRangeAnchor(text, start, start + phrase.length);
  if (!anchor) throw new Error("anchor not built");
  return anchor;
}

const PHRASE = "revert the image first";

describe("making a range anchor", () => {
  it("quotes the selection and both of its shoulders", () => {
    const anchor = anchorForPhrase(TEXT, PHRASE);

    expect(anchor.exact).toBe(PHRASE);
    expect(TEXT.slice(anchor.start, anchor.end)).toBe(PHRASE);
    expect(anchor.prefix.endsWith("Rollback is: ")).toBe(true);
    expect(anchor.suffix.startsWith(", then migrate.")).toBe(true);
  });

  it("caps what travels in a link, at both ends", () => {
    const long = "x".repeat(500);
    const text = `${"before ".repeat(20)}${long}${" after".repeat(20)}`;
    const start = text.indexOf(long);
    const anchor = makeRangeAnchor(text, start, start + long.length)!;

    expect(anchor.exact).toHaveLength(EXACT_CAP);
    expect(anchor.prefix).toHaveLength(CONTEXT_CAP);
    expect(anchor.suffix).toHaveLength(CONTEXT_CAP);
    // The full length still travels, in end - start, which is what keeps the cap
    // from turning into "any passage that starts the same way".
    expect(anchor.end - anchor.start).toBe(long.length);
  });

  it("refuses a range that is not inside the text", () => {
    expect(makeRangeAnchor("short", 2, 99)).toBeNull();
    expect(makeRangeAnchor("short", -1, 3)).toBeNull();
    expect(makeRangeAnchor("short", 4, 2)).toBeNull();
  });
});

describe("the document is edited AROUND the comment", () => {
  const EDITED_AROUND = [
    "Deploy discipline, revised",
    "A paragraph inserted above.",
    "The migration is applied before the image swap, never after, and never in the same release.",
    "Rollback is: revert the image first, then migrate.",
    "Contact the on-call lead if the gate refuses.",
    "A closing paragraph added below.",
  ];

  it("still points at the same words", () => {
    const anchor = anchorForPhrase(TEXT, PHRASE);
    const edited = projectionOf(buildBlocks(EDITED_AROUND));

    const match = resolveRangeAnchor(edited, anchor);

    expect(match.status).toBe("moved");
    expect(edited.slice(match.start!, match.end!)).toBe(PHRASE);
  });

  it("NEGATIVE CONTROL: the stored offset now covers different words", () => {
    // Without this, "survives an edit around it" would pass on a fixture where
    // nothing actually moved.
    const anchor = anchorForPhrase(TEXT, PHRASE);
    const edited = projectionOf(buildBlocks(EDITED_AROUND));

    const byOffset = edited.slice(anchor.start, anchor.end);

    expect(byOffset).not.toBe(PHRASE);
  });

  it("says nothing moved when nothing did", () => {
    const anchor = anchorForPhrase(TEXT, PHRASE);
    expect(resolveRangeAnchor(TEXT, anchor).status).toBe("exact");
  });
});

describe("the commented text ITSELF is edited", () => {
  it("marks it edited, at the place it used to occupy", () => {
    const anchor = anchorForPhrase(TEXT, PHRASE);
    const rewritten = projectionOf(
      buildBlocks(
        DOC.map((b) => b.replace(PHRASE, "roll the image back")),
      ),
    );

    const match = resolveRangeAnchor(rewritten, anchor);

    expect(match.status).toBe("edited");
    // The place is right — and the text there is provably NOT what was quoted,
    // which is the whole reason the status has to be surfaced to the reader.
    expect(rewritten.slice(match.start!, match.end!)).toBe("roll the image back");
    expect(rewritten.slice(match.start!, match.end!)).not.toBe(anchor.exact);
  });

  it("refuses when the surrounding text changed too", () => {
    const anchor = anchorForPhrase(TEXT, PHRASE);
    const gutted = projectionOf(
      buildBlocks([
        "Deploy discipline",
        "The migration is applied before the image swap, never after.",
        "Recovery is a different procedure entirely now.",
        "Ask in the channel if the gate refuses.",
      ]),
    );

    expect(resolveRangeAnchor(gutted, anchor)).toEqual({
      status: "lost",
      start: null,
      end: null,
    });
  });

  it("refuses rather than claiming a half-document range", () => {
    // Both shoulders survive but are now far apart. Reporting the everything in
    // between as "the edited fragment" would be consistent with the evidence and
    // useless to a reader, so the gap is bounded.
    const anchor = anchorForPhrase(TEXT, PHRASE);
    const stretched = projectionOf(
      buildBlocks([
        "Deploy discipline",
        "The migration is applied before the image swap, never after.",
        `Rollback is: ${"filler ".repeat(300)}, then migrate.`,
        "Contact the on-call lead if the gate refuses.",
      ]),
    );

    expect(resolveRangeAnchor(stretched, anchor).status).toBe("lost");
  });
});

describe("two passages that read the same", () => {
  const REPEATED = [
    "First section.",
    "Run the migration.",
    "Second section.",
    "Run the migration.",
    "Closing.",
  ];
  const REPEATED_TEXT = projectionOf(buildBlocks(REPEATED));

  it("picks the occurrence whose surroundings agree, not the first", () => {
    const second = REPEATED_TEXT.lastIndexOf("Run the migration.");
    const anchor = makeRangeAnchor(REPEATED_TEXT, second, second + 18)!;

    const shifted = projectionOf(buildBlocks(["A new opening.", ...REPEATED]));
    const match = resolveRangeAnchor(shifted, anchor);

    expect(match.status).toBe("moved");
    // The one after "Second section.", not the one after "First section."
    expect(shifted.slice(0, match.start!)).toContain("Second section.");
  });
});

describe("the block wrapper still only ever answers with a whole block", () => {
  it("does not accept a paragraph that merely opens the same way", () => {
    // The quote is capped, so beyond EXACT_CAP the two paragraphs are
    // indistinguishable by quote alone. The block caller knows the answer has to
    // line up with a block boundary, and that is what rejects this.
    const long = `${"word ".repeat(60)}tail`;
    const blocks = buildBlocks(["Above.", long, "Below."]);
    const anchor = makeRangeAnchor(projectionOf(blocks), blocks[1]!.start, blocks[1]!.end)!;

    const changed = buildBlocks(["Above.", `${"word ".repeat(60)}a different tail`, "Below."]);

    expect(resolveAnchor(changed, anchor).status).toBe("edited");
  });

  it("NEGATIVE CONTROL: the general resolver, having no such rule, does accept it", () => {
    // This is what the extra rule is buying, stated as a measurement rather than
    // as a claim in a comment. If this ever comes out "edited" too, the block
    // rule has stopped doing anything and the test above is vacuous.
    const long = `${"word ".repeat(60)}tail`;
    const blocks = buildBlocks(["Above.", long, "Below."]);
    const anchor = makeRangeAnchor(projectionOf(blocks), blocks[1]!.start, blocks[1]!.end)!;

    const changedText = projectionOf(
      buildBlocks(["Above.", `${"word ".repeat(60)}a different tail`, "Below."]),
    );

    expect(resolveRangeAnchor(changedText, anchor).status).toBe("exact");
  });
});

describe("a range anchor in a URL", () => {
  it("round-trips through the same encoding a paragraph link uses", () => {
    const anchor = anchorForPhrase(TEXT, PHRASE);
    expect(decodeAnchor(encodeAnchor(anchor))).toEqual(anchor);
  });
});
