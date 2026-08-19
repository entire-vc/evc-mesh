import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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
 * The third surface.
 *
 * Comments already had an inline `@` menu before this unit; the description and
 * markdown editors had nothing. The risk in fixing that is the mirror image —
 * building the document picker for the two that were missing it and leaving the
 * one that already had a menu alone, so a writer learns `[[` on a task and finds
 * it dead in the comment box underneath.
 *
 * Hence this file, separate from the editor table only because CommentList needs
 * its own fetch stubbed.
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

function commentBox(): HTMLTextAreaElement {
  return screen.getByPlaceholderText(/to link a document/i) as HTMLTextAreaElement;
}

describe("CommentList — linking a document", () => {
  it("offers the same [[ picker the task editors have", async () => {
    render(
      <MemoryRouter>
        <CommentList taskId="task-1" projId={PROJECT.id} />
      </MemoryRouter>,
    );
    const el = await waitFor(commentBox);

    fireEvent.change(el, { target: { value: "See [[run", selectionStart: 9 } });

    expect(await screen.findByRole("option", { name: /Deploy runbook/ })).toBeInTheDocument();
  });

  it("inserts the same link the task editors insert", async () => {
    render(
      <MemoryRouter>
        <CommentList taskId="task-1" projId={PROJECT.id} />
      </MemoryRouter>,
    );
    const el = await waitFor(commentBox);
    fireEvent.change(el, { target: { value: "See [[run", selectionStart: 9 } });

    fireEvent.mouseDown(await screen.findByRole("option", { name: /Deploy runbook/ }));

    await waitFor(() => {
      expect(el.value).toBe("See [Deploy runbook](/w/acme/p/demo/docs/doc-1) ");
    });
  });

  it("leaves the mention menu working", async () => {
    // The negative control for the wiring: the document picker takes the keydown
    // first, and a version that swallowed every key would silently kill `@`.
    mockedMentionables.mockResolvedValue([
      { id: "u1", slug: "pavel", display_name: "Pavel", kind: "user" },
    ]);
    render(
      <MemoryRouter>
        <CommentList taskId="task-1" projId={PROJECT.id} />
      </MemoryRouter>,
    );
    const el = await waitFor(commentBox);

    fireEvent.change(el, { target: { value: "ping @pav", selectionStart: 9 } });

    // The mention fetch is debounced 150ms, so this waits rather than asserting
    // on the render that follows the keystroke.
    expect(await screen.findByText("@pavel", {}, { timeout: 2000 })).toBeInTheDocument();
  });
});
