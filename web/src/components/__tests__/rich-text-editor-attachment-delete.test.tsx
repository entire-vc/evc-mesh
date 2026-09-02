import { beforeAll, beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/**
 * jsdom implements no layout, and — unlike Element — its Range does not even
 * stub out getClientRects/getBoundingClientRect, so ProseMirror's
 * `view.coordsAtPos` (which attachment-controls.ts calls to place the delete
 * button) throws outright instead of returning a zero rect. A real browser
 * always has both; this is a jsdom gap, not a claim about production
 * geometry — the tests below only assert that a button exists, never where.
 */
beforeAll(() => {
  const zeroRect = {
    x: 0,
    y: 0,
    left: 0,
    right: 0,
    top: 0,
    bottom: 0,
    width: 0,
    height: 0,
    toJSON() {
      return this;
    },
  } as DOMRect;
  if (!Range.prototype.getBoundingClientRect) {
    Range.prototype.getBoundingClientRect = () => zeroRect;
  }
  if (!Range.prototype.getClientRects) {
    Range.prototype.getClientRects = () => [zeroRect] as unknown as DOMRectList;
  }
});

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

vi.mock("@/lib/task-artifacts", async () => {
  const actual = await vi.importActual<typeof import("@/lib/task-artifacts")>(
    "@/lib/task-artifacts",
  );
  return { ...actual, uploadArtifact: vi.fn() };
});

const apiCalls: Array<{ path: string; method?: string }> = [];
let deleteShouldFail = false;

vi.mock("@/lib/api", () => ({
  api: vi.fn(async (path: string, opts?: { method?: string }) => {
    apiCalls.push({ path, method: opts?.method });
    if (opts?.method === "DELETE") {
      if (deleteShouldFail) throw new Error("insufficient permissions");
      return {};
    }
    return { url: "https://example.com/presigned" };
  }),
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
 * The other half of PR #655's attachment work (see
 * rich-text-editor-upload-failure.test.tsx): removing an image or file link
 * from the text has always worked as plain ProseMirror editing, but nothing
 * deleted the task artifact underneath it — the bytes stayed uploaded, findable
 * only in the separate artifact panel. This pins the inline "×" that does both
 * at once.
 */
describe("RichTextEditor inline attachment delete", () => {
  beforeEach(() => {
    vi.mocked(toast.error).mockReset();
    vi.mocked(uploadArtifact).mockReset();
    apiCalls.length = 0;
    deleteShouldFail = false;
  });

  async function uploadImage(container: HTMLElement, onChange: ReturnType<typeof vi.fn>) {
    vi.mocked(uploadArtifact).mockResolvedValue({
      id: "art-1",
      task_id: "task-1",
      name: "pic.png",
      artifact_type: "image",
      mime_type: "image/png",
      size_bytes: 3,
      created_at: "2026-08-20T00:00:00Z",
    } as Awaited<ReturnType<typeof uploadArtifact>>);

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
    await waitFor(() => {
      expect(container.querySelector("img")).not.toBeNull();
    });
    // Milkdown's listener debounces 200ms.
    await new Promise((resolve) => setTimeout(resolve, 320));
    void onChange;
  }

  async function uploadFile(container: HTMLElement) {
    vi.mocked(uploadArtifact).mockResolvedValue({
      id: "art-2",
      task_id: "task-1",
      name: "notes.txt",
      artifact_type: "file",
      mime_type: "text/plain",
      size_bytes: 5,
      created_at: "2026-08-20T00:00:00Z",
    } as Awaited<ReturnType<typeof uploadArtifact>>);

    await waitFor(() => {
      expect(container.querySelector(".mesh-doc-prose")).not.toBeNull();
    });

    const pickers = container.querySelectorAll<HTMLInputElement>('input[type="file"]');
    const picker = pickers[pickers.length - 1];
    if (!picker) throw new Error("file picker missing");
    fireEvent.change(picker, {
      target: { files: [new File(["hello"], "notes.txt", { type: "text/plain" })] },
    });

    await waitFor(() => {
      expect(vi.mocked(uploadArtifact)).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(container.querySelector('a[href*="art-2"]')).not.toBeNull();
    });
    await new Promise((resolve) => setTimeout(resolve, 320));
  }

  it("shows a delete button over an uploaded non-image file link, and removes it on click", async () => {
    const onChange = vi.fn();
    const { container } = render(
      <MemoryRouter>
        <RichTextEditor value="hello" onChange={onChange} taskId="task-1" />
      </MemoryRouter>,
    );
    await uploadFile(container);

    const btn = await waitFor(() => {
      const el = container.querySelector<HTMLButtonElement>(".mesh-attachment-delete-btn");
      if (!el) throw new Error("delete button not rendered");
      return el;
    });
    fireEvent.click(btn);

    await waitFor(() => {
      expect(apiCalls.some((c) => c.path === "/api/v1/artifacts/art-2" && c.method === "DELETE")).toBe(
        true,
      );
    });
    await waitFor(() => {
      expect(container.querySelector('a[href*="art-2"]')).toBeNull();
    });
  });

  it("shows a delete button over an uploaded image", async () => {
    const onChange = vi.fn();
    const { container } = render(
      <MemoryRouter>
        <RichTextEditor value="hello" onChange={onChange} taskId="task-1" />
      </MemoryRouter>,
    );
    await uploadImage(container, onChange);

    await waitFor(() => {
      expect(container.querySelector(".mesh-attachment-delete-btn")).not.toBeNull();
    });
  });

  it("deletes the artifact and removes the reference on click", async () => {
    const onChange = vi.fn();
    const { container } = render(
      <MemoryRouter>
        <RichTextEditor value="hello" onChange={onChange} taskId="task-1" />
      </MemoryRouter>,
    );
    await uploadImage(container, onChange);

    const btn = await waitFor(() => {
      const el = container.querySelector<HTMLButtonElement>(".mesh-attachment-delete-btn");
      if (!el) throw new Error("delete button not rendered");
      return el;
    });
    fireEvent.click(btn);

    await waitFor(() => {
      expect(apiCalls.some((c) => c.path === "/api/v1/artifacts/art-1" && c.method === "DELETE")).toBe(
        true,
      );
    });
    await waitFor(() => {
      expect(container.querySelector("img")).toBeNull();
    });
    await waitFor(() => {
      expect(container.querySelector(".mesh-attachment-delete-btn")).toBeNull();
    });
  });

  it("leaves the document untouched when the delete request fails", async () => {
    deleteShouldFail = true;
    const onChange = vi.fn();
    const { container } = render(
      <MemoryRouter>
        <RichTextEditor value="hello" onChange={onChange} taskId="task-1" />
      </MemoryRouter>,
    );
    await uploadImage(container, onChange);

    const btn = await waitFor(() => {
      const el = container.querySelector<HTMLButtonElement>(".mesh-attachment-delete-btn");
      if (!el) throw new Error("delete button not rendered");
      return el;
    });
    fireEvent.click(btn);

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalled();
    });
    // A refused delete must not also silently drop the reference from the
    // document — the same principle as an upload failure writing nothing.
    expect(container.querySelector("img")).not.toBeNull();
  });
});
