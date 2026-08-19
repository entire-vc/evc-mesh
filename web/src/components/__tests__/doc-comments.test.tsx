import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import {
  CommentThreadList,
  type ThreadView,
  shouldPaint,
} from "@/components/doc-comments";
import type { DocumentComment } from "@/lib/document-comments";

/**
 * The thread list under the document.
 *
 * Its one job that cannot be got wrong is telling the truth about an anchor: a
 * thread whose text was edited, or whose text is gone, must SAY so and must not
 * offer to jump to it. Everything else here is ordinary UI, and the tests assert
 * behaviour (which callback fired with what) rather than that an element exists.
 */

const AUTHOR = "user-1";

function comment(over: Partial<DocumentComment> & { id: string }): DocumentComment {
  return {
    document_id: "doc-1",
    parent_comment_id: null,
    body: `body of ${over.id}`,
    author_id: AUTHOR,
    author_type: "user",
    created_at: "2026-08-19T12:00:00Z",
    updated_at: "2026-08-19T12:00:00Z",
    ...over,
  };
}

function thread(over: Partial<ThreadView> & { id: string }): ThreadView {
  return {
    root: comment({
      id: over.id,
      anchor: { start: 0, end: 9, exact: "the quote", prefix: "", suffix: "" },
      ...(over.root ?? {}),
    }),
    replies: over.replies ?? [],
    state: over.state ?? "exact",
  };
}

function renderList(threads: ThreadView[], handlers: Partial<Parameters<typeof CommentThreadList>[0]> = {}) {
  const props = {
    threads,
    currentActorId: AUTHOR,
    onReply: vi.fn(),
    onEdit: vi.fn(),
    onDelete: vi.fn(),
    onToggleResolved: vi.fn(),
    onFocusThread: vi.fn(),
    ...handlers,
  };
  render(<CommentThreadList {...props} />);
  return props;
}

describe("CommentThreadList", () => {
  it("renders nothing at all when there are no comments", () => {
    const { container } = render(
      <CommentThreadList
        threads={[]}
        currentActorId={AUTHOR}
        onReply={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onToggleResolved={vi.fn()}
        onFocusThread={vi.fn()}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the quote and the thread body", () => {
    renderList([thread({ id: "t1" })]);

    expect(screen.getByText("the quote")).toBeInTheDocument();
    expect(screen.getByText("body of t1")).toBeInTheDocument();
  });

  it("shows nested replies, not just direct ones", () => {
    renderList([
      thread({
        id: "t1",
        replies: [
          comment({ id: "r1", parent_comment_id: "t1" }),
          comment({ id: "r2", parent_comment_id: "r1" }),
        ],
      }),
    ]);

    expect(screen.getByText("body of r1")).toBeInTheDocument();
    expect(screen.getByText("body of r2")).toBeInTheDocument();
  });

  // -------------------------------------------------------------------------
  // The honest part
  // -------------------------------------------------------------------------

  it("says nothing about an anchor that still points at its words", () => {
    renderList([thread({ id: "t1", state: "exact" })]);

    expect(screen.queryByText(/has been edited since/i)).toBeNull();
    expect(screen.queryByText(/no longer in this document/i)).toBeNull();
  });

  it("stays quiet for a thread whose text merely moved", () => {
    renderList([thread({ id: "t1", state: "moved" })]);

    expect(screen.queryByText(/has been edited since/i)).toBeNull();
  });

  it("says the text was edited, and refuses to offer a jump to it", () => {
    renderList([thread({ id: "t1", state: "edited" })]);

    expect(screen.getByText(/has been edited since/i)).toBeInTheDocument();
    // The quote button is the "go to this text" affordance. Offering it for a
    // thread with no place to go is how a reader ends up somewhere arbitrary and
    // believes it is the commented passage.
    expect(screen.getByRole("button", { name: "the quote" })).toBeDisabled();
  });

  it("says the text is gone, and refuses to offer a jump to it", () => {
    renderList([thread({ id: "t1", state: "lost" })]);

    expect(screen.getByText(/no longer in this document/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "the quote" })).toBeDisabled();
  });

  it("still lets a detached thread be answered and resolved", () => {
    // Detached is not dead: the conversation is still worth having, and a thread
    // nobody can close is a thread that stays open forever.
    const props = renderList([thread({ id: "t1", state: "lost" })]);

    fireEvent.click(screen.getByRole("button", { name: /resolve/i }));
    expect(props.onToggleResolved).toHaveBeenCalledWith("t1", true);
  });

  it("offers a jump for an anchored thread", () => {
    const props = renderList([thread({ id: "t1", state: "moved" })]);

    fireEvent.click(screen.getByRole("button", { name: "the quote" }));
    expect(props.onFocusThread).toHaveBeenCalledWith("t1");
  });

  // -------------------------------------------------------------------------
  // Resolve / reopen
  // -------------------------------------------------------------------------

  it("hides resolved threads until asked, then shows them", () => {
    renderList([
      thread({ id: "open" }),
      thread({ id: "done", root: { resolved_at: "2026-08-19T13:00:00Z" } as never }),
    ]);

    expect(screen.getByText("body of open")).toBeInTheDocument();
    expect(screen.queryByText("body of done")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /show 1 resolved/i }));
    expect(screen.getByText("body of done")).toBeInTheDocument();
  });

  it("reopens a resolved thread", () => {
    const props = renderList([
      thread({ id: "done", root: { resolved_at: "2026-08-19T13:00:00Z" } as never }),
    ]);
    fireEvent.click(screen.getByRole("button", { name: /show 1 resolved/i }));

    fireEvent.click(screen.getByRole("button", { name: /reopen/i }));
    expect(props.onToggleResolved).toHaveBeenCalledWith("done", false);
  });

  it("does not offer a reply box on a resolved thread", () => {
    renderList([thread({ id: "done", root: { resolved_at: "2026-08-19T13:00:00Z" } as never })]);
    fireEvent.click(screen.getByRole("button", { name: /show 1 resolved/i }));

    expect(screen.queryByRole("button", { name: /^reply$/i })).toBeNull();
  });

  // -------------------------------------------------------------------------
  // Reply / edit / delete
  // -------------------------------------------------------------------------

  it("sends a reply against the thread's root", () => {
    const props = renderList([
      thread({ id: "t1", replies: [comment({ id: "r1", parent_comment_id: "t1" })] }),
    ]);

    fireEvent.change(screen.getByLabelText("Reply to comment t1"), {
      target: { value: "  agreed  " },
    });
    fireEvent.click(screen.getByRole("button", { name: /^reply$/i }));

    // Trimmed, and addressed to the ROOT — a reply parented to another reply
    // would still render, but the thread's identity is its root.
    expect(props.onReply).toHaveBeenCalledWith("t1", "agreed");
  });

  it("lets the author edit their own comment", () => {
    const props = renderList([thread({ id: "t1" })]);

    fireEvent.click(screen.getAllByRole("button", { name: /edit comment/i })[0]!);
    fireEvent.change(screen.getByLabelText("Edit comment"), {
      target: { value: "rewritten" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));

    expect(props.onEdit).toHaveBeenCalledWith("t1", "rewritten");
  });

  it("offers no edit or delete on someone else's comment", () => {
    // The API refuses it anyway; showing the control would be an invitation to
    // an error message.
    renderList([thread({ id: "t1" })], { currentActorId: "someone-else" });

    expect(screen.queryByRole("button", { name: /edit comment/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /delete comment/i })).toBeNull();
  });

  it("deletes a comment", () => {
    const props = renderList([thread({ id: "t1" })]);

    fireEvent.click(screen.getAllByRole("button", { name: /delete comment/i })[0]!);
    expect(props.onDelete).toHaveBeenCalledWith("t1");
  });
});

describe("shouldPaint — what may be drawn over the document", () => {
  // Exhaustive on purpose: four anchor states times open/resolved is eight
  // cases, and the two that must be false for a reason (edited, lost) are the
  // reason this unit exists.
  it.each([
    ["exact", null, true],
    ["moved", null, true],
    ["edited", null, false],
    ["lost", null, false],
    ["exact", "2026-08-19T13:00:00Z", false],
    ["moved", "2026-08-19T13:00:00Z", false],
    ["edited", "2026-08-19T13:00:00Z", false],
    ["lost", "2026-08-19T13:00:00Z", false],
  ] as const)("%s / resolved=%s → %s", (state, resolvedAt, expected) => {
    expect(shouldPaint(state, resolvedAt)).toBe(expected);
  });

  it("treats undefined the same as null — an absent field is not a resolution", () => {
    expect(shouldPaint("exact", undefined)).toBe(true);
  });
});

describe("who wrote it", () => {
  it("shows the resolved name the API supplies", () => {
    renderList([
      thread({ id: "t1", root: { author_name: "Pavel Rogozhin" } as never }),
    ]);
    expect(screen.getByText("Pavel Rogozhin")).toBeInTheDocument();
  });

  it("falls back to the actor KIND when the name is gone", () => {
    // Not a bare "someone": whether a person or a service wrote a comment
    // changes how the next reader treats it.
    renderList([thread({ id: "t1", root: { author_type: "agent" } as never })]);
    expect(screen.getByText("Agent")).toBeInTheDocument();
  });
});
