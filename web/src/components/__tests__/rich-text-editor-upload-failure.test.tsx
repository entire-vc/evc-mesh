import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

vi.mock("@/lib/task-artifacts", async () => {
  const actual = await vi.importActual<typeof import("@/lib/task-artifacts")>(
    "@/lib/task-artifacts",
  );
  return { ...actual, uploadArtifact: vi.fn() };
});

vi.mock("@/lib/api", () => ({
  api: vi.fn(async () => ({ url: "https://example.com/presigned" })),
  getAccessToken: vi.fn(() => null),
  getMentionables: vi.fn(async () => []),
}));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));

import { RichTextEditor } from "@/components/rich-text-editor";
import { toast } from "@/components/ui/toast";
import { uploadArtifact } from "@/lib/task-artifacts";

/**
 * Carried over from markdown-editor-upload-failure.test.tsx (PR #655) when the
 * textarea editor was deleted and this one took over task descriptions and
 * comments. The defect it pins is the reason that PR exists: a refused
 * attachment used to report itself by splicing `<!-- upload failed: ... -->`
 * into the body, which renders as nothing — so the user saw the placeholder
 * vanish, saw no error, and then saved the comment into their own text.
 *
 * The surface changed; the property did not. A failed upload must say so where
 * the user is looking, and must leave the document exactly as they wrote it.
 */
describe("RichTextEditor task attachment failures", () => {
  beforeEach(() => {
    vi.mocked(toast.error).mockReset();
    vi.mocked(uploadArtifact).mockReset();
  });

  it("names the refused file, and writes nothing into the body", async () => {
    vi.mocked(uploadArtifact).mockRejectedValue(new Error("insufficient permissions"));
    const onChange = vi.fn();
    const { container } = render(
      <MemoryRouter>
        <RichTextEditor value="hello" onChange={onChange} taskId="task-1" />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(container.querySelector(".mesh-doc-prose")).not.toBeNull();
    });

    // The last file input is the Attach-file one; the first is images-only.
    const pickers = container.querySelectorAll<HTMLInputElement>('input[type="file"]');
    const picker = pickers[pickers.length - 1];
    if (!picker) throw new Error("file picker missing");
    fireEvent.change(picker, {
      target: { files: [new File(["x"], "pic.png", { type: "image/png" })] },
    });

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalled();
    });
    // The file name, not just the server's sentence: a selection can hold
    // several files, and "insufficient permissions" alone names none of them.
    expect(String(vi.mocked(toast.error).mock.calls[0]?.[0])).toMatch(/pic\.png/);

    // Milkdown's listener debounces 200ms — asserting sooner would read "the
    // document was untouched" off a working editor and a dead one alike.
    await new Promise((resolve) => setTimeout(resolve, 320));

    // A failure must not edit the writer's text: no image node, no stranded
    // placeholder, and no HTML comment smuggled into what gets saved.
    for (const call of onChange.mock.calls) {
      const value = call[0] as string;
      expect(value).not.toMatch(/<!--/);
      expect(value).not.toMatch(/pic\.png/);
    }
    expect(screen.queryByText(/Uploading/i)).not.toBeInTheDocument();
  });

  it("inserts the attachment when the upload succeeds", async () => {
    // The positive control. Without it, the assertions above pass just as
    // happily on an editor whose Attach button is wired to nothing at all:
    // "no toast.error, nothing written" is exactly what a dead button does.
    vi.mocked(uploadArtifact).mockResolvedValue({
      id: "art-1",
      task_id: "task-1",
      name: "pic.png",
      artifact_type: "image",
      mime_type: "image/png",
      size_bytes: 3,
      created_at: "2026-08-20T00:00:00Z",
    } as Awaited<ReturnType<typeof uploadArtifact>>);
    const onChange = vi.fn();
    const { container } = render(
      <MemoryRouter>
        <RichTextEditor value="hello" onChange={onChange} taskId="task-1" />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(container.querySelector(".mesh-doc-prose")).not.toBeNull();
    });

    const pickers = container.querySelectorAll<HTMLInputElement>('input[type="file"]');
    const picker = pickers[pickers.length - 1];
    if (!picker) throw new Error("file picker missing");
    fireEvent.change(picker, {
      target: { files: [new File(["x"], "pic.png", { type: "image/png" })] },
    });

    await waitFor(() => {
      expect(vi.mocked(uploadArtifact)).toHaveBeenCalled();
    });
    await new Promise((resolve) => setTimeout(resolve, 320));

    expect(vi.mocked(toast.error)).not.toHaveBeenCalled();
    const written = onChange.mock.calls.map((c) => c[0] as string).join("\n");
    expect(written).toMatch(/art-1/);
  });
});
