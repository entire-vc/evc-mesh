import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));

// The picker's own data source, stubbed so these tests are about the wiring in
// each editor rather than about pagination.
// Same reason as in doc-link-in-comments: this file's subject is the wiring in
// each editor, not the content search, and leaving the search unmocked makes
// every assertion here wait on a debounce first.
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

vi.mock("@/lib/docs/linkable-documents", () => ({
  fetchLinkableDocuments: vi.fn(async () => [
    { id: "doc-1", title: "Deploy runbook" },
    { id: "doc-2", title: "Onboarding" },
  ]),
  forgetLinkableDocuments: vi.fn(),
}));

import { DescriptionEditor } from "@/components/description-editor";
import { MarkdownEditor } from "@/components/markdown-editor";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Project, Workspace } from "@/types";

/**
 * Linking a document from a task.
 *
 * The acceptance criterion for this unit is not "an inline picker exists" — one
 * already did, in comments only, and that is exactly the gap. It is that the
 * SAME affordance works in the description editor and the markdown editor too.
 *
 * So the surfaces are a table and every behaviour runs against all of them. A
 * suite written against one editor would pass on the state this unit was created
 * to fix.
 */

const PROJECT = {
  id: "proj-1",
  workspace_id: "ws-1",
  name: "Demo",
  slug: "demo",
  description: "",
  icon: "",
  settings: {},
  default_assignee_type: "none",
  is_archived: false,
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
} as unknown as Project;

const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as unknown as Workspace;

/** Every editor a writer can link a document from. */
const SURFACES = [
  {
    name: "DescriptionEditor",
    render: (value: string, onChange: (v: string) => void) => (
      <DescriptionEditor value={value} onChange={onChange} projId={PROJECT.id} />
    ),
  },
  {
    name: "MarkdownEditor",
    render: (value: string, onChange: (v: string) => void) => (
      <MarkdownEditor value={value} onChange={onChange} projectId={PROJECT.id} attachments={false} />
    ),
  },
] as const;

function textarea(): HTMLTextAreaElement {
  return screen.getByRole("textbox") as HTMLTextAreaElement;
}

/** Type `text` and put the caret at its end, the way a writer would. */
function type(el: HTMLTextAreaElement, text: string) {
  fireEvent.change(el, { target: { value: text, selectionStart: text.length } });
}

beforeEach(() => {
  useProjectStore.setState({ projects: [PROJECT], currentProject: PROJECT });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
});

describe.each(SURFACES)("$name — linking a document", (surface) => {
  function mount(initial = "") {
    let value = initial;
    const onChange = vi.fn((next: string) => {
      value = next;
      rerender();
    });
    const view = render(<MemoryRouter>{surface.render(value, onChange)}</MemoryRouter>);
    const rerender = () =>
      view.rerender(<MemoryRouter>{surface.render(value, onChange)}</MemoryRouter>);
    return { onChange, current: () => value };
  }

  it("opens the menu on [[ and lists the project's documents", async () => {
    mount();
    type(textarea(), "See [[");

    expect(await screen.findByRole("option", { name: /Deploy runbook/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Onboarding/ })).toBeInTheDocument();
  });

  it("filters as the writer types", async () => {
    mount();
    type(textarea(), "See [[onb");

    expect(await screen.findByRole("option", { name: /Onboarding/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Deploy runbook/ })).toBeNull();
  });

  it("inserts a real markdown link to the document's route", async () => {
    const { current } = mount();
    type(textarea(), "See [[run");

    fireEvent.mouseDown(await screen.findByRole("option", { name: /Deploy runbook/ }));

    // The whole point of the unit, asserted as the exact text the writer ends up
    // with — not as "something was inserted".
    expect(current()).toBe("See [Deploy runbook](/w/acme/p/demo/docs/doc-1) ");
  });

  it("accepts the highlighted suggestion on Enter", async () => {
    const { current } = mount();
    const el = textarea();
    type(el, "See [[");
    await screen.findByRole("option", { name: /Deploy runbook/ });

    fireEvent.keyDown(el, { key: "ArrowDown" });
    fireEvent.keyDown(el, { key: "Enter" });

    await waitFor(() => {
      expect(current()).toContain("[Onboarding](/w/acme/p/demo/docs/doc-2)");
    });
  });

  it("closes on Escape without touching the text", async () => {
    const { current } = mount();
    const el = textarea();
    type(el, "See [[");
    await screen.findByRole("option", { name: /Deploy runbook/ });

    fireEvent.keyDown(el, { key: "Escape" });

    await waitFor(() => {
      expect(screen.queryByRole("option")).toBeNull();
    });
    expect(current()).toBe("See [[");
  });

  it("stays closed for ordinary prose", () => {
    mount();
    type(textarea(), "a normal [link](https://example.com) in a sentence");

    expect(screen.queryByRole("option")).toBeNull();
  });
});

describe("the menu is honest when nothing matches", () => {
  it("says so rather than offering every document", async () => {
    // The failure mode of a filter written the other way round: no match falls
    // back to the whole list, and the writer picks the wrong document.
    render(
      <MemoryRouter>
        <DescriptionEditor value="" onChange={vi.fn()} projId={PROJECT.id} />
      </MemoryRouter>,
    );
    type(textarea(), "[[zzzz");

    expect(await screen.findByText("No documents match")).toBeInTheDocument();
    expect(screen.queryByRole("option")).toBeNull();
  });
});
