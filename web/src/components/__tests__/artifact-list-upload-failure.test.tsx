import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

vi.mock("@/components/markdown-editor", () => ({
  uploadArtifact: vi.fn(),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn(async () => ({ items: [], total: 0 })) };
});

import { ArtifactList } from "@/components/artifact-list";
import { uploadArtifact } from "@/components/markdown-editor";
import { toast } from "@/components/ui/toast";

// The list renders a loading skeleton until its first fetch settles; the file
// input only exists after that.
async function pickerOf(container: HTMLElement): Promise<HTMLInputElement> {
  return await waitFor(() => {
    const el = container.querySelector<HTMLInputElement>('input[type="file"]');
    if (!el) throw new Error("file picker missing");
    return el;
  });
}

describe("ArtifactList upload failures", () => {
  beforeEach(() => {
    vi.mocked(toast.error).mockReset();
    vi.mocked(uploadArtifact).mockReset();
  });

  // This path swallowed failures completely — `catch { /* error toast could go
  // here */ }` — so a refused attachment on the task's file list produced no
  // toast, no console message, and no change on screen. Indistinguishable from
  // the drop target being dead.
  it("tells the user when an attachment is refused", async () => {
    vi.mocked(uploadArtifact).mockRejectedValue(new Error("insufficient permissions"));

    const { container } = render(
      <MemoryRouter>
        <ArtifactList taskId="task-1" />
      </MemoryRouter>,
    );

    const picker = await pickerOf(container);
    fireEvent.change(picker, {
      target: { files: [new File(["x"], "notes.pdf", { type: "application/pdf" })] },
    });

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalled();
    });
    expect(String(vi.mocked(toast.error).mock.calls[0]?.[0])).toMatch(/notes\.pdf/);
  });

  // One bad file in a multi-file drop must not abandon the others.
  it("keeps going through the rest of a drop when one file is refused", async () => {
    vi.mocked(uploadArtifact)
      .mockRejectedValueOnce(new Error("too large"))
      .mockResolvedValueOnce({
        id: "art-2",
        task_id: "task-1",
        name: "ok.png",
        artifact_type: "image",
        mime_type: "image/png",
        size_bytes: 1,
        created_at: "2026-08-20T00:00:00Z",
      } as never);

    const { container } = render(
      <MemoryRouter>
        <ArtifactList taskId="task-1" />
      </MemoryRouter>,
    );

    const picker = await pickerOf(container);
    fireEvent.change(picker, {
      target: {
        files: [
          new File(["x"], "big.zip", { type: "application/zip" }),
          new File(["y"], "ok.png", { type: "image/png" }),
        ],
      },
    });

    await waitFor(() => {
      expect(vi.mocked(uploadArtifact)).toHaveBeenCalledTimes(2);
    });
    expect(vi.mocked(toast.error)).toHaveBeenCalledTimes(1);
  });
});
