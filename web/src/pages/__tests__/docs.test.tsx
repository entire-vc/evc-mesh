import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import {
  AA_NON_TEXT,
  AA_TEXT,
  brandkitThemes,
  contrast,
} from "@/test-utils/brandkit-contrast";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", async () => {
  // Keep the real ApiRequestError: the page does `err instanceof
  // ApiRequestError` to tell a version conflict from any other failure, and a
  // mock that dropped the class would make that check throw instead of just
  // being false.
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn(), apiBlob: vi.fn(), getAccessToken: vi.fn(() => null) };
});

vi.mock("@/components/ui/toast", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

/**
 * The page's subject is the save orchestration — debounce, flush on leave,
 * failure reporting — not what renders the body. DocEditor is stubbed with the
 * smallest thing that honours its contract (`value` / `onChange` / `readOnly`),
 * so these tests keep asserting the page's behaviour and stay indifferent to
 * which editor sits behind the boundary. That indifference is the whole point of
 * having the boundary; the editor has its own tests in
 * components/__tests__/doc-editor.test.tsx.
 *
 * It is also the only way to drive it: the real editor is a ProseMirror
 * contenteditable, which does not respond to fireEvent.change.
 */
/**
 * Anchors: the page owns the URL and the message, the editor owns the DOM. The
 * stub therefore exposes the two calls the real editor makes — "the reader
 * asked for a link to this paragraph" and "here is what the incoming link
 * resolved to" — and nothing else.
 */
const stubAnchor = {
  start: 12,
  end: 40,
  exact: "Second paragraph, linked.",
  prefix: "The introduction.",
  suffix: "The conclusion.",
};
/** Set by a test before rendering; reported to the page as if resolved. */
let stubMatch: { status: string; index: number | null } | null = null;

vi.mock("@/components/doc-editor", () => ({
  DocEditor: ({
    value,
    onChange,
    readOnly,
    anchor,
    onAnchorResolved,
    onCopyAnchor,
  }: {
    value: string;
    onChange: (v: string) => void;
    readOnly?: boolean;
    anchor?: unknown;
    onAnchorResolved?: (m: unknown) => void;
    onCopyAnchor?: (a: unknown) => void;
  }) => {
    if (readOnly) {
      return value.trim() ? (
        <div data-testid="doc-view">
          {value}
          <span data-testid="anchor-received">{anchor ? "yes" : "no"}</span>
          <button type="button" onClick={() => onCopyAnchor?.(stubAnchor)}>
            stub copy anchor
          </button>
          <button
            type="button"
            onClick={() => stubMatch && onAnchorResolved?.(stubMatch)}
          >
            stub resolve anchor
          </button>
        </div>
      ) : (
        <p>This page is empty.</p>
      );
    }
    return (
      <textarea
        aria-label="Document body"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  },
}));

vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn(() => Promise.resolve()) }));

import { api, apiBlob, ApiRequestError } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { copyText } from "@/lib/clipboard";
import { anchorFromHash, anchorToHash } from "@/lib/docs/anchor";
import { DOC_TREE_DEFAULT_WIDTH } from "@/lib/docs-layout-storage";
import { MATCH_END, MATCH_START } from "@/lib/docs/document-search";
import { DocsPage } from "@/pages/docs";
import { useDocumentStore } from "@/stores/document";
import { useProjectStore } from "@/stores/project";
import type { Project, ProjectDocument } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;
const mockedApiBlob = apiBlob as unknown as ReturnType<typeof vi.fn>;

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

function makeDoc(overrides: Partial<ProjectDocument> & { id: string }): ProjectDocument {
  return {
    project_id: "proj-1",
    parent_id: null,
    slug: overrides.id,
    title: overrides.id,
    storage_key: `documents/proj-1/${overrides.id}.md`,
    position: 0,
    created_by: "u1",
    created_by_type: "user",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    version: 1,
    ...overrides,
  };
}

const LIST_PATH = "/api/v1/projects/proj-1/documents";

type MockApiOpts = {
  method?: string;
  body?: unknown;
  params?: Record<string, string | number | undefined>;
};

/** Answers the list endpoint; anything else must be handled by the test. */
function mockRoutes(
  docs: ProjectDocument[],
  extra?: (path: string, opts?: MockApiOpts) => unknown,
) {
  mockedApi.mockImplementation(
    (path: string, opts?: MockApiOpts) => {
      // Method-checked: POST goes to the same path as the list, and a branch
      // that only matched the path answered creates with the listing.
      if (path === LIST_PATH && (opts?.method ?? "GET") === "GET") {
        return Promise.resolve({ items: docs, has_more: false });
      }
      const handled = extra?.(path, opts);
      if (handled !== undefined) return handled;
      return Promise.reject(new Error(`unexpected request: ${opts?.method ?? "GET"} ${path}`));
    },
  );
}

function renderDocs(docId?: string, hash = "") {
  const base = "/w/acme/p/demo/docs";
  return render(
    <MemoryRouter initialEntries={[(docId ? `${base}/${docId}` : base) + hash]}>
      <Routes>
        <Route path="/w/:wsSlug/p/:projectSlug/docs" element={<DocsPage />} />
        <Route path="/w/:wsSlug/p/:projectSlug/docs/:docId" element={<DocsPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedNavigate.mockReset();
  mockedApi.mockReset();
  mockedApiBlob.mockReset();
  vi.mocked(toast.error).mockReset();
  useProjectStore.setState({ currentProject: PROJECT });
  useDocumentStore.getState().reset();
  // The tree column width lives here; a width left behind by one test would
  // otherwise be the starting width of the next.
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("DocsPage — empty state", () => {
  it("offers a way to create the first document", async () => {
    mockRoutes([]);
    renderDocs();

    // Both columns must offer it: the tree header button is the durable one,
    // the empty-state button is what a new project actually sees first.
    const createButtons = await screen.findAllByRole("button", { name: /new page/i });
    expect(createButtons.length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: /^New$/ })).toBeInTheDocument();
  });
});

describe("DocsPage — create", () => {
  it("posts the title and opens the new document", async () => {
    const created = makeDoc({ id: "doc-new", title: "Runbook" });
    let posted: unknown;
    mockRoutes([], (path, opts) => {
      if (path === LIST_PATH && opts?.method === "POST") {
        posted = opts.body;
        return Promise.resolve(created);
      }
      return undefined;
    });

    renderDocs();

    fireEvent.click(await screen.findByRole("button", { name: /^New$/ }));
    fireEvent.change(screen.getByLabelText("Title"), {
      target: { value: "Runbook" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs/doc-new");
    });
    expect(posted).toMatchObject({ title: "Runbook", parent_id: null, position: 0 });
  });

  it("creates a child under the node whose + was clicked", async () => {
    const parent = makeDoc({ id: "parent", title: "Parent", position: 3 });
    let posted: { parent_id?: string | null } | undefined;
    mockRoutes([parent], (path, opts) => {
      if (path === LIST_PATH && opts?.method === "POST") {
        posted = opts.body as { parent_id?: string | null };
        return Promise.resolve(makeDoc({ id: "kid", parent_id: "parent" }));
      }
      return undefined;
    });

    renderDocs();

    fireEvent.click(await screen.findByLabelText("New page inside Parent"));
    fireEvent.change(screen.getByLabelText("Title"), {
      target: { value: "Child" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(posted).toBeDefined());
    expect(posted?.parent_id).toBe("parent");
  });

  it("surfaces a failed create instead of closing silently", async () => {
    mockRoutes([], (path, opts) => {
      if (path === LIST_PATH && opts?.method === "POST") {
        return Promise.reject(new Error("storage backend not configured"));
      }
      return undefined;
    });

    renderDocs();

    fireEvent.click(await screen.findByRole("button", { name: /^New$/ }));
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "X" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("storage backend not configured");
    });
    expect(mockedNavigate).not.toHaveBeenCalled();
  });
});

describe("DocsPage — open, view and edit", () => {
  const doc = makeDoc({ id: "doc-1", title: "Runbook" });

  function mockWithBody(body: string, onPatch?: (b: unknown) => unknown) {
    mockRoutes([doc], (path, opts) => {
      if (path === "/api/v1/documents/doc-1" && !opts?.method) {
        return Promise.resolve({ ...doc, body });
      }
      if (path === "/api/v1/documents/doc-1" && opts?.method === "PATCH") {
        return onPatch ? onPatch(opts.body) : Promise.resolve({ ...doc, body: "" });
      }
      return undefined;
    });
  }

  it("opens in view mode: the body is shown, with nothing to type into", async () => {
    mockWithBody("# Deploy\n\nRun the thing.");
    renderDocs("doc-1");

    expect(await screen.findByTestId("doc-view")).toHaveTextContent("Run the thing.");
    // Scoped to the measured body, not the whole page: since #744ae979 the
    // page legitimately carries one textbox outside it — the always-mounted
    // comment composer under the document — and that one is not what this
    // test is about.
    expect(
      within(screen.getByTestId("doc-measured-body")).queryByRole("textbox"),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /edit/i })).toBeInTheDocument();
  });

  it("switches to the editor on Edit", async () => {
    mockWithBody("hello");
    renderDocs("doc-1");

    fireEvent.click(await screen.findByRole("button", { name: /edit/i }));

    expect(screen.getByRole("textbox")).toHaveValue("hello");
  });

  it("autosaves the body after the debounce", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const patched: unknown[] = [];
    mockWithBody("hello", (body) => {
      patched.push(body);
      return Promise.resolve({ ...doc, body: "hello world" });
    });
    renderDocs("doc-1");

    fireEvent.click(await screen.findByRole("button", { name: /edit/i }));
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "hello world" },
    });

    expect(patched).toHaveLength(0); // debounced, not per keystroke
    await vi.advanceTimersByTimeAsync(2000);

    // base_version travels on every save — the version read on load (1).
    expect(patched).toEqual([{ body: "hello world", base_version: 1 }]);
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("shows a failed save rather than pretending the text is stored", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mockWithBody("hello", () => Promise.reject(new Error("network down")));
    renderDocs("doc-1");

    fireEvent.click(await screen.findByRole("button", { name: /edit/i }));
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "unsaved words" },
    });
    await vi.advanceTimersByTimeAsync(2000);

    expect(await screen.findByText(/Not saved: network down/)).toBeInTheDocument();
    // The text is still in the editor — the failure must not eat it.
    expect(screen.getByRole("textbox")).toHaveValue("unsaved words");
  });

  it("shows the conflict message and keeps the draft when the save is refused", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let getCalls = 0;
    let patchCalls = 0;
    mockRoutes([doc], (path, opts) => {
      if (path === "/api/v1/documents/doc-1" && opts?.method === "PATCH") {
        patchCalls++;
        return Promise.reject(
          new ApiRequestError(
            "Document was modified since you read it.",
            "document_version_conflict",
            409,
          ),
        );
      }
      if (path === "/api/v1/documents/doc-1" && !opts?.method) {
        getCalls++;
        // First GET is the initial open; the second is the re-read the
        // conflict triggers, reporting what the other tab actually saved.
        return Promise.resolve(
          getCalls === 1
            ? { ...doc, body: "hello", version: 1 }
            : { ...doc, body: "hello from the other tab", version: 2 },
        );
      }
      return undefined;
    });
    renderDocs("doc-1");

    fireEvent.click(await screen.findByRole("button", { name: /edit/i }));
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "my own edit" },
    });
    await vi.advanceTimersByTimeAsync(2000);

    expect(
      await screen.findByText("this page changed elsewhere"),
    ).toBeInTheDocument();
    // Neither text was silently lost: the reader's own draft is still here...
    expect(screen.getByRole("textbox")).toHaveValue("my own edit");
    // ...and the other tab's save was never overwritten — no second PATCH.
    expect(patchCalls).toBe(1);
    expect(getCalls).toBe(2);
  });

  it("does not retry on its own after a conflict — only a new edit re-arms the save, this time with the refreshed version", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let getCalls = 0;
    const patched: unknown[] = [];
    mockRoutes([doc], (path, opts) => {
      if (path === "/api/v1/documents/doc-1" && opts?.method === "PATCH") {
        patched.push(opts.body);
        // Conflict once, on the version read at load; succeed once the
        // caller comes back with the version the re-read reported.
        const body = opts.body as { base_version?: number };
        if (body.base_version === 1) {
          return Promise.reject(
            new ApiRequestError("stale", "document_version_conflict", 409),
          );
        }
        return Promise.resolve({ ...doc, body: "my own edit, retried", version: 3 });
      }
      if (path === "/api/v1/documents/doc-1" && !opts?.method) {
        getCalls++;
        return Promise.resolve(
          getCalls === 1
            ? { ...doc, body: "hello", version: 1 }
            : { ...doc, body: "hello from the other tab", version: 2 },
        );
      }
      return undefined;
    });
    renderDocs("doc-1");

    fireEvent.click(await screen.findByRole("button", { name: /edit/i }));
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "my own edit" },
    });
    await vi.advanceTimersByTimeAsync(2000);
    expect(await screen.findByText("this page changed elsewhere")).toBeInTheDocument();

    // No further keystroke: waiting alone must not fire another PATCH — a
    // blind retry on the timer is exactly the loop this guards against.
    await vi.advanceTimersByTimeAsync(10000);
    expect(patched).toHaveLength(1);

    // A genuine new edit is what re-arms the debounce, now against the
    // version the conflict's re-read learned (2).
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "my own edit, retried" },
    });
    await vi.advanceTimersByTimeAsync(2000);

    expect(patched).toEqual([
      { body: "my own edit", base_version: 1 },
      { body: "my own edit, retried", base_version: 2 },
    ]);
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("saves pending keystrokes when the document is left before the debounce fires", async () => {
    const other = makeDoc({ id: "doc-2", title: "Other" });
    const patched: unknown[] = [];
    mockedApi.mockImplementation(
      (path: string, opts?: { method?: string; body?: unknown }) => {
        if (path === LIST_PATH && (opts?.method ?? "GET") === "GET") {
          return Promise.resolve({ items: [doc, other], has_more: false });
        }
        if (path === "/api/v1/documents/doc-1" && opts?.method === "PATCH") {
          patched.push(opts.body);
          return Promise.resolve({ ...doc, body: "half-typed" });
        }
        if (path === "/api/v1/documents/doc-1") {
          return Promise.resolve({ ...doc, body: "hello" });
        }
        if (path === "/api/v1/documents/doc-2") {
          return Promise.resolve({ ...other, body: "" });
        }
        return Promise.reject(new Error(`unexpected: ${path}`));
      },
    );

    const { unmount } = renderDocs("doc-1");
    fireEvent.click(await screen.findByRole("button", { name: /edit/i }));
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "half-typed" },
    });

    // Leaving well inside the 2s window.
    unmount();

    await waitFor(() =>
      expect(patched).toEqual([{ body: "half-typed", base_version: 1 }]),
    );
  });

  it("reports a document that cannot be loaded", async () => {
    mockRoutes([doc], (path) => {
      if (path === "/api/v1/documents/doc-1") {
        return Promise.reject(new Error("Document not found"));
      }
      return undefined;
    });
    renderDocs("doc-1");

    expect(await screen.findByText("Document not found")).toBeInTheDocument();
  });
});

describe("DocsPage — the page's own header", () => {
  const parent = makeDoc({ id: "p", title: "Engineering" });
  const child = makeDoc({ id: "c", title: "ADR-004 Document storage", parent_id: "p" });

  function mockOpen(target: ProjectDocument) {
    mockRoutes([parent, child], (path) => {
      if (path === `/api/v1/documents/${target.id}`) {
        return Promise.resolve({ ...target, body: "text" });
      }
      return undefined;
    });
  }

  it("leads with breadcrumbs down to the open page", async () => {
    mockOpen(child);
    renderDocs("c");

    const nav = await screen.findByRole("navigation", { name: "Breadcrumb" });
    expect(nav).toHaveTextContent("Documents");
    expect(nav).toHaveTextContent("Engineering");
    expect(nav).toHaveTextContent("ADR-004 Document storage");
  });

  it("navigates to an ancestor from the breadcrumb", async () => {
    mockOpen(child);
    renderDocs("c");

    const nav = await screen.findByRole("navigation", { name: "Breadcrumb" });
    fireEvent.click(within(nav).getByRole("button", { name: "Engineering" }));
    expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs/p");
  });

  it("shows the title as the page heading with the meta line under it", async () => {
    mockOpen(child);
    renderDocs("c");

    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: "ADR-004 Document storage",
      }),
    ).toBeInTheDocument();
    // updated_by is not in the API yet — the line must render regardless.
    expect(screen.getByText(/Created by/)).toBeInTheDocument();
    expect(screen.getByText(/Updated Aug 1, 2026/)).toBeInTheDocument();
  });

  it("keeps Edit primary and puts rename and delete behind the overflow", async () => {
    mockOpen(child);
    renderDocs("c");

    expect(await screen.findByRole("button", { name: /^Edit$/ })).toBeInTheDocument();
    // Closed by default: the destructive item is not one stray click away.
    expect(screen.queryByRole("menuitem", { name: "Delete" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("More actions"));
    expect(screen.getByRole("menuitem", { name: "Rename" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
  });

  it("renames the open page from the overflow menu", async () => {
    let patched: unknown;
    mockRoutes([parent], (path, opts) => {
      if (path === "/api/v1/documents/p" && opts?.method === "PATCH") {
        patched = opts.body;
        return Promise.resolve({ ...parent, title: "Platform" });
      }
      if (path === "/api/v1/documents/p") {
        return Promise.resolve({ ...parent, body: "text" });
      }
      return undefined;
    });
    renderDocs("p");

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.change(screen.getByLabelText("Title"), {
      target: { value: "Platform" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patched).toEqual({ title: "Platform" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Platform" }),
    ).toBeInTheDocument();
  });

  it("deletes the open page from the overflow menu", async () => {
    let deleted = false;
    mockRoutes([parent], (path, opts) => {
      if (path === "/api/v1/documents/p" && opts?.method === "DELETE") {
        deleted = true;
        return Promise.resolve(undefined);
      }
      if (path === "/api/v1/documents/p") {
        return Promise.resolve({ ...parent, body: "text" });
      }
      return undefined;
    });
    renderDocs("p");

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() => expect(deleted).toBe(true));
    expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs");
  });
});

// Export 6/7 (#015491ee). What "download actually happened in a real
// browser" means is not something jsdom can observe — that half of the AC is
// the mandatory authed Playwright scenario (§1n), not this file. What these
// tests own: the three menu items exist with the pre-approved labels, the
// dialog appears only when there ARE children to choose from (and not
// otherwise — the point of #2a467980 §2's "лишний шаг... раздражение без
// пользы"), and the chosen scope reaches apiBlob correctly either way.
describe("DocsPage — export", () => {
  const parent = makeDoc({ id: "p", title: "Engineering" });
  const child = makeDoc({ id: "c", title: "ADR-004", parent_id: "p" });
  const lonely = makeDoc({ id: "lonely", title: "Standalone" });

  function mockOpen(docs: ProjectDocument[], target: ProjectDocument) {
    mockRoutes(docs, (path) => {
      if (path === `/api/v1/documents/${target.id}`) {
        return Promise.resolve({ ...target, body: "text" });
      }
      return undefined;
    });
  }

  function stubBlobDownload() {
    mockedApiBlob.mockResolvedValue({
      blob: new Blob(["fake-bytes"]),
      filename: "export.pdf",
    });
  }

  it("all three formats are offered, pre-approved labels", async () => {
    mockOpen([lonely], lonely);
    renderDocs("lonely");
    stubBlobDownload();

    fireEvent.click(await screen.findByLabelText("More actions"));

    expect(screen.getByRole("menuitem", { name: "Export as PDF" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Export as Markdown" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Export as DOCX" })).toBeInTheDocument();
  });

  it("a document with no children downloads immediately, scope=self, no dialog", async () => {
    mockOpen([lonely], lonely);
    renderDocs("lonely");
    stubBlobDownload();

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export as PDF" }));

    await waitFor(() => expect(mockedApiBlob).toHaveBeenCalledTimes(1));
    expect(mockedApiBlob).toHaveBeenCalledWith("/api/v1/documents/lonely/export", {
      format: "pdf",
      scope: "self",
    });
    expect(screen.queryByText(/Export "/)).not.toBeInTheDocument();
  });

  it("a document WITH children shows the dialog instead of downloading immediately", async () => {
    mockOpen([parent, child], parent);
    renderDocs("p");
    stubBlobDownload();

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export as Markdown" }));

    expect(await screen.findByText('Export "Engineering"')).toBeInTheDocument();
    expect(screen.getByText("Include sub-documents (1)")).toBeInTheDocument();
    expect(mockedApiBlob).not.toHaveBeenCalled();
  });

  it("Cancel closes the dialog without exporting anything", async () => {
    mockOpen([parent, child], parent);
    renderDocs("p");
    stubBlobDownload();

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export as DOCX" }));
    await screen.findByText('Export "Engineering"');

    fireEvent.click(within(screen.getByTestId("export-dialog")).getByRole("button", { name: "Cancel" }));

    expect(screen.queryByText('Export "Engineering"')).not.toBeInTheDocument();
    expect(mockedApiBlob).not.toHaveBeenCalled();
  });

  it("default choice is 'just this document' — scope=self even though children exist", async () => {
    mockOpen([parent, child], parent);
    renderDocs("p");
    stubBlobDownload();

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export as PDF" }));
    await screen.findByText('Export "Engineering"');
    fireEvent.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() => expect(mockedApiBlob).toHaveBeenCalledTimes(1));
    expect(mockedApiBlob).toHaveBeenCalledWith("/api/v1/documents/p/export", {
      format: "pdf",
      scope: "self",
    });
  });

  it("picking 'include sub-documents' sends scope=tree", async () => {
    mockOpen([parent, child], parent);
    renderDocs("p");
    stubBlobDownload();

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export as PDF" }));
    await screen.findByText('Export "Engineering"');
    fireEvent.click(screen.getByRole("radio", { name: /Include sub-documents/ }));
    fireEvent.click(screen.getByRole("button", { name: "Export" }));

    await waitFor(() => expect(mockedApiBlob).toHaveBeenCalledTimes(1));
    expect(mockedApiBlob).toHaveBeenCalledWith("/api/v1/documents/p/export", {
      format: "pdf",
      scope: "tree",
    });
  });

  it("a failed export is reported, not silent", async () => {
    mockOpen([lonely], lonely);
    renderDocs("lonely");
    mockedApiBlob.mockRejectedValue(new ApiRequestError("too large", "export_too_large", 400));

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export as DOCX" }));

    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });

  it("counts only DESCENDANTS of the open document as children, not siblings or the doc itself", async () => {
    // A sibling of `child` under a different parent must not inflate the count.
    const other = makeDoc({ id: "other", title: "Unrelated", parent_id: null });
    mockOpen([parent, child, other], parent);
    renderDocs("p");
    stubBlobDownload();

    fireEvent.click(await screen.findByLabelText("More actions"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Export as Markdown" }));

    expect(await screen.findByText("Include sub-documents (1)")).toBeInTheDocument();
  });
});

describe("DocsPage — tree column width", () => {
  const doc = makeDoc({ id: "doc-1", title: "Runbook" });

  it("starts at the stored width and writes back the one the reader drags to", async () => {
    localStorage.setItem("mesh_docs_tree_width", "320");
    mockRoutes([doc]);
    renderDocs();

    const handle = await screen.findByRole("separator", {
      name: "Resize document tree",
    });
    expect(handle).toHaveAttribute("aria-valuenow", "320");

    fireEvent.mouseDown(handle, { clientX: 320, button: 0 });
    fireEvent.mouseMove(window, { clientX: 400 });
    fireEvent.mouseUp(window, { clientX: 400 });

    expect(handle).toHaveAttribute("aria-valuenow", "400");
    expect(localStorage.getItem("mesh_docs_tree_width")).toBe("400");
  });

  it("falls back to the default when nothing is stored", async () => {
    mockRoutes([doc]);
    renderDocs();

    expect(
      await screen.findByRole("separator", { name: "Resize document tree" }),
    ).toHaveAttribute("aria-valuenow", "280");
  });
});

describe("DocsPage — rename and delete", () => {
  const parent = makeDoc({ id: "p", title: "Parent" });
  const child = makeDoc({ id: "c", title: "Child", parent_id: "p" });

  it("renames through the per-node menu", async () => {
    let patched: unknown;
    mockRoutes([parent], (path, opts) => {
      if (path === "/api/v1/documents/p" && opts?.method === "PATCH") {
        patched = opts.body;
        return Promise.resolve({ ...parent, title: "Renamed" });
      }
      return undefined;
    });
    renderDocs();

    fireEvent.click(await screen.findByLabelText("Actions for Parent"));
    fireEvent.click(screen.getByText("Rename"));
    fireEvent.change(screen.getByLabelText("Title"), {
      target: { value: "Renamed" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patched).toEqual({ title: "Renamed" }));
    expect(await screen.findByText("Renamed")).toBeInTheDocument();
  });

  it("asks for confirmation in-app, naming the nested pages, and deletes on confirm", async () => {
    const confirmSpy = vi.spyOn(window, "confirm");
    let deleted = false;
    mockRoutes([parent, child], (path, opts) => {
      if (path === "/api/v1/documents/p" && opts?.method === "DELETE") {
        deleted = true;
        return Promise.resolve(undefined);
      }
      return undefined;
    });
    renderDocs();

    fireEvent.click(await screen.findByLabelText("Actions for Parent"));
    fireEvent.click(screen.getByText("Delete"));

    // In-app dialog, not a browser modal.
    expect(confirmSpy).not.toHaveBeenCalled();
    expect(
      await screen.findByText(/Delete "Parent" and its 1 nested page\?/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(deleted).toBe(true));
    // The subtree goes with it.
    await waitFor(() => expect(screen.queryByText("Child")).not.toBeInTheDocument());
    confirmSpy.mockRestore();
  });

  it("returns to the docs root when the open document is deleted", async () => {
    mockRoutes([parent], (path, opts) => {
      if (path === "/api/v1/documents/p" && opts?.method === "DELETE") {
        return Promise.resolve(undefined);
      }
      if (path === "/api/v1/documents/p") {
        return Promise.resolve({ ...parent, body: "text" });
      }
      return undefined;
    });
    renderDocs("p");

    fireEvent.click(await screen.findByLabelText("Actions for Parent"));
    fireEvent.click(screen.getByText("Delete"));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() => {
      expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs");
    });
  });
});

describe("DocsPage — move", () => {
  const parent = makeDoc({ id: "p", title: "Parent" });
  const child = makeDoc({ id: "c", title: "Child", parent_id: "p" });
  const other = makeDoc({ id: "o", title: "Other", position: 1 });

  function openMoveDialogFor(title: string) {
    fireEvent.click(screen.getByLabelText(`Actions for ${title}`));
    fireEvent.click(screen.getByText("Move to..."));
  }

  it("moves a document under another, sending parent_id", async () => {
    let patched: unknown;
    mockRoutes([parent, child, other], (path, opts) => {
      if (path === "/api/v1/documents/c" && opts?.method === "PATCH") {
        patched = opts.body;
        return Promise.resolve({ ...child, parent_id: "o" });
      }
      return undefined;
    });
    renderDocs();

    await screen.findByText("Child");
    openMoveDialogFor("Child");
    fireEvent.click(await screen.findByRole("option", { name: /Other/ }));
    fireEvent.click(screen.getByRole("button", { name: "Move" }));

    await waitFor(() => expect(patched).toEqual({ parent_id: "o" }));
  });

  it("moves a document to the top level with clear_parent, not parent_id: null", async () => {
    let patched: unknown;
    mockRoutes([parent, child], (path, opts) => {
      if (path === "/api/v1/documents/c" && opts?.method === "PATCH") {
        patched = opts.body;
        return Promise.resolve({ ...child, parent_id: null });
      }
      return undefined;
    });
    renderDocs();

    await screen.findByText("Child");
    openMoveDialogFor("Child");
    fireEvent.click(await screen.findByRole("option", { name: /Top level/ }));
    fireEvent.click(screen.getByRole("button", { name: "Move" }));

    // The distinction is load-bearing: the API binds parent_id into a *uuid.UUID,
    // where an explicit null is indistinguishable from an omitted field, so a
    // null here would be read as "leave the parent alone" and silently no-op.
    await waitFor(() => expect(patched).toEqual({ clear_parent: true }));
  });

  it("does not offer the document itself or its descendants as a destination", async () => {
    mockRoutes([parent, child, other]);
    renderDocs();

    await screen.findByText("Parent");
    openMoveDialogFor("Parent");

    await screen.findByRole("option", { name: /Top level/ });
    expect(screen.getByRole("option", { name: /Other/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Parent/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Child/ })).not.toBeInTheDocument();
  });

  it("keeps Move disabled until a different parent is picked", async () => {
    mockRoutes([parent, child, other]);
    renderDocs();

    await screen.findByText("Child");
    openMoveDialogFor("Child");

    // Opens on the current parent, so the button starts inert.
    expect(await screen.findByRole("button", { name: "Move" })).toBeDisabled();

    fireEvent.click(screen.getByRole("option", { name: /Other/ }));
    expect(screen.getByRole("button", { name: "Move" })).toBeEnabled();
  });

  it("surfaces a refused move instead of closing as if it worked", async () => {
    mockRoutes([parent, child, other], (path, opts) => {
      if (path === "/api/v1/documents/c" && opts?.method === "PATCH") {
        return Promise.reject(
          new Error("a document with this slug already exists in this location"),
        );
      }
      return undefined;
    });
    renderDocs();

    await screen.findByText("Child");
    openMoveDialogFor("Child");
    fireEvent.click(await screen.findByRole("option", { name: /Other/ }));
    fireEvent.click(screen.getByRole("button", { name: "Move" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "a document with this slug already exists in this location",
      ),
    );
    // Still open: the user has to see the destination they picked was refused.
    expect(screen.getByRole("button", { name: "Move" })).toBeInTheDocument();
  });

  it("re-parents the node in the tree after a move", async () => {
    mockRoutes([parent, child, other], (path, opts) => {
      if (path === "/api/v1/documents/c" && opts?.method === "PATCH") {
        return Promise.resolve({ ...child, parent_id: "o" });
      }
      return undefined;
    });
    renderDocs();

    await screen.findByText("Child");
    openMoveDialogFor("Child");
    fireEvent.click(await screen.findByRole("option", { name: /Other/ }));
    fireEvent.click(screen.getByRole("button", { name: "Move" }));

    // Collapsing the new parent must take the moved node with it — that is the
    // observable difference between "the store was updated" and "the tree was
    // rebuilt from it".
    await waitFor(() => expect(screen.getByLabelText("Collapse Other")).toBeInTheDocument());
    fireEvent.click(screen.getByLabelText("Collapse Other"));
    expect(screen.queryByText("Child")).not.toBeInTheDocument();
  });

  it("fills the picked destination with the light secondary surface, not the accent", async () => {
    // Pinned deliberately, and it is the same pin the tree carries. The dialog
    // shipped with `--accent` on the picked row and on hover — a saturated teal
    // block under the inherited near-black text — and that is what was reported
    // as "the highlight is dark, it was supposed to be the light green one".
    mockRoutes([parent, child, other]);
    renderDocs();

    await screen.findByText("Child");
    openMoveDialogFor("Child");
    fireEvent.click(await screen.findByRole("option", { name: /Other/ }));

    const picked = screen.getByRole("option", { name: /Other/ });
    expect(picked).toHaveClass("bg-secondary");
    expect(picked).toHaveClass("text-secondary-foreground");
    expect(picked).not.toHaveClass("bg-accent");
    // Hover is a neutral tint, not the brand fill: every row the pointer
    // crossed used to turn into a solid teal block.
    expect(picked).toHaveClass("hover:bg-muted");
    expect(picked.className).not.toContain("hover:bg-accent");
  });

  it("gives the picked destination a cue beyond its fill", async () => {
    // --secondary and --muted are close in lightness by design, so the pick
    // cannot rest on the fill alone — same reasoning as the tree row.
    mockRoutes([parent, child, other]);
    renderDocs();

    await screen.findByText("Child");
    openMoveDialogFor("Child");
    fireEvent.click(await screen.findByRole("option", { name: /Other/ }));

    const picked = screen.getByRole("option", { name: /Other/ });
    expect(picked).toHaveClass("font-medium");
    expect(picked.querySelector("[data-selected-bar]")).toBeInTheDocument();

    const notPicked = screen.getByRole("option", { name: /Top level/ });
    expect(notPicked).not.toHaveClass("bg-secondary");
    expect(notPicked.querySelector("[data-selected-bar]")).toBeNull();
  });

  it("keeps the destination list readable in both themes", () => {
    // The pairing is theme tokens, so the readability check belongs to the
    // tokens. Eyeballing is what put --accent here in the first place.
    const { light, dark } = brandkitThemes();

    expect(contrast(light, "--secondary", "--secondary-foreground")).toBeGreaterThanOrEqual(AA_TEXT);
    expect(contrast(dark, "--secondary", "--secondary-foreground")).toBeGreaterThanOrEqual(AA_TEXT);
    // What it replaced, and why it was reported.
    expect(contrast(light, "--accent", "--foreground")).toBeLessThan(AA_TEXT);
    // The bar has to stay visible on the fill it sits on (WCAG 1.4.11).
    expect(contrast(light, "--secondary", "--primary")).toBeGreaterThanOrEqual(AA_NON_TEXT);
    expect(contrast(dark, "--secondary", "--primary")).toBeGreaterThanOrEqual(AA_NON_TEXT);
  });
});

describe("DocsPage — links to a paragraph", () => {
  const doc = makeDoc({ id: "doc-1", title: "Runbook" });

  function mockWithBody(body: string) {
    mockRoutes([doc], (path, opts) => {
      if (path === "/api/v1/documents/doc-1" && !opts?.method) {
        return Promise.resolve({ ...doc, body });
      }
      return undefined;
    });
  }

  beforeEach(() => {
    stubMatch = null;
    vi.mocked(copyText).mockClear();
    vi.mocked(toast.success).mockReset();
  });

  it("copies a link that carries the anchor and points at this document", async () => {
    mockWithBody("The introduction.\n\nSecond paragraph, linked.\n");
    renderDocs("doc-1");

    fireEvent.click(await screen.findByRole("button", { name: /stub copy anchor/i }));

    await waitFor(() => expect(copyText).toHaveBeenCalledTimes(1));
    const url = new URL(vi.mocked(copyText).mock.calls[0]![0]);
    expect(url.pathname).toBe("/w/acme/p/demo/docs/doc-1");
    // Not "contains something anchor-shaped": the link must decode back to the
    // exact anchor the editor handed over, or it points somewhere else.
    expect(anchorFromHash(url.hash)).toEqual(stubAnchor);
    expect(toast.success).toHaveBeenCalled();
  });

  it("passes an incoming anchor down, and passes none when the hash has none", async () => {
    mockWithBody("The introduction.\n\nSecond paragraph, linked.\n");
    const withAnchor = renderDocs("doc-1", anchorToHash(stubAnchor));
    expect(await screen.findByTestId("anchor-received")).toHaveTextContent("yes");
    withAnchor.unmount();

    mockWithBody("The introduction.\n\nSecond paragraph, linked.\n");
    renderDocs("doc-1", "#not-an-anchor");
    expect(await screen.findByTestId("anchor-received")).toHaveTextContent("no");
  });

  it("says nothing when the link landed on the paragraph it was made from", async () => {
    mockWithBody("The introduction.\n\nSecond paragraph, linked.\n");
    stubMatch = { status: "exact", index: 1 };
    renderDocs("doc-1", anchorToHash(stubAnchor));

    fireEvent.click(await screen.findByRole("button", { name: /stub resolve anchor/i }));

    expect(screen.queryByText(/has been edited since/i)).toBeNull();
    expect(screen.queryByText(/no longer in this document/i)).toBeNull();
  });

  it("stays quiet when the paragraph merely moved", async () => {
    mockWithBody("The introduction.\n\nSecond paragraph, linked.\n");
    stubMatch = { status: "moved", index: 3 };
    renderDocs("doc-1", anchorToHash(stubAnchor));

    fireEvent.click(await screen.findByRole("button", { name: /stub resolve anchor/i }));

    expect(screen.queryByText(/has been edited since/i)).toBeNull();
  });

  it("tells the reader when the paragraph itself was edited", async () => {
    mockWithBody("The introduction.\n\nSecond paragraph, rewritten.\n");
    stubMatch = { status: "edited", index: 1 };
    renderDocs("doc-1", anchorToHash(stubAnchor));

    fireEvent.click(await screen.findByRole("button", { name: /stub resolve anchor/i }));

    expect(await screen.findByText(/has been edited since the link was created/i))
      .toBeInTheDocument();
  });

  it("tells the reader when the paragraph could not be found at all", async () => {
    mockWithBody("Something else entirely.\n");
    stubMatch = { status: "lost", index: null };
    renderDocs("doc-1", anchorToHash(stubAnchor));

    fireEvent.click(await screen.findByRole("button", { name: /stub resolve anchor/i }));

    expect(await screen.findByText(/no longer in this document/i)).toBeInTheDocument();
  });

  it("drops the notice when the reader starts editing", async () => {
    mockWithBody("Something else entirely.\n");
    stubMatch = { status: "lost", index: null };
    renderDocs("doc-1", anchorToHash(stubAnchor));

    fireEvent.click(await screen.findByRole("button", { name: /stub resolve anchor/i }));
    expect(await screen.findByText(/no longer in this document/i)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /edit/i }));

    // A verdict about a link is about the document as the reader arrived at it;
    // leaving it on screen while they rewrite the page turns it into a lie.
    expect(screen.queryByText(/no longer in this document/i)).toBeNull();
  });
});

describe("DocsPage — the discussion under the page", () => {
  const doc = makeDoc({ id: "doc-1", title: "Deploy" });
  const BODY = "Run the migration first, then swap the image.\n";

  /** One anchored comment, quoting words that really are in BODY. */
  function comment() {
    return {
      id: "dc-1",
      document_id: "doc-1",
      parent_comment_id: null,
      author_id: "u1",
      author_type: "user",
      author_name: "Pavel",
      body: "Say why the order matters.",
      anchor: {
        exact: "Run the migration first",
        prefix: "",
        suffix: "",
        start: 0,
        end: 23,
        orphaned: false,
      },
      resolved_at: null,
      resolved_by: null,
      resolved_by_type: null,
      created_at: "2026-08-19T10:00:00Z",
      updated_at: "2026-08-19T10:00:00Z",
    };
  }

  function mockWithComments(items: unknown[]) {
    mockRoutes([doc], (path, opts) => {
      if (path === "/api/v1/documents/doc-1" && !opts?.method) {
        return Promise.resolve({ ...doc, body: BODY });
      }
      if (path === "/api/v1/documents/doc-1/comments") {
        return Promise.resolve({ items, has_more: false });
      }
      return undefined;
    });
  }

  it("renders the tree OUTSIDE the element the comment layer measures", async () => {
    // The load-bearing structural invariant, and the reason this test is at page
    // level rather than in the component's own file. `doc-measured-body` is
    // flattened into "the text of this document" to locate each quote. If the
    // discussion is rendered inside it, every comment body joins the haystack —
    // and a quote starts resolving to the text of a comment about it. That
    // failure is silent, gets worse the more people talk, and no assertion
    // inside doc-comment-tree.test.tsx can see it, because the component does
    // not choose where it is mounted.
    mockWithComments([comment()]);
    renderDocs("doc-1");

    const tree = await screen.findByTestId("doc-comment-tree");
    const measured = screen.getByTestId("doc-measured-body");

    expect(measured).toBeInTheDocument();
    expect(measured.contains(tree)).toBe(false);
    // Positive control on the probe itself: `contains` must be capable of
    // returning true here, or the assertion above passes for a container that
    // matched nothing.
    expect(measured.contains(screen.getByTestId("doc-view"))).toBe(true);
  });

  it("still renders the section — with its composer — on a page with no comments", async () => {
    // #744ae979: the tree used to unmount entirely here. It can't any more —
    // it carries the bottom composer, which needs somewhere to render before
    // the first comment exists.
    mockWithComments([]);
    renderDocs("doc-1");

    await screen.findByTestId("doc-view");
    expect(screen.getByTestId("doc-comment-tree")).toBeInTheDocument();
    expect(
      screen.getByPlaceholderText("Add a comment… @ to mention"),
    ).toBeInTheDocument();
  });

  /**
   * AC1 — the bottom form is available with NO selection and NO prior click,
   * in both rail states. This is the load-bearing test for the card's whole
   * premise: `#a9df0f4a`'s fix hid this exact composer whenever the rail was
   * open, and Pavel's decision is specifically that the hiding was wrong.
   */
  it.each([
    ["closed", "0"],
    ["open", "1"],
  ] as const)("AC1: the bottom composer is available with the rail %s", async (_label, stored) => {
    mockWithComments([comment()]);
    localStorage.setItem("mesh_docs_comments_rail", stored);
    renderDocs("doc-1");

    const tree = await screen.findByTestId("doc-comment-tree");
    if (stored === "1") await screen.findByTestId("doc-comment-rail");

    expect(
      within(tree).getByPlaceholderText("Add a comment… @ to mention"),
    ).toBeInTheDocument();
  });

  describe("the rail and the tree render together, once per surface (#744ae979)", () => {
    /** Never resolves in BODY — the server's placement, not ours. */
    function pageComment() {
      return {
        id: "dc-page",
        document_id: "doc-1",
        parent_comment_id: null,
        author_id: "u1",
        author_type: "user",
        author_name: "Pavel",
        body: "A note on the document as a whole.",
        anchor: null,
        resolved_at: null,
        resolved_by: null,
        resolved_by_type: null,
        created_at: "2026-08-19T09:00:00Z",
        updated_at: "2026-08-19T09:00:00Z",
      };
    }

    /** A quote the current page text no longer contains — orphaned by the server. */
    function orphanedComment() {
      return {
        id: "dc-orphan",
        document_id: "doc-1",
        parent_comment_id: null,
        author_id: "u1",
        author_type: "user",
        author_name: "Pavel",
        body: "This text isn't here any more.",
        anchor: {
          exact: "a sentence that got deleted",
          prefix: "",
          suffix: "",
          start: null,
          end: null,
          orphaned: true,
        },
        resolved_at: null,
        resolved_by: null,
        resolved_by_type: null,
        created_at: "2026-08-19T08:00:00Z",
        updated_at: "2026-08-19T08:00:00Z",
      };
    }

    /**
     * Pavel's reversal of `#a9df0f4a` (2026-08-23, `#744ae979`): the rail and
     * the tree are supposed to show the discussion at the same time, not take
     * turns. So with the rail open, each of the three threads legitimately
     * appears TWICE on screen — once per surface — which is why the count
     * that matters is per-surface, not per-document. Counting unscoped here
     * would make a correct implementation look like the `#a9df0f4a` bug; that
     * trap is exactly why the card's AC4 asked for this reformulation before
     * any test got written.
     */
    it("renders each thread exactly once PER SURFACE with the rail open, and keeps the tree", async () => {
      mockWithComments([comment(), pageComment(), orphanedComment()]);
      localStorage.setItem("mesh_docs_comments_rail", "1");
      renderDocs("doc-1");

      const rail = await screen.findByTestId("doc-comment-rail");
      const tree = await screen.findByTestId("doc-comment-tree");
      await waitFor(() => {
        expect(within(rail).getAllByTestId("doc-comment-thread")).toHaveLength(3);
      });
      expect(within(tree).getAllByTestId("doc-comment-thread")).toHaveLength(3);
      // Six on the page as a whole: three threads, two surfaces, nothing
      // repeated a third time within either one.
      expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(6);
    });

    it("with the rail closed, the tree alone carries all three", async () => {
      mockWithComments([comment(), pageComment(), orphanedComment()]);
      localStorage.setItem("mesh_docs_comments_rail", "0");
      renderDocs("doc-1");

      await screen.findByTestId("doc-comment-tree");
      await waitFor(() => {
        expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(3);
      });
      expect(screen.queryByTestId("doc-comment-rail")).not.toBeInTheDocument();
    });

    it("NEGATIVE CONTROL: closing the rail after opening it drops it back to the tree's three", async () => {
      // Guards the two tests above from going vacuous on a future refactor
      // that always renders 3 (e.g. de-duplicating across surfaces, which
      // would silently defeat AC1 — the bottom form must work independently
      // of the rail). Toggling within one render proves the count actually
      // tracks `railOpen` rather than being a fixture artefact.
      mockWithComments([comment(), pageComment(), orphanedComment()]);
      localStorage.setItem("mesh_docs_comments_rail", "1");
      renderDocs("doc-1");

      await screen.findByTestId("doc-comment-rail");
      await waitFor(() => {
        expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(6);
      });

      fireEvent.click(screen.getByTestId("doc-comment-toggle"));

      await waitFor(() => {
        expect(screen.queryByTestId("doc-comment-rail")).not.toBeInTheDocument();
      });
      expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(3);
    });

    /**
     * Acceptance criterion 2 — the entry for a comment on the document as a
     * whole is not lost by hiding one of the two surfaces.
     *
     * This was the weakest part of the fix's evidence: it held only by
     * reading the code (`Composer` lives in the rail, so hiding the tree
     * cannot take it away). Reading is not a check — a later refactor that
     * moved the composer into the tree would break the criterion while every
     * other test here stayed green, because none of them looks at whether the
     * discussion is still *writable*.
     *
     * What makes the claim true is structural: `doc-comment-tree.tsx` imports
     * the very same `Thread` from `doc-comment-rail.tsx`, so whichever
     * surface is on screen carries the same reply entry. The assertion below
     * pins that, on the page-as-a-whole thread specifically — the one class
     * of thread that has nowhere else to live.
     *
     * Scoped to the tree rather than an unscoped `findByText`: since the tree
     * is now always mounted, the same text is on screen twice with the rail
     * open (once per surface), and an unscoped query would throw on the
     * duplicate instead of testing writability at all.
     */
    it("AC2: the page-as-a-whole thread stays writable in BOTH rail states", async () => {
      for (const railOpen of [true, false] as const) {
        mockWithComments([comment(), pageComment(), orphanedComment()]);
        localStorage.setItem("mesh_docs_comments_rail", railOpen ? "1" : "0");
        const { unmount } = renderDocs("doc-1");

        const tree = await screen.findByTestId("doc-comment-tree");
        if (railOpen) await screen.findByTestId("doc-comment-rail");

        // The thread that exists only as "the document as a whole" — it has no
        // anchor to fall back to, so if a surface drops it, it is unreachable.
        const whole = await within(tree).findByText("On the page as a whole");
        const card = whole.closest('[data-testid="doc-comment-thread"]');
        expect(card).not.toBeNull();

        // Writable, not merely visible: a reply entry inside that very thread.
        expect(
          within(card as HTMLElement).getByRole("button", { name: /reply/i }),
        ).toBeInTheDocument();

        // And the switch between the two surfaces is always on screen, so the
        // reader is never stranded on the one that is currently hidden.
        expect(screen.getByTestId("doc-comment-toggle")).toBeInTheDocument();

        unmount();
        localStorage.clear();
        vi.clearAllMocks();
      }
    });
  });

  describe("below `lg`, one COMMENTS surface, not two (#fd898a58)", () => {
    // jsdom does not evaluate media queries (see the "narrow screens" describe
    // above), and this fix genuinely needs JS to decide whether to mount the
    // rail at all — a CSS-only `hidden` would still leave it in the DOM (and
    // running), which is exactly what AC1 below rules out. So these tests
    // replace `window.matchMedia` directly rather than relying on jsdom's
    // (nonexistent) layout, matching `useIsMobile`'s own contract.
    let restoreMatchMedia: (() => void) | undefined;

    function mockViewport(isMobile: boolean) {
      const previous = window.matchMedia;
      window.matchMedia = ((query: string) => {
        const target = new EventTarget();
        return Object.assign(target, {
          matches: isMobile,
          media: query,
          onchange: null,
          addListener: () => {},
          removeListener: () => {},
          addEventListener: target.addEventListener.bind(target),
          removeEventListener: target.removeEventListener.bind(target),
          dispatchEvent: target.dispatchEvent.bind(target),
        }) as MediaQueryList;
      }) as typeof window.matchMedia;
      restoreMatchMedia = () => {
        window.matchMedia = previous;
      };
    }

    afterEach(() => {
      restoreMatchMedia?.();
      restoreMatchMedia = undefined;
    });

    it("AC1: exactly one COMMENTS surface renders, even with the rail stored open", async () => {
      mockViewport(true);
      mockWithComments([comment()]);
      localStorage.setItem("mesh_docs_comments_rail", "1");
      renderDocs("doc-1");

      const tree = await screen.findByTestId("doc-comment-tree");
      // The bug this guards: `railOpen=true` used to mount the rail
      // regardless of width, landing it directly under the always-mounted
      // tree — same threads, second header, right where the reader was
      // already scrolling.
      expect(screen.queryByTestId("doc-comment-rail")).not.toBeInTheDocument();
      expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(1);
      // And the one surface that IS on screen still carries the composer —
      // AC1 is "one surface with everything", not "one surface, form gone".
      expect(
        within(tree).getByPlaceholderText("Add a comment… @ to mention"),
      ).toBeInTheDocument();
    });

    it("AC2: the rail toggle is not shown — nothing visible controls a surface that cannot open", async () => {
      mockViewport(true);
      mockWithComments([comment()]);
      renderDocs("doc-1");

      await screen.findByTestId("doc-comment-tree");
      expect(screen.queryByTestId("doc-comment-toggle")).not.toBeInTheDocument();
    });

    it("AC3 regression: at `lg`+ the rail still renders alongside the tree, once per surface", async () => {
      mockViewport(false);
      mockWithComments([comment()]);
      localStorage.setItem("mesh_docs_comments_rail", "1");
      renderDocs("doc-1");

      await screen.findByTestId("doc-comment-tree");
      await screen.findByTestId("doc-comment-rail");
      await waitFor(() => {
        expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(2);
      });
      expect(screen.getByTestId("doc-comment-toggle")).toBeInTheDocument();
    });
  });

  describe("comment on the page as a whole, no selection required (#41d01325)", () => {
    // jsdom has no layout, so it has no scrollIntoView — the rail calls it on
    // the thread it focuses, and submitting a draft focuses the new thread.
    beforeAll(() => {
      Element.prototype.scrollIntoView = vi.fn();
    });

    /**
     * `mockWithComments` only answers the GET; this also answers the POST that
     * `submitDraft` sends, and hands the sent body back to the test so AC2 can
     * assert on the wire shape rather than trusting what the code claims to send.
     */
    function mockWithComposePost(
      items: unknown[],
      onPost: (body: unknown) => unknown,
    ) {
      mockRoutes([doc], (path, opts) => {
        if (path === "/api/v1/documents/doc-1" && !opts?.method) {
          return Promise.resolve({ ...doc, body: BODY });
        }
        if (path === "/api/v1/documents/doc-1/comments") {
          if ((opts?.method ?? "GET") === "POST") {
            return Promise.resolve(onPost(opts?.body));
          }
          return Promise.resolve({ items, has_more: false });
        }
        return undefined;
      });
    }

    function createdPageComment(body: unknown) {
      return {
        id: "dc-page-new",
        document_id: "doc-1",
        parent_comment_id: null,
        author_id: "u1",
        author_type: "user",
        author_name: "Pavel",
        body: (body as { body: string }).body,
        anchor: null,
        resolved_at: null,
        resolved_by: null,
        resolved_by_type: null,
        created_at: "2026-08-23T12:00:00Z",
        updated_at: "2026-08-23T12:00:00Z",
      };
    }

    // Scoped to the rail throughout: since #744ae979 the tree carries its own
    // permanently-mounted composer with the identical placeholder and submit
    // label (reused copy, not a coincidence — see #744ae979's instruction not
    // to invent new strings), so an unscoped query now matches two composers
    // whenever the rail is open.

    it("AC1: reaches the composer with nothing selected, rail already open", async () => {
      mockWithComposePost([], createdPageComment);
      localStorage.setItem("mesh_docs_comments_rail", "1");
      renderDocs("doc-1");

      const rail = await screen.findByTestId("doc-comment-rail");
      // jsdom's window.getSelection() is collapsed by default — there is no
      // selection here, unlike every other way to reach the composer.
      fireEvent.click(screen.getByTestId("doc-comment-page-entry"));

      await within(rail).findByPlaceholderText(/@ to mention/i);
    });

    it("AC1: reaches the composer with nothing selected, rail starts closed", async () => {
      mockWithComposePost([], createdPageComment);
      localStorage.setItem("mesh_docs_comments_rail", "0");
      renderDocs("doc-1");

      await screen.findByTestId("doc-view");
      expect(screen.queryByTestId("doc-comment-rail")).not.toBeInTheDocument();

      fireEvent.click(screen.getByTestId("doc-comment-page-entry"));

      // The click opens the rail itself — the composer has to render somewhere.
      const rail = await screen.findByTestId("doc-comment-rail");
      expect(
        within(rail).getByPlaceholderText(/@ to mention/i),
      ).toBeInTheDocument();
    });

    it("AC2: the request omits anchor, and the reply is placed as page — same wording a live page thread uses", async () => {
      let sentBody: unknown;
      mockWithComposePost([], (body) => {
        sentBody = body;
        return createdPageComment(body);
      });
      localStorage.setItem("mesh_docs_comments_rail", "1");
      renderDocs("doc-1");

      const rail = await screen.findByTestId("doc-comment-rail");
      fireEvent.click(screen.getByTestId("doc-comment-page-entry"));
      fireEvent.change(await within(rail).findByPlaceholderText(/@ to mention/i), {
        target: { value: "A note on the whole page." },
      });
      fireEvent.click(within(rail).getByRole("button", { name: /^comment$/i }));

      // `findByText` alone, not chained into `.toBeInTheDocument()` on its
      // resolved value: this component re-renders more than once while the
      // draft settles, and a node `findByText` matched on one poll can be
      // gone by the time a *separate* synchronous assertion runs against that
      // same reference. `findByText` throwing IS the check. Scoped to the
      // rail: once the reply lands, the tree renders the identical text too.
      await within(rail).findByText("A note on the whole page.");

      // Not `anchor: null` — the key must be absent, per
      // CreateDocumentCommentRequest's own contract ("omitted for a page-level
      // comment"). A `null` value is a different wire shape even though both
      // are falsy in JS.
      expect(sentBody).not.toHaveProperty("anchor");
      expect(sentBody).toEqual({ body: "A note on the whole page." });

      // What the server hands back is what `placeAnchor` turns into "page" —
      // rendered with the exact wording an existing live page-thread already
      // carries (PlacementNotice), not a new string this task would have had
      // to introduce under its own gate.
      expect(within(rail).getByText("On the page as a whole")).toBeInTheDocument();
    });

    it("AC4: the new thread appears once per surface, not twice within either one", async () => {
      // Reformulated per the card's trap note: with both surfaces mounted,
      // one thread legitimately paints twice on the page as a whole — once in
      // the rail, once in the tree. What must NOT happen is either surface
      // showing its own copy more than once.
      mockWithComposePost([], createdPageComment);
      localStorage.setItem("mesh_docs_comments_rail", "1");
      renderDocs("doc-1");

      const rail = await screen.findByTestId("doc-comment-rail");
      const tree = await screen.findByTestId("doc-comment-tree");
      fireEvent.click(screen.getByTestId("doc-comment-page-entry"));
      fireEvent.change(await within(rail).findByPlaceholderText(/@ to mention/i), {
        target: { value: "A note on the whole page." },
      });
      fireEvent.click(within(rail).getByRole("button", { name: /^comment$/i }));

      await within(rail).findByText("A note on the whole page.");
      await waitFor(() => {
        expect(within(tree).getAllByTestId("doc-comment-thread")).toHaveLength(1);
      });
      expect(within(rail).getAllByTestId("doc-comment-thread")).toHaveLength(1);
      expect(screen.getAllByTestId("doc-comment-thread")).toHaveLength(2);
    });
  });
});

describe("DocsPage — narrow screens: the route decides which column you get", () => {
  const doc = makeDoc({ id: "doc-1", title: "Runbook" });

  /**
   * jsdom does not evaluate media queries, so "is it on screen at 393px" is not
   * a question the DOM can answer here. What it can answer is the mechanism
   * that decides it: which of the two columns carries `hidden`, and whether the
   * `md:` escape hatch that keeps the desktop two-column is still on both. The
   * live 393px/1440px proof is the screenshot pair on the card.
   */
  function columns(container: HTMLElement) {
    const tree = container.querySelector("aside");
    const page = container.querySelector("section");
    if (!tree || !page) throw new Error("expected a tree column and a page column");
    return { tree, page };
  }

  it("gives the screen to the tree when no page is open", async () => {
    mockRoutes([doc]);
    const { container } = renderDocs();
    await screen.findByText("Runbook");

    const { tree, page } = columns(container);
    expect(tree.className).not.toMatch(/(^|\s)hidden(\s|$)/);
    expect(page.className).toMatch(/(^|\s)hidden(\s|$)/);
  });

  it("gives the screen to the page once one is open", async () => {
    mockRoutes([doc], (path) =>
      path === "/api/v1/documents/doc-1"
        ? Promise.resolve({ ...doc, body: "text" })
        : undefined,
    );
    const { container } = renderDocs("doc-1");
    await screen.findByTestId("doc-view");

    const { tree, page } = columns(container);
    expect(tree.className).toMatch(/(^|\s)hidden(\s|$)/);
    expect(page.className).not.toMatch(/(^|\s)hidden(\s|$)/);
  });

  it("keeps both columns from `md` up, whichever half the route is showing", async () => {
    mockRoutes([doc]);
    const { container, unmount } = renderDocs();
    await screen.findByText("Runbook");

    // The negative control for the whole change: a fix that swapped one column
    // for the other unconditionally would pass both tests above and quietly
    // take the second column away from every desktop.
    let cols = columns(container);
    expect(cols.tree.className).toContain("md:flex");
    expect(cols.page.className).toContain("md:flex");
    unmount();

    mockRoutes([doc], (path) =>
      path === "/api/v1/documents/doc-1"
        ? Promise.resolve({ ...doc, body: "text" })
        : undefined,
    );
    const second = renderDocs("doc-1");
    await screen.findByTestId("doc-view");

    cols = columns(second.container);
    expect(cols.tree.className).toContain("md:flex");
    expect(cols.page.className).toContain("md:flex");
  });

  it("carries the dragged width as a variable, not as an inline width", async () => {
    mockRoutes([doc]);
    const { container } = renderDocs();
    await screen.findByText("Runbook");

    // An inline `width` outranks every class, so the tree would keep its
    // desktop width on a 393px screen and push the page off the side. The
    // width has to arrive as a custom property that only `md:w-[…]` consumes.
    const { tree } = columns(container);
    expect(tree.style.width).toBe("");
    expect(tree.style.getPropertyValue("--doc-tree-width")).toBe(
      `${DOC_TREE_DEFAULT_WIDTH}px`,
    );
    expect(tree.className).toContain("md:w-[var(--doc-tree-width)]");
  });

  it("offers a way back from a page that fails to load", async () => {
    mockRoutes([doc], (path) =>
      path === "/api/v1/documents/doc-1"
        ? Promise.reject(new Error("Document not found"))
        : undefined,
    );
    renderDocs("doc-1");
    await screen.findByText("Document not found");

    // This is the one state with no breadcrumbs, so on a phone — where the tree
    // has yielded the screen — a dead link would otherwise be a dead end.
    fireEvent.click(screen.getByRole("button", { name: /^Documents$/ }));
    expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs");
  });
});

describe("DocsPage — search", () => {
  const SEARCH_PATH = "/api/v1/projects/proj-1/documents/search";

  it("renders a search box in the tree column", async () => {
    mockRoutes([]);
    renderDocs();

    expect(
      await screen.findByRole("searchbox", { name: /search documents/i }),
    ).toBeInTheDocument();
  });

  it("finds a document by a phrase from its body, not just its title", async () => {
    const docs = [makeDoc({ id: "doc-1", title: "Runbook" })];
    mockRoutes(docs, (path, opts) => {
      if (path === SEARCH_PATH && (opts?.method ?? "GET") === "GET") {
        expect(opts?.params).toMatchObject({ q: "rotate the key" });
        return Promise.resolve({
          items: [
            {
              id: "doc-1",
              title: "Runbook",
              snippet: `...${MATCH_START}rotate the key${MATCH_END} before...`,
              snippet_is_match: true,
            },
          ],
        });
      }
      return undefined;
    });

    renderDocs();
    fireEvent.change(
      await screen.findByRole("searchbox", { name: /search documents/i }),
      { target: { value: "rotate the key" } },
    );

    // The highlighted run, not a plain title match — this is the search
    // reaching the body, which is what the tree's own title list never did.
    const hit = await screen.findByText("rotate the key");
    expect(hit.tagName).toBe("MARK");
  });

  it("opens the matched document and restores the tree", async () => {
    const docs = [makeDoc({ id: "doc-1", title: "Other" })];
    mockRoutes(docs, (path, opts) => {
      if (path === SEARCH_PATH && (opts?.method ?? "GET") === "GET") {
        return Promise.resolve({
          items: [{ id: "doc-1", title: "Other", snippet: "", snippet_is_match: false }],
        });
      }
      return undefined;
    });

    renderDocs();
    const box = await screen.findByRole("searchbox", { name: /search documents/i });
    fireEvent.change(box, { target: { value: "other" } });

    fireEvent.click(await screen.findByRole("button", { name: /Other/ }));

    expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs/doc-1");
    // Picking a result is a way to a document, not a new column — the query
    // clears and the ordinary tree comes back.
    expect(box).toHaveValue("");
  });

  it("does not query the server for an empty query", async () => {
    mockRoutes([makeDoc({ id: "doc-1", title: "Runbook" })], (path) => {
      if (path === SEARCH_PATH) throw new Error("must not search on an empty query");
      return undefined;
    });

    renderDocs();
    const box = await screen.findByRole("searchbox", { name: /search documents/i });
    fireEvent.change(box, { target: { value: "x" } });
    fireEvent.change(box, { target: { value: "" } });

    // Nothing to assert on directly — the mock above throws if it is called.
    // Give the debounce window a chance to fire before the test ends.
    await new Promise((r) => setTimeout(r, 250));
  });
});
