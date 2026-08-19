import { describe, expect, it } from "vitest";
import { MATCH_END, MATCH_START, splitSnippet } from "@/lib/docs/document-search";

/**
 * Splitting a server snippet into plain and matched runs.
 *
 * This exists instead of dangerouslySetInnerHTML: the snippet is a fragment of a
 * document somebody wrote, and handing it to the DOM as markup would make every
 * document body an injection surface for everyone who can read it.
 */

const mark = (s: string) => `${MATCH_START}${s}${MATCH_END}`;

describe("splitSnippet", () => {
  it("marks the matched run and leaves the rest alone", () => {
    expect(splitSnippet(`before ${mark("rollback")} after`)).toEqual([
      { text: "before ", match: false },
      { text: "rollback", match: true },
      { text: " after", match: false },
    ]);
  });

  it("handles several matches", () => {
    expect(splitSnippet(`${mark("a")} and ${mark("b")}`)).toEqual([
      { text: "a", match: true },
      { text: " and ", match: false },
      { text: "b", match: true },
    ]);
  });

  it("returns one plain run when nothing is marked", () => {
    expect(splitSnippet("the opening of a document")).toEqual([
      { text: "the opening of a document", match: false },
    ]);
  });

  it("returns nothing for an empty snippet", () => {
    expect(splitSnippet("")).toEqual([]);
  });

  it("treats an unbalanced marker as plain text, not as a match to the end", () => {
    // A truncated snippet can end mid-marker. Marking the remainder would
    // highlight text the query never touched — which reads exactly like a
    // correct highlight and is not.
    expect(splitSnippet(`start ${MATCH_START}truncated`)).toEqual([
      { text: "start ", match: false },
      { text: "truncated", match: false },
    ]);
  });

  it("never leaves a marker in the output", () => {
    // The marker is a private-use codepoint; rendering one shows a tofu box.
    const parts = splitSnippet(`x ${mark("y")} z ${MATCH_START}w`);
    for (const part of parts) {
      expect(part.text).not.toContain(MATCH_START);
      expect(part.text).not.toContain(MATCH_END);
    }
  });
});
