import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { AttachDocDialog } from "@/components/AttachDocDialog";
import { MATCH_START, MATCH_END } from "@/lib/docs/document-search";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";

describe("AttachDocDialog", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
  });

  it("positive control: an existing document is found and selecting it fires onSelect with the hit, then closes", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/projects/proj-1/documents/search") {
        return Promise.resolve({
          items: [
            {
              id: "doc-1",
              title: "Runbook",
              snippet: "how to deploy",
              snippet_is_match: true,
            },
          ],
        });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    const onSelect = vi.fn();
    const onClose = vi.fn();
    render(
      <AttachDocDialog
        projId="proj-1"
        scope="docs"
        open
        onClose={onClose}
        onSelect={onSelect}
      />,
    );

    fireEvent.change(screen.getByPlaceholderText("Search documents..."), {
      target: { value: "runbook" },
    });

    const hit = await screen.findByText("Runbook");
    fireEvent.click(hit);

    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({ id: "doc-1", title: "Runbook" }),
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("renders a matched snippet as highlighted text, not the raw marker codepoints", async () => {
    const rawSnippet = `how to ${MATCH_START}deploy${MATCH_END} safely`;
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/projects/proj-1/documents/search") {
        return Promise.resolve({
          items: [
            { id: "doc-1", title: "Runbook", snippet: rawSnippet, snippet_is_match: true },
          ],
        });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    const { container } = render(
      <AttachDocDialog projId="proj-1" scope="docs" open onClose={vi.fn()} onSelect={vi.fn()} />,
    );
    fireEvent.change(screen.getByPlaceholderText("Search documents..."), {
      target: { value: "deploy" },
    });

    const mark = await screen.findByText("deploy");
    expect(mark.tagName).toBe("MARK");
    // The private-use marker codepoints themselves must never reach the DOM as text —
    // that is the bug a live browser screenshot caught (tofu glyphs where a highlight
    // should be), because a mocked "it returns JSON" test never renders real content.
    expect(container.textContent).not.toContain(MATCH_START);
    expect(container.textContent).not.toContain(MATCH_END);
  });

  it("docs scope does not search on an empty query — the server rejects q='' outright, so the dialog must not fire it on open", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      // Any call at all is a bug for this test: the empty-query guard must
      // stop it before a request is made.
      return Promise.reject(new Error(`unexpected call: ${path}`));
    });

    render(
      <AttachDocDialog projId="proj-1" scope="docs" open onClose={vi.fn()} onSelect={vi.fn()} />,
    );

    expect(await screen.findByText("Start typing to search")).toBeInTheDocument();
    expect(api).not.toHaveBeenCalled();
  });

  it("negative control: a query that matches nothing shows a visible empty state, not a silently broken picker", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/projects/proj-1/documents/search") {
        return Promise.resolve({ items: [] });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    const onSelect = vi.fn();
    render(
      <AttachDocDialog
        projId="proj-1"
        scope="docs"
        open
        onClose={vi.fn()}
        onSelect={onSelect}
      />,
    );

    fireEvent.change(screen.getByPlaceholderText("Search documents..."), {
      target: { value: "no-such-document" },
    });

    await waitFor(() => {
      expect(screen.getByText("Ничего не найдено")).toBeInTheDocument();
    });
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("negative control: a failed search shows a visible error rather than an empty-looking list", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/projects/proj-1/tr/search") {
        return Promise.reject(new Error("upstream unavailable"));
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    render(
      <AttachDocDialog
        projId="proj-1"
        scope="relay"
        open
        onClose={vi.fn()}
        onSelect={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText("upstream unavailable")).toBeInTheDocument();
    });
  });

  it("relay scope searches the Team Relay endpoint and labels the dialog for Obsidian", async () => {
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/projects/proj-1/tr/search") {
        return Promise.resolve({
          docs: [
            { title: "Meeting notes", path: "notes/meeting.md", relay_url: "relay://mesh/notes/meeting.md" },
          ],
        });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    const onSelect = vi.fn();
    render(
      <AttachDocDialog
        projId="proj-1"
        scope="relay"
        open
        onClose={vi.fn()}
        onSelect={onSelect}
      />,
    );

    expect(await screen.findByText("Attach Obsidian doc")).toBeInTheDocument();
    const hit = await screen.findByText("Meeting notes");
    fireEvent.click(hit);

    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({
        title: "Meeting notes",
        relayUrl: "relay://mesh/notes/meeting.md",
      }),
    );
  });
});
