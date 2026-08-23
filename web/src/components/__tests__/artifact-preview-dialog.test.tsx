import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

import { api } from "@/lib/api";
import { ArtifactPreviewDialog } from "@/components/artifact-preview-dialog";
import type { Artifact } from "@/types";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

function artifact(over: Partial<Artifact> = {}): Artifact {
  return {
    id: "a1",
    task_id: "t1",
    name: "audit.md",
    artifact_type: "report",
    mime_type: "text/markdown; charset=utf-8",
    storage_key: "k",
    storage_url: "",
    size_bytes: 128,
    checksum_sha256: "",
    metadata: {},
    uploaded_by: "u1",
    uploaded_by_type: "user",
    created_at: "2026-08-21T00:00:00Z",
    ...over,
  };
}

/**
 * The dialog renders through `createPortal` into document.body, so the
 * container returned by `render` is empty even when the viewer is on screen.
 * Query the document, not the container — a container query here reads as a
 * clean "nothing rendered" for a viewer that rendered perfectly.
 */
function q(selector: string) {
  return document.body.querySelector(selector);
}

function qa(selector: string) {
  return document.body.querySelectorAll(selector);
}

function show(a: Artifact | null, onDownload = vi.fn()) {
  return render(
    <MemoryRouter>
      <ArtifactPreviewDialog artifact={a} onClose={vi.fn()} onDownload={onDownload} />
    </MemoryRouter>,
  );
}

/** Stand in for the S3 fetch. Returns whatever `body` says, with `ok` derived. */
function stubStorage(body: string, status = 200) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    text: () => Promise.resolve(body),
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  mockedApi.mockReset();
  mockedApi.mockResolvedValue({ url: "https://mesh.entire.host/s3/x?X-Amz-Expires=3600" });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ArtifactPreviewDialog", () => {
  it("renders nothing when no artifact is selected", () => {
    const { container } = show(null);
    expect(container).toBeEmptyDOMElement();
  });

  /**
   * AC1 — a `.md` artifact is readable INSIDE the app, formatted.
   *
   * The assertion is on structure, not on the presence of the words: a raw-text
   * fallback would also contain "Findings", and would pass a text-only check
   * while being exactly the bug. `<h1>` and `<table>` exist only if something
   * actually parsed the markdown.
   */
  it("renders markdown as formatted structure, not as source", async () => {
    stubStorage("# Findings\n\n| col | val |\n| --- | --- |\n| a | 1 |\n");
    show(artifact());

    await waitFor(() => {
      expect(q("h1")).not.toBeNull();
    });
    expect(q("h1")?.textContent).toBe("Findings");
    expect(q("table")).not.toBeNull();
    expect(qa("table td").length).toBeGreaterThan(0);
    // And the source markers are gone — a `<pre>` dump would still contain them.
    expect(document.body.textContent).not.toContain("| --- |");
  });

  /**
   * The artifact is text of external origin. Raw HTML inside it must arrive as
   * characters on the page, never as markup — asserted on the rendered tree,
   * because the bytes legitimately still contain the payload.
   */
  it("keeps raw HTML inside the file inert", async () => {
    stubStorage('# Doc\n\n<img src=x onerror="alert(1)">\n<script>alert(2)</script>\n');
    show(artifact());

    await waitFor(() => expect(q("h1")).not.toBeNull());
    expect(q("script")).toBeNull();
    expect(q("img")).toBeNull();
  });

  /** A non-markdown text file is shown verbatim rather than parsed. */
  it("shows plain text in a pre block, without parsing it as markdown", async () => {
    stubStorage("# not a heading, this is a log line\nsecond line");
    show(artifact({ name: "run.log", mime_type: "text/plain" }));

    await waitFor(() => expect(q("pre")).not.toBeNull());
    expect(q("h1")).toBeNull();
    expect(q("pre")?.textContent).toContain("# not a heading");
  });

  /**
   * AC4 — the presigned URL lives one hour, so an expired link must produce an
   * explanation and a way forward, never a blank panel.
   */
  it("explains an expired or failed link instead of showing an empty page", async () => {
    stubStorage("", 403);
    show(artifact());

    await waitFor(() => {
      expect(screen.getByText("Could not load preview")).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
    expect(q("pre")).toBeNull();
  });

  /**
   * The other half of AC4, and the one a static check would miss: Retry must
   * mint a FRESH url. Retrying against the same expired URL would fail forever
   * while looking like a working button.
   */
  it("requests a new presigned URL on every retry", async () => {
    stubStorage("", 403);
    show(artifact());

    await waitFor(() => expect(mockedApi).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: /try again/i }));
    await waitFor(() => expect(mockedApi).toHaveBeenCalledTimes(2));

    expect(mockedApi.mock.calls[1]?.[0]).toContain("/download?disposition=inline");
  });

  /** A cut file says how much it withheld and offers the whole thing. */
  it("announces truncation rather than silently showing a prefix", async () => {
    stubStorage("x".repeat(250_000));
    show(artifact({ name: "big.csv", mime_type: "text/csv", size_bytes: 250_000 }));

    await waitFor(() => {
      expect(screen.getByText(/more characters are\s+not shown/i)).toBeInTheDocument();
    });
    expect(screen.getByText(/50,000/)).toBeInTheDocument();
  });

  it("offers Download, and hands back the artifact it is showing", async () => {
    const onDownload = vi.fn();
    stubStorage("# Doc");
    show(artifact(), onDownload);

    await waitFor(() => expect(screen.getAllByText("Download").length).toBeGreaterThan(0));
    fireEvent.click(screen.getAllByText("Download")[0]!);
    expect(onDownload).toHaveBeenCalledWith("a1", "audit.md");
  });
});
