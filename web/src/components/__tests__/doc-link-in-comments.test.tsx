import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const mockedApi = vi.fn();
const mockedMentionables = vi.fn(async (_workspaceId: string, _query: string) => [] as unknown[]);
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...args),
  getAccessToken: vi.fn(() => null),
  // Same module as `api`, and easy to forget: without it the mention fetch
  // throws, the component swallows it, and the menu simply never appears —
  // which reads exactly like the document picker having broken mentions.
  getMentionables: (workspaceId: string, query: string) =>
    mockedMentionables(workspaceId, query),
}));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));
vi.mock("@/lib/docs/linkable-documents", () => ({
  fetchLinkableDocuments: vi.fn(async () => [{ id: "doc-1", title: "Deploy runbook" }]),
  forgetLinkableDocuments: vi.fn(),
}));

// The content search too. Its subject is a different test file; leaving it
// unmocked here makes this one wait on a debounce and a rejected request before
// the local title matches render — which passes on a quiet machine and fails on
// a loaded CI runner. A test that depends on how fast the box is does not test
// what it says it does.
vi.mock("@/lib/docs/document-search", async () => {
  const actual = await vi.importActual<typeof import("@/lib/docs/document-search")>(
    "@/lib/docs/document-search",
  );
  return {
    ...actual,
    searchDocuments: vi.fn(async () => []),
    searchRelayDocuments: vi.fn(async () => []),
  };
});

import { CommentList } from "@/components/comment-list";
import { useProjectStore } from "@/stores/project";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Project, Workspace } from "@/types";

/**
 * The comment box writes with the same editor the task description does.
 *
 * This file used to run the `[[` and `@` menus against CommentList directly,
 * because there were three separate editors and the risk was that a writer
 * would learn `[[` on a task and find it dead in the comment box underneath.
 *
 * That risk is now structural rather than behavioural: the comment composer,
 * the comment edit box, the task description and the create dialog all mount
 * ONE component (RichTextEditor), so there is no second implementation left to
 * drift. The behaviour those tests asserted — which trigger opens, what the
 * accepted suggestion inserts, the exact markdown that gets saved — is asserted
 * against a real editor in lib/milkdown/__tests__/suggestion.test.ts, where it
 * can be driven by ProseMirror positions instead of by a textarea's
 * selectionStart.
 *
 * What is worth keeping here is the wiring claim, and it is the one that would
 * actually regress: that the comment box mounts that editor at all, rather than
 * quietly falling back to a plain textarea with no menus and no rich text. Both
 * places a comment is written are checked, because the edit box was the one
 * that got forgotten before.
 */

const PROJECT = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Demo",
  slug: "demo",
  settings: {},
} as unknown as Project;
const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as unknown as Workspace;

beforeEach(() => {
  mockedApi.mockReset();
  mockedApi.mockResolvedValue({ items: [], total_count: 0, has_more: false });
  mockedMentionables.mockReset();
  mockedMentionables.mockResolvedValue([]);
  useProjectStore.setState({ projects: [PROJECT], currentProject: PROJECT });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
  // Seeded so the component does not fetch it: the directory feeds the mention
  // menu, which is not what these tests are about, and an unseeded one makes the
  // whole component crash on a shape the stubbed api does not return.
  useRulesStore.setState({
    teamDirectory: { agents: [], humans: [] } as never,
  });
});

/**
 * The writing surface, identified by what makes it the shared editor rather
 * than a textarea: a ProseMirror contenteditable inside the box.
 */
function editorSurfaces(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>(".mesh-doc-prose"));
}

describe("CommentList — the writing surface", () => {
  it("mounts the shared rich-text editor, not a textarea", async () => {
    render(
      <MemoryRouter>
        <CommentList taskId="task-1" projId={PROJECT.id} />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(editorSurfaces().length).toBeGreaterThan(0);
    });
    const surface = editorSurfaces()[0]!;

    expect(surface.getAttribute("contenteditable")).toBe("true");
    // The negative control. Asserting only "an editor is present" would pass on
    // a box that still rendered the old textarea beside it.
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("offers the same formatting affordances the task description has", async () => {
    // These come from RichTextEditor's toolbar. If the comment box were wired to
    // some cut-down surface instead, this is what would be missing.
    render(
      <MemoryRouter>
        <CommentList taskId="task-1" projId={PROJECT.id} />
      </MemoryRouter>,
    );

    expect(await screen.findByTitle("Table")).toBeInTheDocument();
    expect(screen.getByTitle("Code block")).toBeInTheDocument();
    expect(screen.getByTitle("Bullet list")).toBeInTheDocument();
  });
});
