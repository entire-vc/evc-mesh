import { beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: vi.fn(), getAccessToken: vi.fn(() => "tok") };
});

import { MarkdownEditor } from "@/components/markdown-editor";
import { toast } from "@/components/ui/toast";
import { api } from "@/lib/api";

function renderInRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

describe("MarkdownEditor task attachment failures", () => {
  beforeEach(() => {
    vi.mocked(toast.error).mockReset();
    vi.mocked(api).mockReset();
  });

  // The reported bug in the form the user meets it: a file is picked, the
  // server refuses it, and the only trace is an HTML comment spliced into the
  // description body. An HTML comment renders as nothing — so in Preview, and
  // in the task description once saved, the user sees no error at all. Worse,
  // the comment is now part of their text and gets persisted with it.
  it("tells the user when a picked file fails to upload, without writing into the body", async () => {
    vi.mocked(api).mockRejectedValue(new Error("insufficient permissions"));
    const onChange = vi.fn();
    const { container } = renderInRouter(
      <MarkdownEditor value="hello" onChange={onChange} taskId="task-1" />,
    );

    const picker = container.querySelector<HTMLInputElement>('input[type="file"]');
    if (!picker) throw new Error("file picker missing");
    fireEvent.change(picker, {
      target: { files: [new File(["x"], "pic.png", { type: "image/png" })] },
    });

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalled();
    });
    expect(String(vi.mocked(toast.error).mock.calls[0]?.[0])).toMatch(/pic\.png/);

    // The failure must leave the text as it was — no placeholder stranded, and
    // no `<!-- upload failed -->` smuggled into what the user will save.
    const calls = onChange.mock.calls;
    const finalValue = calls[calls.length - 1]?.[0] as string | undefined;
    expect(finalValue ?? "hello").not.toMatch(/Uploading/);
    expect(finalValue ?? "hello").not.toMatch(/<!--/);

    // And the "Uploading..." indicator must not be left behind.
    expect(screen.queryByText(/Uploading/i)).not.toBeInTheDocument();
  });
});
