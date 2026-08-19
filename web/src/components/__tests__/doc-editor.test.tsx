import { describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { DocEditor } from "@/components/doc-editor";

// MarkdownRenderer resolves artifact links through useNavigate, so the
// read-only half of the boundary needs a router around it.
function renderInRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

describe("DocEditor", () => {
  it("renders the markdown when read-only, with nothing to type into", () => {
    renderInRouter(<DocEditor value={"# Title\n\nBody text."} onChange={vi.fn()} readOnly />);

    expect(screen.getByText("Title")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("says so when a read-only document has no body", () => {
    renderInRouter(<DocEditor value="   " onChange={vi.fn()} readOnly />);

    expect(screen.getByText("This page is empty.")).toBeInTheDocument();
  });

  it("edits through a single onChange, and does not offer task attachments", () => {
    const onChange = vi.fn();
    renderInRouter(<DocEditor value="hello" onChange={onChange} />);

    const textbox = screen.getByRole("textbox");
    expect(textbox).toHaveValue("hello");

    fireEvent.change(textbox, { target: { value: "hello there" } });
    expect(onChange).toHaveBeenCalledWith("hello there");

    // The task-editor footer ("uploads after task is saved") makes no sense on
    // a document — the boundary replaces it, and the attachment buttons that
    // would need a task id are gone rather than dead.
    expect(screen.getByText("Markdown supported")).toBeInTheDocument();
    expect(screen.queryByText(/after task is saved/)).not.toBeInTheDocument();
    expect(screen.queryByTitle(/attach file/i)).not.toBeInTheDocument();
    expect(screen.queryByTitle(/insert image/i)).not.toBeInTheDocument();
  });
});
