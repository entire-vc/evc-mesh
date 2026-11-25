/**
 * A refused comment POST (e.g. 422 mention_handoff_required) must be visible
 * under the form and must not clear the draft. Only the network is mocked; the
 * rich editor is replaced by a textarea because tiptap needs a real layout.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", async () => ({
  ...(await vi.importActual<typeof import("@/lib/api")>("@/lib/api")),
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));
vi.mock("@/components/rich-text-editor", () => ({
  RichTextEditor: ({ value, onChange }: { value: string; onChange: (v: string) => void }) => (
    <textarea aria-label="editor" value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));

import { api, ApiRequestError } from "@/lib/api";
import { CommentList } from "@/components/comment-list";

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

describe("CommentList submit failure", () => {
  beforeEach(() => {
    mockedApi.mockReset();
    mockedApi.mockImplementation((_path: string, init?: { method?: string }) => {
      if (init?.method === "POST") {
        return Promise.reject(
          new ApiRequestError("mention needs a handoff", "mention_handoff_required", 422, undefined, "mention_handoff_required"),
        );
      }
      return Promise.resolve({ items: [], has_more: false, next_cursor: null });
    });
  });

  it("shows the translated server error and keeps the draft on 422", async () => {
    render(<CommentList taskId="t1" projId="p1" />);
    const editor = await screen.findByLabelText("editor");
    fireEvent.change(editor, { target: { value: "@robin проверь?" } });
    const form = editor.closest("form")!;
    fireEvent.submit(form);

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(alert.textContent).toContain("A mention does not wake an agent"));
    expect((screen.getByLabelText("editor") as HTMLTextAreaElement).value).toBe("@robin проверь?");
  });

  it("drops the error when the form is reused for another task", async () => {
    const { rerender } = render(<CommentList taskId="t1" projId="p1" />);
    const editor = await screen.findByLabelText("editor");
    fireEvent.change(editor, { target: { value: "@robin проверь?" } });
    fireEvent.submit(editor.closest("form")!);
    await screen.findByRole("alert");

    rerender(<CommentList taskId="t2" projId="p1" />);
    // The form is hidden while the new task loads; judge it once it is back.
    await screen.findByLabelText("editor");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("ignores a POST failure that arrives after the task changed", async () => {
    let rejectPost: (e: Error) => void = () => {};
    mockedApi.mockImplementation((_path: string, init?: { method?: string }) => {
      if (init?.method === "POST") return new Promise((_, reject) => { rejectPost = reject; });
      return Promise.resolve({ items: [], has_more: false, next_cursor: null });
    });
    const { rerender } = render(<CommentList taskId="t1" projId="p1" />);
    const editor = await screen.findByLabelText("editor");
    fireEvent.change(editor, { target: { value: "@robin проверь?" } });
    fireEvent.submit(editor.closest("form")!);

    rerender(<CommentList taskId="t2" projId="p1" />);
    await screen.findByLabelText("editor");
    rejectPost(new ApiRequestError("late", "mention_handoff_required", 422, undefined, "mention_handoff_required"));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
