import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import {
  DocCommentRail,
  DocCommentToggle,
} from "@/components/doc-comment-rail";
import type {
  DocCommentsController,
  PlacedThread,
  ThreadPlacement,
} from "@/lib/doc-comments/use-doc-comments";
import type { DocumentComment } from "@/types";

const ME = "11111111-1111-1111-1111-111111111111";
const SOMEBODY_ELSE = "22222222-2222-2222-2222-222222222222";

function makeComment(overrides: Partial<DocumentComment> = {}): DocumentComment {
  return {
    id: "c1",
    document_id: "doc-1",
    parent_comment_id: null,
    author_id: ME,
    author_type: "user",
    author_name: "Pavel",
    body: "This paragraph contradicts the one above.",
    anchor: {
      exact: "the deploy is blocked",
      prefix: "note that ",
      suffix: " until Friday",
      start: 42,
      end: 63,
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

function makeThread(
  root: DocumentComment,
  placement: ThreadPlacement = "anchored",
  replies: DocumentComment[] = [],
): PlacedThread {
  return {
    root,
    replies,
    placement,
    span: placement === "anchored" ? { start: 10, end: 31 } : null,
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
    draft: null,
    cancelDraft: vi.fn(),
    submitDraft: vi.fn(async () => {}),
    reply: vi.fn(async () => {}),
    edit: vi.fn(async () => {}),
    setResolved: vi.fn(async () => {}),
    remove: vi.fn(async () => {}),
    canModify: (comment) => comment.author_type === "user" && comment.author_id === ME,
    ...overrides,
  };
}

function renderRail(
  controller: DocCommentsController,
  showResolved = false,
  onShowResolvedChange = vi.fn(),
) {
  return render(
    <DocCommentRail
      controller={controller}
      showResolved={showResolved}
      onShowResolvedChange={onShowResolvedChange}
    />,
  );
}

describe("DocCommentRail — a thread", () => {
  it("shows the quote the comment was written about", () => {
    renderRail(makeController({ threads: [makeThread(makeComment())] }));
    expect(screen.getByText("the deploy is blocked")).toBeInTheDocument();
    expect(
      screen.getByText("This paragraph contradicts the one above."),
    ).toBeInTheDocument();
  });

  it("shows replies under their thread", () => {
    const root = makeComment();
    const reply = makeComment({
      id: "c2",
      parent_comment_id: "c1",
      author_id: SOMEBODY_ELSE,
      author_name: "Howard",
      body: "Agreed, I will fix it.",
    });
    renderRail(makeController({ threads: [makeThread(root, "anchored", [reply])] }));
    expect(screen.getByText("Agreed, I will fix it.")).toBeInTheDocument();
    expect(screen.getByText("Howard")).toBeInTheDocument();
  });

  it("posts a reply against the thread root", async () => {
    const controller = makeController({ threads: [makeThread(makeComment())] });
    renderRail(controller);

    fireEvent.click(screen.getByRole("button", { name: /reply/i }));
    fireEvent.change(screen.getByPlaceholderText("Reply..."), { target: { value: "Looking at it now" } });
    fireEvent.click(screen.getByRole("button", { name: "Reply" }));

    await waitFor(() =>
      expect(controller.reply).toHaveBeenCalledWith("c1", "Looking at it now"),
    );
  });
});

describe("DocCommentRail — who may do what", () => {
  it("offers edit and delete on your own comment", () => {
    renderRail(makeController({ threads: [makeThread(makeComment())] }));
    expect(screen.getByRole("button", { name: /edit/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /delete/i })).toBeInTheDocument();
  });

  it("does not offer edit or delete on somebody else's", () => {
    // The server refuses both for a non-author, so offering them would be
    // offering a 403.
    const theirs = makeComment({ author_id: SOMEBODY_ELSE, author_name: "Howard" });
    renderRail(makeController({ threads: [makeThread(theirs)] }));
    expect(screen.queryByRole("button", { name: /edit/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /delete/i })).not.toBeInTheDocument();
  });

  it("offers resolve on somebody else's thread — resolving is not ownership", () => {
    const theirs = makeComment({ author_id: SOMEBODY_ELSE, author_name: "Howard" });
    renderRail(makeController({ threads: [makeThread(theirs)] }));
    expect(screen.getByRole("button", { name: /resolve/i })).toBeInTheDocument();
  });

  it("deletes only after a confirmation", async () => {
    const controller = makeController({ threads: [makeThread(makeComment())] });
    renderRail(controller);

    fireEvent.click(screen.getByRole("button", { name: /delete/i }));
    expect(controller.remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Yes" }));
    expect(controller.remove).toHaveBeenCalledWith("c1");
  });

  it("saves an edit", async () => {
    const controller = makeController({ threads: [makeThread(makeComment())] });
    renderRail(controller);

    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    const box = screen.getByDisplayValue("This paragraph contradicts the one above.");
    fireEvent.change(box, { target: { value: "Reworded" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(controller.edit).toHaveBeenCalledWith("c1", "Reworded"));
  });
});

describe("DocCommentRail — resolve and unresolve", () => {
  it("resolves an open thread", async () => {
    const controller = makeController({ threads: [makeThread(makeComment())] });
    renderRail(controller);
    fireEvent.click(screen.getByRole("button", { name: /^resolve/i }));
    expect(controller.setResolved).toHaveBeenCalledWith("c1", true);
  });

  it("hides a resolved thread until the filter is on, and never drops it", async () => {
    const resolved = makeComment({
      resolved_at: "2026-08-19T12:00:00Z",
      resolved_by: SOMEBODY_ELSE,
      resolved_by_type: "user",
      resolved_by_name: "Howard",
    });
    const onChange = vi.fn();
    const controller = makeController({ threads: [makeThread(resolved)] });

    const { rerender } = renderRail(controller, false, onChange);
    expect(screen.queryByTestId("doc-comment-thread")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("checkbox", { name: /show resolved/i }));
    expect(onChange).toHaveBeenCalledWith(true);

    rerender(
      <DocCommentRail
        controller={controller}
        showResolved
        onShowResolvedChange={onChange}
      />,
    );
    const thread = screen.getByTestId("doc-comment-thread");
    expect(within(thread).getByText(/Resolved by Howard/)).toBeInTheDocument();
  });

  it("offers unresolve on a resolved thread", async () => {
    const resolved = makeComment({ resolved_at: "2026-08-19T12:00:00Z" });
    const controller = makeController({ threads: [makeThread(resolved)] });
    renderRail(controller, true);

    fireEvent.click(screen.getByRole("button", { name: /unresolve/i }));
    expect(controller.setResolved).toHaveBeenCalledWith("c1", false);
  });
});

describe("DocCommentRail — threads that lost their place", () => {
  it("shows an orphaned thread, marked, with its quote", () => {
    // The server's third legal anchor state: the quote survived, the position
    // did not. Hiding it would lose the discussion along with the position.
    const orphan = makeComment({
      anchor: {
        exact: "a sentence that used to be here",
        prefix: "",
        suffix: "",
        start: null,
        end: null,
        orphaned: true,
      },
    });
    renderRail(makeController({ threads: [makeThread(orphan, "orphaned")] }));

    const thread = screen.getByTestId("doc-comment-thread");
    expect(thread).toHaveAttribute("data-placement", "orphaned");
    expect(
      within(thread).getByText("a sentence that used to be here"),
    ).toBeInTheDocument();
    expect(within(thread).getByText(/no longer attached/i)).toBeInTheDocument();
  });

  it("distinguishes a thread whose text was edited away from an orphan", () => {
    renderRail(makeController({ threads: [makeThread(makeComment(), "detached")] }));
    expect(
      screen.getByText(/this text is not in the page any more/i),
    ).toBeInTheDocument();
  });

  it("labels a comment on the page as a whole", () => {
    const pageLevel = makeComment({ anchor: null });
    renderRail(makeController({ threads: [makeThread(pageLevel, "page")] }));
    expect(screen.getByText(/on the page as a whole/i)).toBeInTheDocument();
  });
});

describe("DocCommentRail — while the body is being edited", () => {
  it("shows the threads but offers no way to change them", () => {
    renderRail(
      makeController({ threads: [makeThread(makeComment())], readOnly: true }),
    );
    expect(
      screen.getByText("This paragraph contradicts the one above."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /reply/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^resolve/i })).not.toBeInTheDocument();
    expect(screen.getByText(/read-only while you are editing/i)).toBeInTheDocument();
  });
});

describe("DocCommentRail — the draft composer", () => {
  it("warns when the selection could not be pinned to the source", async () => {
    const controller = makeController({
      draft: {
        anchor: {
          exact: "quoted words",
          prefix: "",
          suffix: "",
          start: null,
          end: null,
        },
        span: { start: 0, end: 12 },
        unplaceable: true,
      },
    });
    renderRail(controller);

    expect(screen.getByText(/could not be pinned/i)).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("Add a comment..."), { target: { value: "Why?" } });
    fireEvent.click(screen.getByRole("button", { name: "Comment" }));
    await waitFor(() => expect(controller.submitDraft).toHaveBeenCalledWith("Why?"));
  });

  it("does not warn for a selection that was pinned", () => {
    renderRail(
      makeController({
        draft: {
          anchor: {
            exact: "quoted words",
            prefix: "",
            suffix: "",
            start: 10,
            end: 22,
          },
          span: { start: 0, end: 12 },
          unplaceable: false,
        },
      }),
    );
    expect(screen.queryByText(/could not be pinned/i)).not.toBeInTheDocument();
  });
});

describe("DocCommentToggle", () => {
  it("counts the open threads, not all of them", () => {
    const controller = makeController({ unresolvedCount: 2, totalCount: 5 });
    render(<DocCommentToggle controller={controller} open onToggle={vi.fn()} />);
    const badge = screen.getByTitle("2 open of 5");
    expect(badge).toHaveTextContent("2");
  });

  it("shows a tick, not a number, when nothing is open", () => {
    // "3" next to Comments reads as three comments waiting for you. It is the
    // opposite of what an all-resolved document means.
    const controller = makeController({ unresolvedCount: 0, totalCount: 3 });
    render(<DocCommentToggle controller={controller} open={false} onToggle={vi.fn()} />);
    expect(screen.getByTestId("doc-comment-toggle")).not.toHaveTextContent("3");
    expect(screen.getByLabelText("All 3 resolved")).toBeInTheDocument();
  });

  it("shows no count on a document with no comments", () => {
    render(
      <DocCommentToggle controller={makeController()} open={false} onToggle={vi.fn()} />,
    );
    expect(screen.getByTestId("doc-comment-toggle")).toHaveTextContent(/^Comments$/);
  });
});
