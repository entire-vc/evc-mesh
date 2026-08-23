import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { DocCommentTree, inDocumentOrder } from "@/components/doc-comment-tree";
import type {
  DocCommentsController,
  PlacedThread,
  ThreadPlacement,
} from "@/lib/doc-comments/use-doc-comments";
import type { DocumentComment } from "@/types";

const ME = "11111111-1111-1111-1111-111111111111";

function makeComment(overrides: Partial<DocumentComment> = {}): DocumentComment {
  return {
    id: "c1",
    document_id: "doc-1",
    parent_comment_id: null,
    author_id: ME,
    author_type: "user",
    author_name: "Pavel",
    body: "body",
    anchor: {
      exact: "quote",
      prefix: "",
      suffix: "",
      start: 42,
      end: 47,
      orphaned: false,
    },
    resolved_at: null,
    resolved_by: null,
    resolved_by_type: null,
    created_at: "2026-08-19T10:00:00Z",
    updated_at: "2026-08-19T10:00:00Z",
    ...overrides,
  };
}

/**
 * A thread at a known place in the rendered text.
 *
 * `span` is what document order sorts on, and it is deliberately independent of
 * `created_at` in these fixtures — a fixture where the two agree would let a
 * component that sorts by neither, or by the wrong one, pass.
 */
function makeThread(
  id: string,
  span: { start: number; end: number } | null,
  overrides: {
    placement?: ThreadPlacement;
    createdAt?: string;
    resolvedAt?: string | null;
    body?: string;
  } = {},
): PlacedThread {
  const root = makeComment({
    id,
    body: overrides.body ?? `comment ${id}`,
    created_at: overrides.createdAt ?? "2026-08-19T10:00:00Z",
    updated_at: overrides.createdAt ?? "2026-08-19T10:00:00Z",
    resolved_at: overrides.resolvedAt ?? null,
    anchor: {
      exact: `quote ${id}`,
      prefix: "",
      suffix: "",
      start: span ? span.start : null,
      end: span ? span.end : null,
      orphaned: span === null,
    },
  });
  return {
    root,
    replies: [],
    placement: overrides.placement ?? (span ? "anchored" : "orphaned"),
    span,
    resolved: root.resolved_at !== null,
  };
}

function makeController(
  overrides: Partial<DocCommentsController> = {},
): DocCommentsController {
  const threads = overrides.threads ?? [];
  return {
    containerRef: { current: null },
    threads,
    isLoading: false,
    error: null,
    unresolvedCount: threads.filter((t) => !t.resolved).length,
    totalCount: threads.length,
    readOnly: false,
    activeThreadId: null,
    focusThread: vi.fn(),
    pendingSelection: null,
    startDraft: vi.fn(),
    startPageDraft: vi.fn(),
    draft: null,
    cancelDraft: vi.fn(),
    submitDraft: vi.fn(async () => {}),
    submitPageComment: vi.fn(async () => {}),
    reply: vi.fn(async () => {}),
    edit: vi.fn(async () => {}),
    setResolved: vi.fn(async () => {}),
    remove: vi.fn(async () => {}),
    canModify: (comment) => comment.author_type === "user" && comment.author_id === ME,
    ...overrides,
  };
}

function renderTree(
  controller: DocCommentsController,
  showResolved = false,
  onShowResolvedChange = vi.fn(),
) {
  return render(
    <DocCommentTree
      controller={controller}
      showResolved={showResolved}
      onShowResolvedChange={onShowResolvedChange}
    />,
  );
}

/** The ids actually painted, top to bottom. Asserting on the RENDER, not on the
 *  return value of the sort: a correct sort rendered in the wrong order is the
 *  defect this section exists to prevent, and a unit test on `inDocumentOrder`
 *  alone cannot see it. */
function renderedIds(): string[] {
  return screen
    .getAllByTestId("doc-comment-thread")
    .map((el) => el.getAttribute("data-thread-id") ?? "");
}

describe("DocCommentTree — order follows the document, not the clock", () => {
  it("renders threads in the order their quotes appear in the page", () => {
    // Written newest-first and latest-in-the-page-first, so creation order and
    // API order are BOTH wrong answers here.
    const controller = makeController({
      threads: [
        makeThread("last", { start: 900, end: 910 }, { createdAt: "2026-08-19T09:00:00Z" }),
        makeThread("first", { start: 10, end: 20 }, { createdAt: "2026-08-19T11:00:00Z" }),
        makeThread("middle", { start: 300, end: 310 }, { createdAt: "2026-08-19T10:00:00Z" }),
      ],
    });
    renderTree(controller);
    expect(renderedIds()).toEqual(["first", "middle", "last"]);
  });

  it("NEGATIVE CONTROL: the fixture's incoming order is not already correct", () => {
    // Guards the test above from going vacuous. If someone later reorders the
    // fixture into document order "for readability", the order assertion would
    // pass against a component that does no sorting at all — and this test is
    // what fails instead of the suite going quietly green.
    const incoming = [
      makeThread("last", { start: 900, end: 910 }),
      makeThread("first", { start: 10, end: 20 }),
      makeThread("middle", { start: 300, end: 310 }),
    ];
    const incomingIds = incoming.map((t) => t.root.id);
    const sortedIds = inDocumentOrder(incoming).map((t) => t.root.id);
    expect(incomingIds).not.toEqual(sortedIds);
  });

  it("puts threads with no position after the placed ones, keeping their order", () => {
    const controller = makeController({
      threads: [
        makeThread("orphan-a", null, { placement: "orphaned" }),
        makeThread("placed", { start: 500, end: 510 }),
        makeThread("orphan-b", null, { placement: "page" }),
      ],
    });
    renderTree(controller);
    expect(renderedIds()).toEqual(["placed", "orphan-a", "orphan-b"]);
  });

  it("breaks a tie on the same start deterministically, shortest selection first", () => {
    // Two people commenting on overlapping words must not swap places between
    // renders — an unstable list re-reads as "something changed" on every paint.
    const controller = makeController({
      threads: [
        makeThread("wide", { start: 100, end: 200 }),
        makeThread("narrow", { start: 100, end: 120 }),
      ],
    });
    renderTree(controller);
    expect(renderedIds()).toEqual(["narrow", "wide"]);
  });

  it("falls back to oldest-first when start and end are identical", () => {
    const controller = makeController({
      threads: [
        makeThread("newer", { start: 100, end: 120 }, { createdAt: "2026-08-19T12:00:00Z" }),
        makeThread("older", { start: 100, end: 120 }, { createdAt: "2026-08-19T08:00:00Z" }),
      ],
    });
    renderTree(controller);
    expect(renderedIds()).toEqual(["older", "newer"]);
  });
});

describe("DocCommentTree — resolved threads", () => {
  it("hides resolved threads by default and counts them on the toggle", () => {
    const controller = makeController({
      threads: [
        makeThread("open", { start: 10, end: 20 }),
        makeThread("done", { start: 30, end: 40 }, { resolvedAt: "2026-08-19T12:00:00Z" }),
      ],
    });
    renderTree(controller, false);
    expect(renderedIds()).toEqual(["open"]);
    expect(screen.getByText("Show resolved (1)")).toBeInTheDocument();
  });

  it("shows them when the toggle is on", () => {
    const controller = makeController({
      threads: [
        makeThread("open", { start: 10, end: 20 }),
        makeThread("done", { start: 30, end: 40 }, { resolvedAt: "2026-08-19T12:00:00Z" }),
      ],
    });
    renderTree(controller, true);
    expect(renderedIds()).toEqual(["open", "done"]);
  });

  it("reports the toggle upward rather than keeping its own copy of the flag", () => {
    // The rail and the tree must agree about what is on screen; two independent
    // flags is exactly how they would stop agreeing.
    const onChange = vi.fn();
    const controller = makeController({
      threads: [
        makeThread("open", { start: 10, end: 20 }),
        makeThread("done", { start: 30, end: 40 }, { resolvedAt: "2026-08-19T12:00:00Z" }),
      ],
    });
    renderTree(controller, false, onChange);
    fireEvent.click(screen.getByRole("checkbox"));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("offers no toggle when nothing is resolved", () => {
    renderTree(makeController({ threads: [makeThread("open", { start: 10, end: 20 })] }));
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
});

describe("DocCommentTree — the surface itself", () => {
  it("still renders the section — with its composer — on a page with no comments", () => {
    // #744ae979: the section used to return null here. It can't any more —
    // the composer that lives under the document needs somewhere to render
    // before the first comment exists, not only after.
    renderTree(makeController({ threads: [] }));
    expect(screen.getByTestId("doc-comment-tree")).toBeInTheDocument();
    expect(screen.getByText("No comments yet.")).toBeInTheDocument();
    expect(
      screen.getByPlaceholderText("Add a comment… @ to mention"),
    ).toBeInTheDocument();
  });

  it("still renders the section when every thread is resolved and hidden", () => {
    // The heading and its toggle are the only way back to a resolved-only
    // discussion. Collapsing the section to nothing here would strand it.
    const controller = makeController({
      threads: [
        makeThread("done", { start: 10, end: 20 }, { resolvedAt: "2026-08-19T12:00:00Z" }),
      ],
    });
    renderTree(controller, false);
    expect(screen.getByTestId("doc-comment-tree")).toBeInTheDocument();
    expect(screen.queryAllByTestId("doc-comment-thread")).toHaveLength(0);
    expect(screen.getByText("Show resolved (1)")).toBeInTheDocument();
  });

  it("drives the same controller as the rail — resolving here calls it once", () => {
    const setResolved = vi.fn(async () => {});
    const controller = makeController({
      threads: [makeThread("open", { start: 10, end: 20 })],
      setResolved,
    });
    renderTree(controller);
    fireEvent.click(screen.getByRole("button", { name: /resolve/i }));
    expect(setResolved).toHaveBeenCalledWith("open", true);
  });

  it("shows the quote each thread is about, so the tree is readable away from the text", () => {
    renderTree(makeController({ threads: [makeThread("t1", { start: 10, end: 20 })] }));
    expect(screen.getByText("quote t1")).toBeInTheDocument();
    expect(screen.getByText("comment t1")).toBeInTheDocument();
  });
});

describe("DocCommentTree — the composer that lives at the bottom (#744ae979)", () => {
  it("is there with a page open, no click and no selection needed first", () => {
    renderTree(makeController({ threads: [makeThread("t1", { start: 10, end: 20 })] }));
    expect(
      screen.getByPlaceholderText("Add a comment… @ to mention"),
    ).toBeInTheDocument();
  });

  it("submits straight through submitPageComment, not the draft/rail path", () => {
    const submitPageComment = vi.fn(async () => {});
    const startPageDraft = vi.fn();
    const submitDraft = vi.fn(async () => {});
    renderTree(
      makeController({ threads: [], submitPageComment, startPageDraft, submitDraft }),
    );

    fireEvent.change(screen.getByPlaceholderText("Add a comment… @ to mention"), {
      target: { value: "A note from the bottom." },
    });
    fireEvent.click(screen.getByRole("button", { name: /^comment$/i }));

    expect(submitPageComment).toHaveBeenCalledWith("A note from the bottom.");
    // The point of NOT reusing the draft path: touching it here would also
    // pop the rail's own draft box open (they share one `draft` field).
    expect(startPageDraft).not.toHaveBeenCalled();
    expect(submitDraft).not.toHaveBeenCalled();
  });

  it("is absent while the page is read-only, like every other way to start a comment", () => {
    // Doubles as the negative control for the two tests above: it proves the
    // same query can and does fail to find the composer, so their passing is
    // not an artefact of `getByPlaceholderText` matching something else.
    renderTree(makeController({ threads: [], readOnly: true }));
    expect(
      screen.queryByPlaceholderText("Add a comment… @ to mention"),
    ).not.toBeInTheDocument();
  });
});
