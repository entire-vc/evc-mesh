import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
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

describe("ArtifactList — Open in new tab", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.spyOn(window, "open").mockImplementation(() => null);
  });

  it("requests the inline-disposition download URL when clicked", async () => {
    const artifact = makeArtifact({});
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-1/artifacts") {
        return Promise.resolve({ items: [artifact], total: 1 });
      }
      if (path === "/api/v1/artifacts/artifact-1/download?disposition=inline") {
        return Promise.resolve({ url: "https://s3.example.com/presigned-inline" });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    render(<ArtifactList taskId="task-1" />);

    const openButton = await screen.findByTitle("Open in new tab");
    fireEvent.click(openButton);

    await waitFor(() => {
      expect(window.open).toHaveBeenCalledWith(
        "https://s3.example.com/presigned-inline",
        "_blank",
      );
    });
  });

  it("hides the button for mime types the browser can't render inline", async () => {
    const artifact = makeArtifact({
      name: "archive.zip",
      artifact_type: "file",
      mime_type: "application/zip",
    });
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-1/artifacts") {
        return Promise.resolve({ items: [artifact], total: 1 });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    render(<ArtifactList taskId="task-1" />);

    await screen.findByText("archive.zip");
    expect(screen.queryByTitle("Open in new tab")).not.toBeInTheDocument();
    expect(screen.getByTitle("Download")).toBeInTheDocument();
  });

  /**
   * The reported bug, at the level the reader meets it: a `.md` artifact must
   * offer an in-app preview, and must NOT offer the browser tab that shows it
   * as raw source. Both halves are asserted — "Preview exists" alone would pass
   * on a row that shows both buttons, which is still the old behaviour plus a
   * new one beside it.
   */
  it("offers Preview and not a browser tab for a markdown artifact", async () => {
    const artifact = makeArtifact({
      name: "audit.md",
      artifact_type: "report",
      mime_type: "text/markdown; charset=utf-8",
    });
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-1/artifacts") {
        return Promise.resolve({ items: [artifact], total: 1 });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    render(<ArtifactList taskId="task-1" />);

    expect(await screen.findByTitle("Preview")).toBeInTheDocument();
    expect(screen.queryByTitle("Open in new tab")).not.toBeInTheDocument();
    expect(screen.getByTitle("Download")).toBeInTheDocument();
  });

  it("still shows the button for a TR-linked artifact regardless of mime type", async () => {
    const artifact = makeArtifact({
      name: "doc.docx",
      mime_type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
      metadata: { tr_public_url: "https://relay.example.com/doc" },
    });
    vi.mocked(api).mockImplementation((path: string) => {
      if (path === "/api/v1/tasks/task-1/artifacts") {
        return Promise.resolve({ items: [artifact], total: 1 });
      }
      return Promise.reject(new Error(`unexpected path: ${path}`));
    });

    render(<ArtifactList taskId="task-1" />);

    expect(await screen.findByTitle("Open in new tab")).toBeInTheDocument();
  });
});
