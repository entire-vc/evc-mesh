import { describe, expect, it } from "vitest";

import { placeAnchor } from "@/lib/doc-comments/use-doc-comments";
import type { DocumentCommentAnchor } from "@/types";

/**
 * The branch that decides whether a reader keeps their highlight.
 *
 * Context: the server now re-anchors every comment when a document body is
 * saved, so `start` MOVES and can become null (evc-mesh #90dd31f9). Before that
 * it was written once and never touched, and this code short-circuited on a null
 * position — a branch that could not fire for a stored comment and now can.
 */

const anchor = (over: Partial<DocumentCommentAnchor> = {}): DocumentCommentAnchor => ({
  exact: "цитата целиком",
  prefix: "слева ",
  suffix: " справа",
  start: 7,
  end: 7 + "цитата целиком".length,
  orphaned: false,
  ...over,
});

const source = "слева цитата целиком справа\n";
const rendered = "слева цитата целиком справа";

describe("placeAnchor", () => {
  it("anchors a quote that is on the page", () => {
    const { placement, span } = placeAnchor(anchor(), rendered, source);
    expect(placement).toBe("anchored");
    expect(rendered.slice(span!.start, span!.end)).toBe("цитата целиком");
  });

  it("puts a comment with no quote on the page as a whole", () => {
    expect(placeAnchor(null, rendered, source).placement).toBe("page");
    expect(placeAnchor(anchor({ exact: "" }), rendered, source).placement).toBe("page");
  });

  it("detaches a quote that is no longer in the rendered text", () => {
    expect(placeAnchor(anchor(), "совсем другой текст", source).placement).toBe("detached");
  });

  it("still highlights a server-orphaned anchor whose words are on the page", () => {
    // The regression this test exists for. The server orphans when the quote is
    // unfindable in the MARKDOWN; the reader's question is whether it is on the
    // SCREEN. An edit that wraps the words in syntax the markdown scan will not
    // step over answers those two differently, and the reader should not lose a
    // highlight to a question they did not ask.
    const orphanedByServer = anchor({ start: null, end: null, orphaned: true });
    const { placement, span } = placeAnchor(orphanedByServer, rendered, source);
    expect(placement).toBe("anchored");
    expect(rendered.slice(span!.start, span!.end)).toBe("цитата целиком");
  });

  it("reports orphaned rather than detached when the server says so and the words are gone", () => {
    // Both render the same way in the rail; the distinction is which of the two
    // notices the reader gets, and the server's is the better-informed one.
    const orphanedByServer = anchor({ start: null, end: null, orphaned: true });
    expect(placeAnchor(orphanedByServer, "совсем другой текст", source).placement).toBe("orphaned");
  });

  it("uses the position only to choose between identical repeats", () => {
    // Which is why a stale position was a quiet cost even before it could be
    // null: it was already picking the wrong one of two identical lines.
    const repeated = "- one line\n\n- one line";
    const second = repeated.lastIndexOf("- one line");
    const { span } = placeAnchor(
      anchor({ exact: "one line", prefix: "- ", suffix: "", start: second, end: second + 8 }),
      repeated,
      repeated,
    );
    expect(span!.start).toBe(second + 2);
  });
});
