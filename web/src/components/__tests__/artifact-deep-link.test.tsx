import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { ArtifactList } from "@/components/artifact-list";
import { api } from "@/lib/api";
import type { Artifact } from "@/types";

vi.mock("@/lib/api", () => ({ api: vi.fn() }));
const artifact = { id: "artifact-2", task_id: "task-1", name: "proof.png",
  artifact_type: "image", mime_type: "image/png", metadata: {},
  size_bytes: 100, created_at: "2026-10-03T00:00:00Z" } as Artifact;
const scroll = vi.fn();

describe("artifact address", () => {
  beforeEach(() => {
    vi.mocked(api).mockReset();
    scroll.mockReset();
    HTMLElement.prototype.scrollIntoView = scroll;
  });
  it("loads a target beyond the first page, highlights it and offers its address", async () => {
    vi.mocked(api).mockImplementation(async (_path, options) => options?.params?.page === 2
      ? { items: [artifact], has_more: false } : { items: [], has_more: true });
    const { container } = render(<ArtifactList taskId="task-1" focusArtifactId="artifact-2" />);
    await screen.findByText("proof.png");
    const row = container.querySelector('[data-artifact-id="artifact-2"]');
    expect(row).toHaveAttribute("data-focused", "true");
    await waitFor(() => expect(scroll).toHaveBeenCalled());
    expect(screen.getByRole("link", { name: "Link to artifact" }))
      .toHaveAttribute("href", "/a/artifact-2");
  });
  it("waits for loading to finish before saying a target was not found", async () => {
    let complete!: (data: unknown) => void;
    vi.mocked(api).mockReturnValue(new Promise(resolve => { complete = resolve; }));
    render(<ArtifactList taskId="task-1" focusArtifactId="missing" />);
    expect(screen.queryByText(/Artifact not found/)).not.toBeInTheDocument();
    complete({ items: [], has_more: false });
    expect(await screen.findByText(/Artifact not found/)).toBeInTheDocument();
  });
  it("does not call a transport error not found", async () => {
    vi.mocked(api).mockRejectedValue(new Error("unavailable"));
    render(<ArtifactList taskId="task-1" focusArtifactId="artifact-2" />);
    expect(await screen.findByText(/Could not load artifacts/)).toBeInTheDocument();
    expect(screen.queryByText(/Artifact not found/)).not.toBeInTheDocument();
  });
  it("ignores a previous task response after navigation", async () => {
    let old!: (data: unknown) => void;
    vi.mocked(api).mockImplementation(path => path.includes("task-old")
      ? new Promise(resolve => { old = resolve; }) : Promise.resolve({ items: [artifact] }));
    const { rerender } = render(<ArtifactList taskId="task-old" focusArtifactId="artifact-2" />);
    rerender(<ArtifactList taskId="task-1" focusArtifactId="artifact-2" />);
    await screen.findByText("proof.png");
    old({ items: [], has_more: false });
    await waitFor(() => expect(screen.getByText("proof.png")).toBeInTheDocument());
    expect(screen.queryByText(/Artifact not found/)).not.toBeInTheDocument();
  });
});
