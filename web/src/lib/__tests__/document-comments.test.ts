import { describe, expect, it } from "vitest";
import {
  type DocumentComment,
  anchorPayload,
  groupIntoThreads,
  positionOf,
} from "@/lib/document-comments";

/**
 * Grouping a flat comment list into threads.
 *
 * The schema allows any depth of reply, so the thing that must not happen is a
 * nested reply going missing: present in the database, invisible in the panel,
 * and therefore impossible to answer or delete. Every test here is a shape that
 * a one-level-of-nesting assumption gets wrong.
 */

function comment(over: Partial<DocumentComment> & { id: string }): DocumentComment {
  return {
    document_id: "doc-1",
    parent_comment_id: null,
    body: over.id,
    author_id: "u1",
    author_type: "user",
    created_at: "2026-08-19T12:00:00Z",
    updated_at: "2026-08-19T12:00:00Z",
    ...over,
  };
}

const ANCHOR = { start: 0, end: 5, exact: "quote", prefix: "", suffix: "" };

describe("groupIntoThreads", () => {
  it("puts a reply under its root", () => {
    const threads = groupIntoThreads([
      comment({ id: "root", anchor: ANCHOR }),
      comment({ id: "reply", parent_comment_id: "root" }),
    ]);

    expect(threads).toHaveLength(1);
    expect(threads[0]!.replies.map((r) => r.id)).toEqual(["reply"]);
  });

  it("puts a reply-to-a-reply under the SAME root, not into a thread of its own", () => {
    const threads = groupIntoThreads([
      comment({ id: "root", anchor: ANCHOR }),
      comment({ id: "reply", parent_comment_id: "root" }),
      comment({ id: "nested", parent_comment_id: "reply" }),
    ]);

    expect(threads).toHaveLength(1);
    // Both, in order. A one-level assumption drops "nested" entirely.
    expect(threads[0]!.replies.map((r) => r.id)).toEqual(["reply", "nested"]);
  });

  it("keeps separate threads separate", () => {
    const threads = groupIntoThreads([
      comment({ id: "a", anchor: ANCHOR }),
      comment({ id: "b", anchor: ANCHOR }),
      comment({ id: "a-reply", parent_comment_id: "a" }),
    ]);

    expect(threads.map((t) => t.root.id)).toEqual(["a", "b"]);
    expect(threads.find((t) => t.root.id === "b")!.replies).toEqual([]);
  });

  it("drops a reply whose root is not in the list rather than inventing one", () => {
    const threads = groupIntoThreads([comment({ id: "orphan", parent_comment_id: "gone" })]);
    expect(threads).toEqual([]);
  });

  it("terminates on a cycle instead of hanging the tab", () => {
    // Unreachable through the API; the loop guard is what makes that a claim
    // about the API rather than a hope about the data.
    const threads = groupIntoThreads([
      comment({ id: "x", parent_comment_id: "y" }),
      comment({ id: "y", parent_comment_id: "x" }),
    ]);
    expect(threads).toEqual([]);
  });
});

describe("positionOf — an orphan has no position to resolve against", () => {
  it("returns the selector pair when the anchor is placed", () => {
    expect(
      positionOf({ exact: "q", prefix: "p", suffix: "s", start: 3, end: 4 }),
    ).toEqual({ start: 3, end: 4, exact: "q", prefix: "p", suffix: "s" });
  });

  it("returns null for an orphan — a quote the API could not place", () => {
    // Not "resolve it anyway and see": the API already established there is no
    // position, and deriving one here would be inventing a verdict.
    expect(
      positionOf({ exact: "q", prefix: "p", suffix: "s", start: null, end: null }),
    ).toBeNull();
  });

  it("returns null for a comment that was never anchored", () => {
    expect(positionOf(null)).toBeNull();
    expect(positionOf(undefined)).toBeNull();
  });
});

describe("anchorPayload — what goes on the wire", () => {
  it("sends the five stored fields and nothing else", () => {
    const payload = anchorPayload({
      start: 1,
      end: 5,
      exact: "quote",
      prefix: "pre",
      suffix: "suf",
    });

    // `orphaned` is computed by the API from whether the offsets are present. A
    // client that sent it would be asserting something the offsets beside it
    // could contradict.
    expect(Object.keys(payload).sort()).toEqual([
      "end",
      "exact",
      "prefix",
      "start",
      "suffix",
    ]);
  });
});
