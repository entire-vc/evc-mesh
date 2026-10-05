import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ArtifactList } from "@/components/artifact-list";
import type { Artifact } from "@/types";

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
}));
import { api } from "@/lib/api";

function makeArtifact(overrides: Partial<Artifact>): Artifact {
  return {
    id: "artifact-1",
    task_id: "task-1",
    name: "screenshot.png",
    artifact_type: "image",
    mime_type: "image/png",
    storage_key: "ws/task-1/artifact-1/screenshot.png",
    storage_url: "",
    size_bytes: 1024,
    checksum_sha256: "",
    metadata: {},
    uploaded_by: "agent-1",
    uploaded_by_type: "agent",
    created_at: new Date().toISOString(),
    ...overrides,
  };
}

function stubList(artifact: Artifact) {
  vi.mocked(api).mockImplementation((path: string) => {
    if (path === "/api/v1/tasks/task-1/artifacts") {
      return Promise.resolve({ items: [artifact], total: 1 });
    }
    return Promise.reject(new Error(`unexpected path: ${path}`));
  });
}

describe("ArtifactList — Open in new tab", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.spyOn(window, "open").mockImplementation(() => null);
  });

  it("opens the artifact's own /a/<id> page, not a presigned URL", async () => {
    stubList(makeArtifact({}));

    render(<ArtifactList taskId="task-1" />);

    fireEvent.click(await screen.findByTitle("Open in new tab"));

    expect(window.open).toHaveBeenCalledWith("/a/artifact-1", "_blank");
    // The old behaviour minted a presigned URL first; that call is gone.
    expect(vi.mocked(api)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api).mock.calls[0]?.[0]).toBe("/api/v1/tasks/task-1/artifacts");
  });

  /**
   * The reported bug (#eb6fde4e), pinned: an artifact carrying
   * `metadata.tr_public_url` must open ITS OWN page in Mesh. The old code
   * window.open'd the Team Relay URL — which answers 401 on a private share
   * — before any other consideration. Asserting what window.open was NOT
   * given matters as much as what it was: a test that only checks the /a/
   * call would still pass if the TR branch ran first and this one second.
   */
  it("never window.opens metadata.tr_public_url, whatever the mime type", async () => {
    stubList(
      makeArtifact({
        name: "doc.docx",
        mime_type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        metadata: { tr_public_url: "https://docs.entire.vc/contenthub/t1/report" },
      }),
    );

    render(<ArtifactList taskId="task-1" />);

    fireEvent.click(await screen.findByTitle("Open in new tab"));

    expect(window.open).toHaveBeenCalledWith("/a/artifact-1", "_blank");
    expect(window.open).not.toHaveBeenCalledWith(
      "https://docs.entire.vc/contenthub/t1/report",
      expect.anything(),
    );
    expect(vi.mocked(api).mock.calls.map((c) => c[0])).not.toContain(
      expect.stringContaining("/download"),
    );
  });

  it("offers a tab for a browser-unrenderable file too — the page handles it", async () => {
    stubList(
      makeArtifact({
        name: "archive.zip",
        artifact_type: "file",
        mime_type: "application/zip",
      }),
    );

    render(<ArtifactList taskId="task-1" />);

    await screen.findByText("archive.zip");
    fireEvent.click(screen.getByTitle("Open in new tab"));
    expect(window.open).toHaveBeenCalledWith("/a/artifact-1", "_blank");
    expect(screen.getByTitle("Download")).toBeInTheDocument();
  });

  /**
   * A `.md` artifact keeps the Preview affordance (an eye, not a tab) — and
   * the preview now IS the /a/ page in a new tab. Both halves asserted: a
   * row showing both buttons would pass an "eye exists" check while keeping
   * the old tab affordance beside the new one.
   */
  it("offers Preview and not a browser tab for a markdown artifact, opening /a/<id>", async () => {
    stubList(
      makeArtifact({
        name: "audit.md",
        artifact_type: "report",
        mime_type: "text/markdown; charset=utf-8",
      }),
    );

    render(<ArtifactList taskId="task-1" />);

    fireEvent.click(await screen.findByTitle("Preview"));
    expect(window.open).toHaveBeenCalledWith("/a/artifact-1", "_blank");
    expect(screen.queryByTitle("Open in new tab")).not.toBeInTheDocument();
    expect(screen.getByTitle("Download")).toBeInTheDocument();
  });
});
