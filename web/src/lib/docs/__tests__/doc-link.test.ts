import { describe, expect, it } from "vitest";
import {
  DOC_LINK_TRIGGER,
  applyDocLinkInsertion,
  documentHref,
  documentMarkdownLink,
  findDocLinkTrigger,
  linkLabel,
  matchDocuments,
} from "@/lib/docs/doc-link";

/**
 * Linking to a document from a task description or a comment.
 *
 * The two things that can go wrong quietly are here: a link that renders as
 * nothing because the title contained a bracket, and a menu that stays open
 * after the writer has moved on and then rewrites text somewhere else. Both are
 * silent — the writer sees a link that never appeared, or a sentence that
 * changed under them — so both are pinned.
 */

const DOCS = [
  { id: "d1", title: "Deploy runbook" },
  { id: "d2", title: "Runbook for incidents" },
  { id: "d3", title: "Onboarding" },
  { id: "d4", title: "runbook archive" },
];

describe("the link itself", () => {
  it("is the document's real route", () => {
    expect(documentHref("acme", "demo", "doc-1")).toBe("/w/acme/p/demo/docs/doc-1");
  });

  it("is an ordinary markdown link", () => {
    expect(documentMarkdownLink("Deploy runbook", "acme", "demo", "doc-1")).toBe(
      "[Deploy runbook](/w/acme/p/demo/docs/doc-1)",
    );
  });

  it("survives the renderer's own link pattern", () => {
    // The dialect this app renders, applied to what we produce. If the two ever
    // disagree the link silently becomes literal text, and nothing else would
    // catch it.
    const md = documentMarkdownLink("Deploy runbook", "acme", "demo", "doc-1");
    const match = /\[([^\]]+)\]\(([^)]+)\)/.exec(md);

    expect(match).not.toBeNull();
    expect(match![1]).toBe("Deploy runbook");
    expect(match![2]).toBe("/w/acme/p/demo/docs/doc-1");
  });
});

describe("titles the dialect cannot express", () => {
  it("strips brackets rather than producing a link that renders as nothing", () => {
    expect(linkLabel("Deploy [runbook]")).toBe("Deploy runbook");
  });

  it("NEGATIVE CONTROL: the unstripped title really does fail to render", () => {
    // What the strip is buying. A bracketed label makes the renderer's pattern
    // fail outright — the whole construct falls through as literal text — so
    // "we lost two characters" is the better of the two outcomes, not a
    // preference.
    const naive = "[Deploy [runbook]](/w/acme/p/demo/docs/doc-1)";
    expect(/\[([^\]]+)\]\(([^)]+)\)/.exec(naive)).toBeNull();

    const stripped = documentMarkdownLink("Deploy [runbook]", "acme", "demo", "doc-1");
    expect(/\[([^\]]+)\]\(([^)]+)\)/.exec(stripped)).not.toBeNull();
  });

  it("falls back to a word rather than an empty label", () => {
    // "[](/…)" renders as nothing at all — an invisible link.
    expect(linkLabel("[]")).toBe("Untitled");
    expect(linkLabel("   ")).toBe("Untitled");
  });
});

describe("the trigger", () => {
  it("opens on the sequence itself, with an empty query", () => {
    const text = `See ${DOC_LINK_TRIGGER}`;
    expect(findDocLinkTrigger(text, text.length)).toEqual({ start: 4, query: "" });
  });

  it("carries what has been typed since", () => {
    const text = "See [[run";
    expect(findDocLinkTrigger(text, text.length)).toEqual({ start: 4, query: "run" });
  });

  it("is not open when there is no trigger", () => {
    expect(findDocLinkTrigger("just some prose", 15)).toBeNull();
    // A single bracket is markdown a writer types all the time.
    expect(findDocLinkTrigger("a [link](x)", 11)).toBeNull();
  });

  it("closes at a newline — the writer has moved on", () => {
    // Left open, the next Enter would rewrite a line the caret has left.
    expect(findDocLinkTrigger("[[run\nmore text", 15)).toBeNull();
  });

  it("closes once the brackets are closed", () => {
    expect(findDocLinkTrigger("[[run]] and more", 16)).toBeNull();
  });

  it("reads the caret, not the end of the text", () => {
    // The writer went back to fix an earlier sentence; the trigger is where the
    // caret is, not wherever the last `[[` happens to sit.
    const text = "[[run and later [[deploy";
    expect(findDocLinkTrigger(text, 5)).toEqual({ start: 0, query: "run" });
  });
});

describe("accepting a suggestion", () => {
  const link = "[Deploy runbook](/w/acme/p/demo/docs/d1)";

  it("replaces the trigger and everything typed after it", () => {
    const text = "See [[run for details";
    const caret = 9; // just after "run"
    const trigger = findDocLinkTrigger(text, caret)!;

    const out = applyDocLinkInsertion(text, trigger, caret, link);

    expect(out.value).toBe(`See ${link} for details`);
  });

  it("leaves the caret past the link, ready for the next word", () => {
    const text = "See [[run";
    const trigger = findDocLinkTrigger(text, text.length)!;

    const out = applyDocLinkInsertion(text, trigger, text.length, link);

    expect(out.value).toBe(`See ${link} `);
    expect(out.caret).toBe(out.value.length);
    // And specifically NOT inside the URL, which is where a naive
    // "insert at cursor" leaves it.
    expect(out.value.slice(out.caret)).toBe("");
  });

  it("keeps the text after the caret intact", () => {
    const text = "before [[q after";
    const caret = 10;
    const trigger = findDocLinkTrigger(text, caret)!;

    expect(applyDocLinkInsertion(text, trigger, caret, link).value).toBe(
      `before ${link} after`,
    );
  });

  it("does not add a second space when the sentence already has one", () => {
    // Caught by the test above before it was true: appending unconditionally
    // leaves a stray space in the middle of the writer's prose, every time the
    // link is dropped into a gap rather than at the end.
    const text = "See [[run for details";
    const caret = 9;
    const trigger = findDocLinkTrigger(text, caret)!;

    const out = applyDocLinkInsertion(text, trigger, caret, link);

    expect(out.value).toBe(`See ${link} for details`);
    expect(out.value).not.toContain("  ");
  });
});

describe("matching documents", () => {
  it("offers everything on an empty query, so the menu browses as well as searches", () => {
    expect(matchDocuments(DOCS, "")).toHaveLength(4);
  });

  it("is case-insensitive", () => {
    expect(matchDocuments(DOCS, "RUNBOOK").map((d) => d.id).sort()).toEqual([
      "d1",
      "d2",
      "d4",
    ]);
  });

  it("puts a title that STARTS with the query first", () => {
    const found = matchDocuments(DOCS, "runbook");
    // "Runbook for incidents" and "runbook archive" both start with it;
    // "Deploy runbook" merely contains it and comes last.
    expect(found[found.length - 1]!.id).toBe("d1");
  });

  it("breaks ties alphabetically IGNORING case, so the list reads as sorted", () => {
    const found = matchDocuments(DOCS, "runbook");
    // By the words, not by which title happens to be capitalised: "archive"
    // before "for". A list that put every capitalised title first would read as
    // unsorted to anyone scanning it.
    expect(found.slice(0, 2).map((d) => d.title)).toEqual([
      "runbook archive",
      "Runbook for incidents",
    ]);
  });

  it("caps the list", () => {
    const many = Array.from({ length: 50 }, (_, i) => ({
      id: `d${i}`,
      title: `Doc ${i}`,
    }));
    expect(matchDocuments(many, "doc")).toHaveLength(8);
    expect(matchDocuments(many, "doc", 3)).toHaveLength(3);
  });

  it("returns nothing when nothing matches, rather than everything", () => {
    // The failure mode of a filter written the other way round: an unmatched
    // query falls back to the whole list and the writer picks the wrong doc.
    expect(matchDocuments(DOCS, "zzzz")).toEqual([]);
  });
});
