import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const mockedMentionables = vi.fn(
  async (_workspaceId: string, _query: string) => [] as unknown[],
);
vi.mock("@/lib/api", () => ({
  api: vi.fn(async () => ({ items: [], total_count: 0, has_more: false })),
  getAccessToken: vi.fn(() => null),
  getMentionables: (workspaceId: string, query: string) =>
    mockedMentionables(workspaceId, query),
}));

import { DocCommentRail } from "@/components/doc-comment-rail";
import type {
  DocCommentsController,
  PlacedThread,
} from "@/lib/doc-comments/use-doc-comments";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { DocumentComment, Workspace } from "@/types";

/**
 * The fourth surface.
 *
 * Task comments have had an `@` menu for a long time; document comments shipped
 * without one, which meant there was no way to address anybody from the page
 * where page-level review actually happens. These tests are about the two halves
 * of fixing that: the picker in the composer, and the mention being visible
 * afterwards in what was written.
 */

const ME = "11111111-1111-1111-1111-111111111111";
const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as unknown as Workspace;

const PAVEL = { id: "u1", slug: "pavel", display_name: "Pavel", kind: "user" };
const DAEDALUS = {
  id: "a1",
  slug: "daedalus",
  display_name: "Daedalus",
  kind: "agent",
};

function makeComment(overrides: Partial<DocumentComment> = {}): DocumentComment {
  return {
    id: "c1",
    document_id: "doc-1",
    parent_comment_id: null,
    author_id: ME,
    author_type: "user",
    author_name: "Ann",
    body: "This paragraph contradicts the one above.",
    anchor: null,
    resolved_at: null,
    resolved_by: null,
    resolved_by_type: null,
    created_at: "2026-08-19T10:00:00Z",
    updated_at: "2026-08-19T10:00:00Z",
    ...overrides,
  };
}

function makeThread(root: DocumentComment, replies: DocumentComment[] = []): PlacedThread {
  return { root, replies, placement: "page", span: null, resolved: false };
}

// A draft is what puts the new-thread composer on screen, so every test that
// types into one needs it. The anchor is the plainest legal shape — what these
// tests are about is the box, not what it is pinned to.
const DRAFT = {
  anchor: { exact: "quoted words", prefix: "", suffix: "", start: 0, end: 12 },
  span: { start: 0, end: 12 },
  unplaceable: false,
} as unknown as DocCommentsController["draft"];

function makeController(
  overrides: Partial<DocCommentsController> = {},
): DocCommentsController {
  const threads = overrides.threads ?? [];
  return {
    containerRef: { current: null },
    threads,
    isLoading: false,
    error: null,
    unresolvedCount: threads.length,
    totalCount: threads.length,
    readOnly: false,
    activeThreadId: null,
    focusThread: vi.fn(),
    pendingSelection: null,
    startDraft: vi.fn(),
    draft: DRAFT,
    cancelDraft: vi.fn(),
    submitDraft: vi.fn(async () => {}),
    reply: vi.fn(async () => {}),
    edit: vi.fn(async () => {}),
    setResolved: vi.fn(async () => {}),
    remove: vi.fn(async () => {}),
    canModify: (comment) => comment.author_id === ME,
    ...overrides,
  } as DocCommentsController;
}

function renderRail(controller: DocCommentsController) {
  return render(
    <MemoryRouter>
      <DocCommentRail
        controller={controller}
        showResolved={false}
        onShowResolvedChange={vi.fn()}
      />
    </MemoryRouter>,
  );
}

function composer(): HTMLTextAreaElement {
  return screen.getByPlaceholderText(/@ to mention/i) as HTMLTextAreaElement;
}

beforeEach(() => {
  mockedMentionables.mockReset();
  mockedMentionables.mockResolvedValue([]);
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
  useRulesStore.setState({
    teamDirectory: {
      agents: [{ slug: "daedalus" }],
      humans: [{ username: "pavel" }],
    } as never,
  });
});

describe("document comments — the @ picker", () => {
  it("opens on @ and offers a person", async () => {
    mockedMentionables.mockResolvedValue([PAVEL]);
    renderRail(makeController());

    fireEvent.change(composer(), {
      target: { value: "ping @pav", selectionStart: 9 },
    });

    expect(await screen.findByRole("option", { name: /Pavel/ })).toBeInTheDocument();
    expect(mockedMentionables).toHaveBeenCalledWith("ws-1", "pav");
  });

  it("offers an agent as well as a person — both actor types, one menu", async () => {
    mockedMentionables.mockResolvedValue([DAEDALUS, PAVEL]);
    renderRail(makeController());

    fireEvent.change(composer(), { target: { value: "@", selectionStart: 1 } });

    expect(await screen.findByRole("option", { name: /Daedalus/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Pavel/ })).toBeInTheDocument();
  });

  it("inserts the slug the server will resolve, with a trailing space", async () => {
    mockedMentionables.mockResolvedValue([DAEDALUS]);
    renderRail(makeController());
    const el = composer();
    fireEvent.change(el, { target: { value: "ping @dae", selectionStart: 9 } });

    fireEvent.mouseDown(await screen.findByRole("option", { name: /Daedalus/ }));

    await waitFor(() => expect(el.value).toBe("ping @daedalus "));
  });

  it("picks with Enter rather than submitting the comment", async () => {
    mockedMentionables.mockResolvedValue([PAVEL]);
    const controller = makeController();
    renderRail(controller);
    const el = composer();
    fireEvent.change(el, { target: { value: "@pav", selectionStart: 4 } });
    await screen.findByRole("option", { name: /Pavel/ });

    fireEvent.keyDown(el, { key: "Enter" });

    await waitFor(() => expect(el.value).toBe("@pavel "));
    expect(controller.submitDraft).not.toHaveBeenCalled();
  });

  it("moves the selection with the arrow keys", async () => {
    mockedMentionables.mockResolvedValue([DAEDALUS, PAVEL]);
    renderRail(makeController());
    const el = composer();
    fireEvent.change(el, { target: { value: "@", selectionStart: 1 } });
    await screen.findByRole("option", { name: /Daedalus/ });

    fireEvent.keyDown(el, { key: "ArrowDown" });
    fireEvent.keyDown(el, { key: "Enter" });

    await waitFor(() => expect(el.value).toBe("@pavel "));
  });

  it("closes on Escape without discarding the draft", async () => {
    mockedMentionables.mockResolvedValue([PAVEL]);
    renderRail(makeController());
    const el = composer();
    fireEvent.change(el, { target: { value: "ping @pav", selectionStart: 9 } });
    await screen.findByRole("option", { name: /Pavel/ });

    fireEvent.keyDown(el, { key: "Escape" });

    await waitFor(() =>
      expect(screen.queryByRole("option", { name: /Pavel/ })).not.toBeInTheDocument(),
    );
    expect(el.value).toBe("ping @pav");
  });

  it("does not open inside an email address", async () => {
    mockedMentionables.mockResolvedValue([PAVEL]);
    renderRail(makeController());

    fireEvent.change(composer(), {
      target: { value: "write to bob@example", selectionStart: 20 },
    });

    await waitFor(() => expect(mockedMentionables).not.toHaveBeenCalled());
    expect(screen.queryByRole("option")).not.toBeInTheDocument();
  });

  it("is available on a reply, not only on a new thread", async () => {
    mockedMentionables.mockResolvedValue([PAVEL]);
    renderRail(makeController({ threads: [makeThread(makeComment())] }));

    fireEvent.click(screen.getByRole("button", { name: /reply/i }));
    const reply = screen.getByPlaceholderText(/Reply/i) as HTMLTextAreaElement;
    fireEvent.change(reply, { target: { value: "@pav", selectionStart: 4 } });

    expect(await screen.findByRole("option", { name: /Pavel/ })).toBeInTheDocument();
  });

  it("is available when editing an existing comment", async () => {
    mockedMentionables.mockResolvedValue([PAVEL]);
    renderRail(makeController({ threads: [makeThread(makeComment())] }));

    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    const box = screen.getByPlaceholderText("Edit your comment") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "@pav", selectionStart: 4 } });

    expect(await screen.findByRole("option", { name: /Pavel/ })).toBeInTheDocument();
  });
});

describe("document comments — how a mention reads afterwards", () => {
  it("links a mention that names somebody", () => {
    renderRail(
      makeController({
        threads: [makeThread(makeComment({ body: "over to you @daedalus" }))],
      }),
    );

    const link = screen.getByRole("link", { name: /@daedalus/ });
    expect(link).toHaveAttribute("href", "/w/acme/team/agent/daedalus");
  });

  it("links a mentioned person as a person, not as an agent", () => {
    renderRail(
      makeController({
        threads: [makeThread(makeComment({ body: "@pavel does this hold?" }))],
      }),
    );

    expect(screen.getByRole("link", { name: /@pavel/ })).toHaveAttribute(
      "href",
      "/w/acme/team/user/pavel",
    );
  });

  // The read-side half of the negative control. The server refuses an
  // unresolvable mention outright, so this only happens to a name that stopped
  // resolving later — and plain text is the honest rendering of "this addresses
  // nobody". What must never happen is a confident link to a profile that is
  // not there.
  it("leaves a slug nobody recognises as plain text", () => {
    renderRail(
      makeController({
        threads: [makeThread(makeComment({ body: "ping @ghost about it" }))],
      }),
    );

    expect(screen.getByText(/ping @ghost about it/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /@ghost/ })).not.toBeInTheDocument();
  });

  it("keeps the rest of the sentence around a mention", () => {
    renderRail(
      makeController({
        threads: [makeThread(makeComment({ body: "before @pavel after" }))],
      }),
    );

    expect(screen.getByRole("link", { name: /@pavel/ })).toBeInTheDocument();
    expect(screen.getByText(/before/)).toBeInTheDocument();
    expect(screen.getByText(/after/)).toBeInTheDocument();
  });

  it("renders a comment with no mentions unchanged", () => {
    renderRail(
      makeController({ threads: [makeThread(makeComment({ body: "no names here" }))] }),
    );

    expect(screen.getByText("no names here")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
