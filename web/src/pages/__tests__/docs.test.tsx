import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

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
vi.mock("@/components/doc-editor", () => ({
  DocEditor: ({
    value,
    onChange,
    readOnly,
  }: {
    value: string;
    onChange: (v: string) => void;
    readOnly?: boolean;
  }) => {
    if (readOnly) {
      return value.trim() ? (
        <div data-testid="doc-view">{value}</div>
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

import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { DocsPage } from "@/pages/docs";
import { useDocumentStore } from "@/stores/document";
import { useProjectStore } from "@/stores/project";
import type { Project, ProjectDocument } from "@/types";

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
    ...overrides,
  };
}

const LIST_PATH = "/api/v1/projects/proj-1/documents";

/** Answers the list endpoint; anything else must be handled by the test. */
function mockRoutes(
  docs: ProjectDocument[],
  extra?: (path: string, opts?: { method?: string; body?: unknown }) => unknown,
) {
  mockedApi.mockImplementation(
    (path: string, opts?: { method?: string; body?: unknown }) => {
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

function renderDocs(docId?: string) {
  const base = "/w/acme/p/demo/docs";
  return render(
    <MemoryRouter initialEntries={[docId ? `${base}/${docId}` : base]}>
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
  vi.mocked(toast.error).mockReset();
  useProjectStore.setState({ currentProject: PROJECT });
  useDocumentStore.getState().reset();
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
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
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

    expect(patched).toEqual([{ body: "hello world" }]);
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

    await waitFor(() => expect(patched).toEqual([{ body: "half-typed" }]));
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
