import { describe, expect, it } from "vitest";
import {
  type DocAnchor,
  type DocBlock,
  anchorFromHash,
  anchorToHash,
  buildBlocks,
  decodeAnchor,
  encodeAnchor,
  makeAnchor,
  resolveAnchor,
} from "@/lib/docs/anchor";

/**
 * Anchors have exactly two interesting failure modes, and the acceptance
 * criteria for this unit name both:
 *
 *   1. the document is edited **above** the anchor — the link must still land on
 *      the same paragraph;
 *   2. the **anchored paragraph itself** is edited — the outcome must be
 *      defined, and it must never be a silent jump to a different paragraph.
 *
 * Every test below is one of those two, or the machinery they rest on. Two of
 * them carry an explicit negative control: a scheme that this module rejects
 * (resolving by stored offset) is run against the same fixture and shown to give
 * the wrong answer. Without that control the "survives an edit above" test would
 * pass just as happily on a fixture where nothing actually moved.
 */

const DOC = [
  "Introduction to the deploy runbook",
  "The migration is applied before the image swap, never after.",
  "Rollback is: revert the image first, then migrate.",
  "Contact the on-call lead if the gate refuses.",
];

function blocksOf(texts: readonly string[]): DocBlock[] {
  return buildBlocks(texts);
}

function anchorTo(texts: readonly string[], index: number): DocAnchor {
  const anchor = makeAnchor(blocksOf(texts), index);
  if (!anchor) throw new Error(`no block at ${index}`);
  return anchor;
}

/**
 * The scheme this module exists to replace: trust the stored offset. Used only
 * as a negative control — if this and `resolveAnchor` ever agree on the fixtures
 * below, the fixtures stopped exercising the thing under test.
 */
function resolveByPositionOnly(
  blocks: readonly DocBlock[],
  anchor: DocAnchor,
): number | null {
  const index = blocks.findIndex(
    (b) => anchor.start >= b.start && anchor.start <= b.end,
  );
  return index === -1 ? null : index;
}

describe("text projection", () => {
  it("collapses the whitespace a markdown wrap introduces", () => {
    const [block] = blocksOf(["a  paragraph\nwrapped   across lines "]);
    expect(block!.text).toBe("a paragraph wrapped across lines");
  });

  it("lays blocks out consecutively with one separator between them", () => {
    const blocks = blocksOf(["ab", "cde"]);
    expect(blocks[0]).toMatchObject({ start: 0, end: 2 });
    expect(blocks[1]).toMatchObject({ start: 3, end: 6 });
  });
});

describe("an unedited document", () => {
  it("resolves to the same paragraph, and says nothing moved", () => {
    const anchor = anchorTo(DOC, 2);
    expect(resolveAnchor(blocksOf(DOC), anchor)).toEqual({
      status: "exact",
      index: 2,
    });
  });
});

describe("criterion 1 — the document is edited ABOVE the anchor", () => {
  // Two paragraphs inserted above, and the first one rewritten: everything below
  // shifts, so every stored offset in the anchor is now stale.
  const EDITED_ABOVE = [
    "Introduction to the deploy runbook, rewritten and now considerably longer",
    "A newly inserted paragraph about the CI gate.",
    "A second newly inserted paragraph.",
    "The migration is applied before the image swap, never after.",
    "Rollback is: revert the image first, then migrate.",
    "Contact the on-call lead if the gate refuses.",
  ];

  it("still lands on the anchored paragraph", () => {
    const anchor = anchorTo(DOC, 2);
    const match = resolveAnchor(blocksOf(EDITED_ABOVE), anchor);

    expect(match.status).toBe("moved");
    expect(match.index).toBe(4);
    expect(blocksOf(EDITED_ABOVE)[match.index!]!.text).toBe(DOC[2]);
  });

  it("NEGATIVE CONTROL: the stored offset now points at a different paragraph", () => {
    // If this ever comes out equal, the fixture stopped shifting anything and
    // the test above proves nothing.
    const anchor = anchorTo(DOC, 2);
    const blocks = blocksOf(EDITED_ABOVE);
    const byPosition = resolveByPositionOnly(blocks, anchor);

    expect(byPosition).not.toBeNull();
    expect(byPosition).not.toBe(4);
    expect(blocks[byPosition!]!.text).not.toBe(DOC[2]);
  });
});

describe("criterion 2 — the ANCHORED paragraph itself is edited", () => {
  const EDITED_SELF = [
    DOC[0]!,
    DOC[1]!,
    "Rollback is: revert the image first, then migrate, and only then redeploy.",
    DOC[3]!,
  ];

  it("reports the paragraph as changed rather than pretending it is intact", () => {
    const anchor = anchorTo(DOC, 2);
    const match = resolveAnchor(blocksOf(EDITED_SELF), anchor);

    expect(match.status).toBe("edited");
    expect(match.index).toBe(2);
  });

  it("the block it reveals is genuinely not the anchored text any more", () => {
    // Guards the claim the status makes: `edited` must not be reachable for a
    // block that would have matched the quote.
    const anchor = anchorTo(DOC, 2);
    const blocks = blocksOf(EDITED_SELF);
    const match = resolveAnchor(blocks, anchor);

    expect(blocks[match.index!]!.text).not.toBe(anchor.exact);
  });

  // Same shape as DOC, so a stored offset still falls inside a real paragraph —
  // the case where refusing to answer is the whole value of the scheme.
  const GUTTED = [
    DOC[0]!,
    DOC[1]!,
    "Something else entirely, and rather longer than it was before.",
    "And another thing, which is not the on-call line any more.",
  ];

  it("says lost — not a wrong paragraph — when the surroundings changed too", () => {
    const anchor = anchorTo(DOC, 2);

    expect(resolveAnchor(blocksOf(GUTTED), anchor)).toEqual({
      status: "lost",
      index: null,
    });
  });

  it("says lost when the anchored paragraph was deleted outright", () => {
    const anchor = anchorTo(DOC, 2);
    const without = [DOC[0]!, DOC[1]!, DOC[3]!];

    expect(resolveAnchor(blocksOf(without), anchor).status).toBe("lost");
  });

  it("NEGATIVE CONTROL: the stored offset would have answered confidently", () => {
    // The whole point of `lost`: the position-only scheme has an answer here,
    // and the answer is a paragraph the link was never made from.
    const anchor = anchorTo(DOC, 2);
    const blocks = blocksOf(GUTTED);

    const byPosition = resolveByPositionOnly(blocks, anchor);
    expect(byPosition).not.toBeNull();
    expect(blocks[byPosition!]!.text).not.toBe(DOC[2]);
  });
});

describe("paragraphs that read the same", () => {
  const REPEATED = [
    "Preamble one.",
    "Run the migration.",
    "Preamble two.",
    "Run the migration.",
    "Afterword.",
  ];

  it("picks the occurrence whose neighbours agree, not the first one", () => {
    const anchor = anchorTo(REPEATED, 3);
    expect(resolveAnchor(blocksOf(REPEATED), anchor)).toEqual({
      status: "exact",
      index: 3,
    });
  });

  it("keeps picking it after the text above grows", () => {
    const shifted = ["A new opening paragraph.", ...REPEATED];
    const anchor = anchorTo(REPEATED, 3);
    const match = resolveAnchor(blocksOf(shifted), anchor);

    expect(match.status).toBe("moved");
    expect(match.index).toBe(4);
  });
});

describe("a paragraph with no text of its own", () => {
  // An image-only paragraph projects to "", so there is no quote to match and
  // the frame is all there is.
  const WITH_IMAGE = ["Above the image.", "", "Below the image."];

  it("is not reported as edited while the document is untouched", () => {
    const anchor = anchorTo(WITH_IMAGE, 1);
    expect(resolveAnchor(blocksOf(WITH_IMAGE), anchor)).toEqual({
      status: "exact",
      index: 1,
    });
  });

  it("follows its frame when the text above changes", () => {
    const anchor = anchorTo(WITH_IMAGE, 1);
    const shifted = ["A new first paragraph.", ...WITH_IMAGE];
    const match = resolveAnchor(blocksOf(shifted), anchor);

    expect(match.status).toBe("moved");
    expect(match.index).toBe(2);
  });
});

describe("long paragraphs", () => {
  it("matches on the leading slice plus the full length, so the cap is not a hole", () => {
    const long = `${"word ".repeat(60)}tail`;
    const doc = ["Above.", long, "Below."];
    const anchor = anchorTo(doc, 1);

    expect(anchor.exact.length).toBeLessThan(long.length);
    expect(resolveAnchor(blocksOf(doc), anchor).status).toBe("exact");
  });

  it("does not match a paragraph that only shares the leading slice", () => {
    const long = `${"word ".repeat(60)}tail`;
    const anchor = anchorTo(["Above.", long, "Below."], 1);
    // Same opening, different length — the length carried by end-start rejects it.
    const doc = ["Above.", `${"word ".repeat(60)}a different tail`, "Below."];

    expect(resolveAnchor(blocksOf(doc), anchor).status).toBe("edited");
  });
});

describe("the URL fragment", () => {
  it("survives a round trip, Cyrillic included", () => {
    const anchor = anchorTo(["Первый абзац.", "Второй абзац — с тире.", "Третий."], 1);
    expect(decodeAnchor(encodeAnchor(anchor))).toEqual(anchor);
  });

  it("is URL-safe: no characters that need escaping in a hash", () => {
    const encoded = encodeAnchor(anchorTo(["Первый абзац.", "Второй."], 0));
    expect(encoded).toMatch(/^[A-Za-z0-9_-]+$/);
  });

  it("reads back out of a location hash", () => {
    const anchor = anchorTo(DOC, 1);
    expect(anchorFromHash(anchorToHash(anchor))).toEqual(anchor);
  });

  it("returns null for a hash that carries no anchor", () => {
    expect(anchorFromHash("")).toBeNull();
    expect(anchorFromHash("#")).toBeNull();
    expect(anchorFromHash("#section-two")).toBeNull();
  });

  it("returns null rather than a half-built anchor for a mangled one", () => {
    expect(decodeAnchor("not-base64!!")).toBeNull();
    expect(decodeAnchor(btoa("[1,2,3]"))).toBeNull();
    expect(decodeAnchor(btoa(JSON.stringify({ s: 0 })))).toBeNull();
    expect(decodeAnchor(btoa(JSON.stringify({ s: 5, e: 1, x: "", p: "", f: "" })))).toBeNull();
  });

  it("clamps a hand-widened quote back to what this module writes", () => {
    const oversized = btoa(
      JSON.stringify({ s: 0, e: 10, x: "x".repeat(500), p: "", f: "" }),
    );
    expect(decodeAnchor(oversized)!.exact.length).toBe(96);
  });
});

describe("makeAnchor", () => {
  it("returns null for an index that is not a block", () => {
    expect(makeAnchor(blocksOf(DOC), 99)).toBeNull();
    expect(makeAnchor(blocksOf(DOC), -1)).toBeNull();
  });

  it("leaves the context empty at the edges of the document", () => {
    expect(anchorTo(DOC, 0).prefix).toBe("");
    expect(anchorTo(DOC, DOC.length - 1).suffix).toBe("");
  });
});
