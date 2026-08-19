import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));

const trEnabled = { value: false };
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: trEnabled.value }),
}));

vi.mock("@/lib/docs/linkable-documents", () => ({
  fetchLinkableDocuments: vi.fn(async () => [{ id: "doc-1", title: "Deploy runbook" }]),
  forgetLinkableDocuments: vi.fn(),
}));

const searchDocuments = vi.fn();
const searchRelayDocuments = vi.fn();
vi.mock("@/lib/docs/document-search", async () => {
  const actual = await vi.importActual<typeof import("@/lib/docs/document-search")>(
    "@/lib/docs/document-search",
  );
  return {
    ...actual,
    searchDocuments: (...args: [string, string, number?]) => searchDocuments(...args),
    searchRelayDocuments: (...args: [string, string, number?]) => searchRelayDocuments(...args),
  };
});

import { DescriptionEditor } from "@/components/description-editor";
import { MATCH_END, MATCH_START } from "@/lib/docs/document-search";
import { useProjectStore } from "@/stores/project";
import { useWorkspaceStore } from "@/stores/workspace";
import type { Project, Workspace } from "@/types";

/**
 * Searching document CONTENT, and choosing where to search.
 *
 * D8 matched titles out of a list already in memory. The criterion here is the
 * one that list cannot meet: a writer who remembers a phrase from inside a
 * document, not its name. So the assertions are that the query reaches the
 * server, that what comes back is shown as the reason the document matched, and
 * that the scope control only exists when there is a second scope to choose.
 */

const PROJECT = { id: "proj-1", workspace_id: "ws-1", name: "Demo", slug: "demo" } as unknown as Project;
const WORKSPACE = { id: "ws-1", name: "Acme", slug: "acme" } as unknown as Workspace;

function mount() {
  let value = "";
  const onChange = vi.fn((next: string) => {
    value = next;
    view.rerender(ui());
  });
  const ui = () => (
    <MemoryRouter>
      <DescriptionEditor value={value} onChange={onChange} projId={PROJECT.id} />
    </MemoryRouter>
  );
  const view = render(ui());
  return { current: () => value };
}

function type(text: string) {
  const el = screen.getByRole("textbox") as HTMLTextAreaElement;
  fireEvent.change(el, { target: { value: text, selectionStart: text.length } });
  return el;
}

beforeEach(() => {
  trEnabled.value = false;
  searchDocuments.mockReset();
  searchRelayDocuments.mockReset();
  searchDocuments.mockResolvedValue([]);
  searchRelayDocuments.mockResolvedValue([]);
  useProjectStore.setState({ projects: [PROJECT], currentProject: PROJECT });
  useWorkspaceStore.setState({ currentWorkspace: WORKSPACE });
});

describe("searching document content", () => {
  it("asks the server once the writer has typed a query", async () => {
    mount();
    type("See [[rollback");

    await waitFor(() => {
      expect(searchDocuments).toHaveBeenCalledWith(PROJECT.id, "rollback");
    });
  });

  it("does not ask on a bare [[ — there is nothing to search for yet", async () => {
    mount();
    type("See [[");

    // The local title list answers this instantly; a rejected empty-query
    // request behind every keystroke would be pure noise.
    await screen.findByRole("option", { name: /Deploy runbook/ });
    expect(searchDocuments).not.toHaveBeenCalled();
  });

  it("shows the matched fragment, with the matched words marked", async () => {
    searchDocuments.mockResolvedValue([
      {
        id: "doc-9",
        title: "Onboarding",
        snippet: `read the ${MATCH_START}rollback${MATCH_END} procedure`,
        snippetIsMatch: true,
      },
    ]);
    const { container } = render(
      <MemoryRouter>
        <DescriptionEditor value="" onChange={vi.fn()} projId={PROJECT.id} />
      </MemoryRouter>,
    );
    fireEvent.change(container.querySelector("textarea")!, {
      target: { value: "[[rollback", selectionStart: 10 },
    });

    // The document whose TITLE does not contain the word — the whole point.
    await screen.findByRole("option", { name: /Onboarding/ });
    const marked = await waitFor(() => {
      const el = container.querySelector("mark");
      if (!el) throw new Error("no marked run");
      return el;
    });
    expect(marked.textContent).toBe("rollback");
    expect(container.querySelector('[data-snippet="match"]')).not.toBeNull();
    expect(container.querySelector('[data-snippet="preview"]')).toBeNull();
    // And the marker itself never reaches the page — it renders as a tofu box.
    expect(container.textContent).not.toContain(MATCH_START);
  });

  it("does NOT mark a snippet that is only the document's opening", async () => {
    // The server says so explicitly. Highlighting it would present the first
    // sentence of a long document as the reason it matched.
    searchDocuments.mockResolvedValue([
      { id: "doc-9", title: "Long doc", snippet: "the opening sentence", snippetIsMatch: false },
    ]);
    const { container } = render(
      <MemoryRouter>
        <DescriptionEditor value="" onChange={vi.fn()} projId={PROJECT.id} />
      </MemoryRouter>,
    );
    fireEvent.change(container.querySelector("textarea")!, {
      target: { value: "[[deep", selectionStart: 6 },
    });

    await screen.findByRole("option", { name: /Long doc/ });
    expect(container.querySelector("mark")).toBeNull();
    // Not just "nothing is marked" — that is also true of a matched snippet the
    // server sent with no markers, so it cannot tell the two apart. The slot has
    // to SAY which of the two it is. (Caught by mutation: dropping the
    // snippetIsMatch branch entirely left every assertion here green.)
    expect(container.querySelector('[data-snippet="preview"]')).not.toBeNull();
    expect(container.querySelector('[data-snippet="match"]')).toBeNull();
  });

  it("keeps the title matches when the search fails", async () => {
    // Degraded beats blank: an empty menu reads as "there is no such document".
    searchDocuments.mockRejectedValue(new Error("boom"));
    mount();
    type("See [[run");

    await waitFor(() => expect(searchDocuments).toHaveBeenCalled());
    expect(await screen.findByRole("option", { name: /Deploy runbook/ })).toBeInTheDocument();
  });
});

describe("the scope switcher", () => {
  it("is absent when the project has no Team Relay", async () => {
    mount();
    type("See [[");
    await screen.findByRole("option", { name: /Deploy runbook/ });

    // A switcher with one option is a control that cannot do anything, and it
    // teaches the reader that the row is decoration.
    expect(screen.queryByRole("radiogroup")).toBeNull();
  });

  it("appears when the project has one", async () => {
    trEnabled.value = true;
    mount();
    type("See [[");
    await screen.findByRole("option", { name: /Deploy runbook/ });

    expect(screen.getByRole("radiogroup", { name: /search in/i })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Docs" })).toBeChecked();
  });

  it("searches Team Relay after switching, and not before", async () => {
    trEnabled.value = true;
    searchRelayDocuments.mockResolvedValue([
      { id: "relay://vault/note", title: "Vault note", snippet: "vault/note", snippetIsMatch: false, relayUrl: "relay://vault/note" },
    ]);
    mount();
    type("See [[note");
    await waitFor(() => expect(searchDocuments).toHaveBeenCalled());
    expect(searchRelayDocuments).not.toHaveBeenCalled();

    fireEvent.mouseDown(screen.getByRole("radio", { name: "Team Relay" }));

    await waitFor(() => {
      expect(searchRelayDocuments).toHaveBeenCalledWith(PROJECT.id, "note");
    });
    expect(await screen.findByRole("option", { name: /Vault note/ })).toBeInTheDocument();
  });

  it("inserts a Team Relay hit as its relay URL, not as a route we do not serve", async () => {
    trEnabled.value = true;
    searchRelayDocuments.mockResolvedValue([
      { id: "relay://vault/note", title: "Vault note", snippet: "vault/note", snippetIsMatch: false, relayUrl: "relay://vault/note" },
    ]);
    const { current } = mount();
    type("See [[note");
    fireEvent.mouseDown(await screen.findByRole("radio", { name: "Team Relay" }));
    fireEvent.mouseDown(await screen.findByRole("option", { name: /Vault note/ }));

    await waitFor(() => {
      // A Team Relay document has no page in this app — that is why the
      // pseudo-scheme exists. Linking it to /w/…/docs/… would be a link to
      // nothing.
      expect(current()).toBe("See [Vault note](relay://vault/note) ");
    });
  });
});

describe("the two sources are merged, not swapped", () => {
  it("keeps a title match the server cannot find", async () => {
    // The two matchers answer different questions. The local list matches
    // SUBSTRINGS, so "run" finds "Deploy runbook". The server matches whole
    // tokens over a 'simple' index, so "run" does NOT find "runbook" there.
    //
    // An earlier version rendered `hits ?? local`, so the moment the server
    // answered with nothing the title matches vanished and typing the first few
    // letters of a document's name stopped finding it. It passed locally and
    // failed on CI, because whether the assertion ran before or after the
    // debounce decided which list was on screen — the bug and the flake were the
    // same bug.
    searchDocuments.mockResolvedValue([]);
    mount();
    type("See [[run");

    await waitFor(() => expect(searchDocuments).toHaveBeenCalled());
    expect(await screen.findByRole("option", { name: /Deploy runbook/ })).toBeInTheDocument();
  });

  it("puts server hits first and does not repeat a document in both lists", async () => {
    searchDocuments.mockResolvedValue([
      { id: "doc-9", title: "Onboarding", snippet: "…", snippetIsMatch: true },
      // The same document the local title list will also match.
      { id: "doc-1", title: "Deploy runbook", snippet: "…", snippetIsMatch: true },
    ]);
    mount();
    type("See [[run");

    await waitFor(() => expect(searchDocuments).toHaveBeenCalled());
    const options = await screen.findAllByRole("option");
    expect(options.map((o) => o.textContent)).toHaveLength(2);
    expect(options[0]!.textContent).toContain("Onboarding");
  });
});
