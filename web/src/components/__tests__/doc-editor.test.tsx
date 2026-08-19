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

  it("edits through a single onChange, and never shows the task-editor footer", () => {
    const onChange = vi.fn();
    renderInRouter(<DocEditor value="hello" onChange={onChange} documentId="doc-1" />);

    const textbox = screen.getByRole("textbox");
    expect(textbox).toHaveValue("hello");

    fireEvent.change(textbox, { target: { value: "hello there" } });
    expect(onChange).toHaveBeenCalledWith("hello there");

    // "uploads after task is saved" makes no sense on a document, whatever the
    // upload affordances do — the boundary replaces that footer outright.
    expect(screen.queryByText(/after task is saved/)).not.toBeInTheDocument();
  });

  // The two halves of the same rule, and both are needed: asserting only the
  // absence would keep passing if the buttons were removed altogether, and
  // asserting only the presence would keep passing if they were shown on a
  // document that does not exist yet — where an upload has nothing to attach to.
  it("offers attachments once the document exists", () => {
    renderInRouter(<DocEditor value="hello" onChange={vi.fn()} documentId="doc-1" />);

    expect(screen.getByTitle(/insert image/i)).toBeInTheDocument();
    expect(screen.getByTitle(/attach file/i)).toBeInTheDocument();
  });

  it("drops the attachment affordances while there is no document to own the bytes", () => {
    renderInRouter(<DocEditor value="hello" onChange={vi.fn()} />);

    expect(screen.queryByTitle(/insert image/i)).not.toBeInTheDocument();
    expect(screen.queryByTitle(/attach file/i)).not.toBeInTheDocument();
  });
});
