/**
 * Arriving on a document from a mention must land on the comment, not just the
 * page.
 *
 * The Mentions tab links to `…/docs/:id?comment=<comment_id>`; this is the
 * other half of that contract. The id in the link may name a reply, so the page
 * has to focus the thread that contains it — a reader who is told "you were
 * mentioned here" and then has to hunt the rail for which thread it was has not
 * been taken to the comment.
 */
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", async () => {
  // Keep the real ApiRequestError — DocsPage checks `err instanceof
  // ApiRequestError` on every save failure, which throws instead of just
  // being false if the mock drops the class.
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn(), getAccessToken: vi.fn(() => null) };
});
vi.mock("@/components/ui/toast", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));
vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn(() => Promise.resolve()) }));

// The real editor is a ProseMirror contenteditable; the rail is the subject
// here, so the body is stubbed with the smallest thing that renders it.
vi.mock("@/components/doc-editor", () => ({
  DocEditor: ({ value }: { value: string }) => (
    <div data-testid="doc-view">{value}</div>
  ),
}));

import { api } from "@/lib/api";
import { DocsPage } from "@/pages/docs";
import { useProjectStore } from "@/stores/project";
import { useDocumentStore } from "@/stores/document";
import { useAuthStore } from "@/stores/auth";
import { useDocumentCommentStore } from "@/stores/document-comment";
import type { DocumentComment, Project, ProjectDocument } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const PROJECT: Project = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Demo",
  description: "",
  slug: "demo",
  icon: "",
  settings: {},
  default_assignee_type: "none",
  is_archived: false,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
};

const DOC: ProjectDocument = {
  id: "doc-1",
  project_id: "proj-1",
  parent_id: null,
  slug: "rollback-plan",
  title: "Rollback Plan",
  body: "The introduction.\n\nSecond paragraph, linked.",
  storage_key: "documents/proj-1/doc-1.md",
  position: 0,
  created_by: "u1",
  created_by_type: "user",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
  version: 1,
};

function makeComment(over: Partial<DocumentComment> & { id: string }): DocumentComment {
  return {
    document_id: "doc-1",
    parent_comment_id: null,
    body: "a comment",
    anchor: null,
    resolved_at: null,
    author_id: "a-1",
    author_type: "user",
    author_name: "Ann Author",
    created_at: "2026-08-19T10:00:00Z",
    updated_at: "2026-08-19T10:00:00Z",
    ...over,
  } as DocumentComment;
}

const THREAD_A_ROOT = makeComment({ id: "root-a", body: "first thread" });
const THREAD_B_ROOT = makeComment({ id: "root-b", body: "second thread" });
const THREAD_B_REPLY = makeComment({
  id: "reply-b",
  parent_comment_id: "root-b",
  body: "@pavel here",
});

function mockRoutes(comments: DocumentComment[]) {
  mockedApi.mockImplementation((path: string, opts?: { method?: string }) => {
    if (path === "/api/v1/projects/proj-1/documents" && (opts?.method ?? "GET") === "GET") {
      return Promise.resolve({ items: [DOC], has_more: false });
    }
    if (path === "/api/v1/documents/doc-1") return Promise.resolve(DOC);
    if (path === "/api/v1/documents/doc-1/comments") {
      return Promise.resolve({ items: comments, has_more: false, total_count: comments.length });
    }
    return Promise.reject(new Error(`unexpected request: ${opts?.method ?? "GET"} ${path}`));
  });
}

function renderDocs(search = "") {
  return render(
    <MemoryRouter initialEntries={[`/w/acme/p/demo/docs/doc-1${search}`]}>
      <Routes>
        <Route path="/w/:wsSlug/p/:projectSlug/docs/:docId" element={<DocsPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

/** The rail marks the focused thread with the amber border. */
function isFocused(el: HTMLElement): boolean {
  return el.className.includes("border-yellow-400");
}

// jsdom has no layout, so it has no scrollIntoView — the rail calls it on the
// thread it focuses. Stubbed rather than guarded in the component: scrolling to
// the focused thread is the behaviour, not an accident of the environment.
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

beforeEach(() => {
  mockedNavigate.mockReset();
  mockedApi.mockReset();
  localStorage.clear();
  useProjectStore.setState({ currentProject: PROJECT });
  useDocumentStore.getState().reset();
  // The comment store is module-global: comments left by a previous test would
  // make the next one assert against a document it never loaded.
  useDocumentCommentStore.getState().reset();
  useAuthStore.setState({ user: { id: "u1", email: "u@e.vc", name: "U" } as never });
});

describe("DocsPage — ?comment= deep link", () => {
  it("opens the rail and focuses the thread the link names", async () => {
    mockRoutes([THREAD_A_ROOT, THREAD_B_ROOT]);
    renderDocs("?comment=root-b");

    await screen.findByTestId("doc-comment-rail");
    await waitFor(() => {
      const threads = screen.getAllByTestId("doc-comment-thread");
      const b = threads.find((t) => t.dataset.threadId === "root-b")!;
      expect(isFocused(b)).toBe(true);
    });
    const threads = screen.getAllByTestId("doc-comment-thread");
    const a = threads.find((t) => t.dataset.threadId === "root-a")!;
    expect(isFocused(a)).toBe(false);
  });

  it("opens a rail the reader had collapsed — the default is open, so this is the real assertion", async () => {
    // Without this the previous test proves nothing about opening: an untouched
    // rail is open already (docs-layout-storage.ts:83).
    localStorage.setItem("mesh_docs_comments_rail", "0");
    mockRoutes([THREAD_A_ROOT, THREAD_B_ROOT]);
    renderDocs("?comment=root-b");

    expect(await screen.findByTestId("doc-comment-rail")).toBeInTheDocument();
  });

  it("leaves a collapsed rail collapsed when there is no ?comment= link", async () => {
    // The negative control for the test above: arriving normally must not
    // reopen a rail the reader closed.
    localStorage.setItem("mesh_docs_comments_rail", "0");
    mockRoutes([THREAD_A_ROOT, THREAD_B_ROOT]);
    renderDocs();

    await screen.findByTestId("doc-view");
    expect(screen.queryByTestId("doc-comment-rail")).not.toBeInTheDocument();
  });

  it("focuses the containing thread when the link names a REPLY", async () => {
    mockRoutes([THREAD_A_ROOT, THREAD_B_ROOT, THREAD_B_REPLY]);
    renderDocs("?comment=reply-b");

    await screen.findByTestId("doc-comment-rail");
    await waitFor(() => {
      const b = screen
        .getAllByTestId("doc-comment-thread")
        .find((t) => t.dataset.threadId === "root-b")!;
      expect(isFocused(b)).toBe(true);
    });
  });

  it("focuses nothing when the link names a comment this document does not have", async () => {
    mockRoutes([THREAD_A_ROOT]);
    renderDocs("?comment=does-not-exist");

    await screen.findByTestId("doc-comment-rail");
    await waitFor(() =>
      expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(1),
    );
    for (const t of screen.getAllByTestId("doc-comment-thread")) {
      expect(isFocused(t)).toBe(false);
    }
  });
});
