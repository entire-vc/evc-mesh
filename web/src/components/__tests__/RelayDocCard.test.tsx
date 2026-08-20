import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

const mockedApi = vi.fn();
vi.mock("@/lib/api", () => ({
  api: (...args: unknown[]) => mockedApi(...(args as [string])),
  getAccessToken: vi.fn(() => null),
}));

// The editor has its own tests; here it stands in for "our renderer", so the
// assertion can be about WHICH renderer received the document.
vi.mock("@/components/doc-editor", () => ({
  DocEditor: ({ value, readOnly }: { value: string; readOnly?: boolean }) => (
    <div data-testid="doc-editor" data-readonly={String(!!readOnly)}>
      {value}
    </div>
  ),
}));

import { RelayDocCard } from "@/components/RelayDocCard";

/**
 * A Team Relay document opened in our own editor.
 *
 * What this replaced was an <iframe sandbox="allow-scripts allow-same-origin">
 * holding Team Relay's rendered page, fixed at 256px, with a six-second timeout
 * and a "Preview not available" dead end. So the assertions here are: the source
 * goes to OUR editor, no iframe is produced under any outcome, and every failure
 * still leaves the reader a working link.
 */

const DOC_URL = "relay://demo/Notes/Architecture.md";

function renderCard(props: { projId?: string; relayUrl?: string; label?: string } = {}) {
  return render(
    <MemoryRouter>
      <RelayDocCard relayUrl={props.relayUrl ?? DOC_URL} label={props.label} projId={props.projId} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mockedApi.mockReset();
  mockedApi.mockResolvedValue({ available: false });
});

describe("RelayDocCard", () => {
  it("renders the document with our editor, read-only", async () => {
    mockedApi.mockResolvedValue({
      available: true,
      name: "Architecture",
      path: "Notes/Architecture.md",
      content: "# Architecture\n\nThe migration runs before the swap.",
    });

    renderCard({ projId: "proj-1" });

    const editor = await screen.findByTestId("doc-editor");
    expect(editor).toHaveTextContent("The migration runs before the swap.");
    expect(editor.getAttribute("data-readonly")).toBe("true");
  });

  it("reads it through the server, never fetching Team Relay from the page", async () => {
    // The integration key is long-lived and does not rotate. The only reason
    // this is a server round-trip is that the key must never be in the browser.
    mockedApi.mockResolvedValue({ available: true, content: "# x" });

    renderCard({ projId: "proj-1" });

    await waitFor(() => expect(mockedApi).toHaveBeenCalled());
    const url = mockedApi.mock.calls[0]![0] as string;
    expect(url).toBe(
      `/api/v1/projects/proj-1/tr/document?relay_url=${encodeURIComponent(DOC_URL)}`,
    );
  });

  it("produces no iframe — under any outcome", async () => {
    // The negative control for the whole unit. Asserted on every branch,
    // because "we removed the component" is not the same claim as "nothing
    // renders an iframe any more".
    for (const response of [
      { available: true, content: "# body" },
      { available: false },
    ]) {
      mockedApi.mockResolvedValue(response);
      const { container, unmount } = renderCard({ projId: "proj-1" });
      await waitFor(() => expect(mockedApi).toHaveBeenCalled());
      expect(container.querySelector("iframe")).toBeNull();
      unmount();
      mockedApi.mockReset();
    }
  });

  it("still offers the link when the document cannot be read", async () => {
    mockedApi.mockResolvedValue({ available: false });

    renderCard({ projId: "proj-1" });

    const link = await screen.findByRole("link", { name: /open/i });
    expect(link).toHaveAttribute("href", "https://demo/Notes/Architecture.md");
    expect(screen.queryByTestId("doc-editor")).toBeNull();
  });

  it("still offers the link when the request itself fails", async () => {
    mockedApi.mockRejectedValue(new Error("boom"));

    renderCard({ projId: "proj-1" });

    expect(await screen.findByRole("link", { name: /open/i })).toBeInTheDocument();
  });

  it("does not call the server at all without a project", async () => {
    // No project means no integration to read through. Asking anyway would be a
    // request that can only fail.
    renderCard({});

    await screen.findByRole("link", { name: /open/i });
    expect(mockedApi).not.toHaveBeenCalled();
  });

  it("treats a folder link as a folder, and does not try to open it", async () => {
    renderCard({ projId: "proj-1", relayUrl: "relay://demo/Notes" });

    expect(await screen.findByRole("link", { name: /open folder/i })).toBeInTheDocument();
    expect(mockedApi).not.toHaveBeenCalled();
  });

  it("prefers the name the server resolved over the one guessed from the URL", async () => {
    mockedApi.mockResolvedValue({ available: true, name: "Real Title", content: "# x" });

    renderCard({ projId: "proj-1" });

    expect(await screen.findByText("Real Title")).toBeInTheDocument();
  });

  it("can be collapsed", async () => {
    mockedApi.mockResolvedValue({ available: true, content: "# body text" });
    const { container } = renderCard({ projId: "proj-1" });
    await screen.findByTestId("doc-editor");

    const toggle = screen.getByRole("button", { name: /collapse document/i });
    toggle.click();

    await waitFor(() => {
      expect(container.querySelector('[data-testid="doc-editor"]')).toBeNull();
    });
  });
});
