import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { DocEditor } from "@/components/doc-editor";

// The editor resolves internal attachment links through the API helper and
// navigates internal links through the router.
vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

function renderInRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

/** The ProseMirror surface, once the editor has finished creating itself. */
async function surface(container: HTMLElement): Promise<HTMLElement> {
  return await waitFor(() => {
    const el = container.querySelector<HTMLElement>(".ProseMirror");
    if (!el) throw new Error("editor not mounted");
    return el;
  });
}

const RICH = `| Left | Right |
| :--- | ----: |
| a | b |

- [ ] todo
- [x] done

1. one
   1. one-a

- outer
  - inner
    - deepest

Cited[^n].

[^n]: The note.
`;

describe("DocEditor", () => {
  it("says so when a read-only document has no body", () => {
    renderInRouter(<DocEditor value="   " onChange={vi.fn()} readOnly />);

    expect(screen.getByText("This page is empty.")).toBeInTheDocument();
  });

  it("renders the document when read-only, with nothing to type into", async () => {
    const { container } = renderInRouter(
      <DocEditor value={"# Title\n\nBody text."} onChange={vi.fn()} readOnly />,
    );
    const el = await surface(container);

    expect(within(el).getByText("Title").tagName).toBe("H1");
    expect(within(el).getByText("Body text.")).toBeInTheDocument();
    expect(el.getAttribute("contenteditable")).toBe("false");
  });

  it("is editable, with a toolbar, when not read-only", async () => {
    const { container } = renderInRouter(
      <DocEditor value="hello" onChange={vi.fn()} documentId="doc-1" />,
    );
    const el = await surface(container);

    expect(el.getAttribute("contenteditable")).toBe("true");
    expect(screen.getByTitle("Bold (Ctrl+B)")).toBeInTheDocument();
    expect(screen.getByTitle("Table")).toBeInTheDocument();

    // "uploads after task is saved" makes no sense on a document — the boundary
    // replaces the task editor's footer outright.
    expect(screen.queryByText(/after task is saved/)).not.toBeInTheDocument();
  });

  // The reason this unit exists: the viewer and the editor were two separate
  // hand-written renderers, and they drifted. They are now one engine in two
  // modes, so the same source must produce the same structure in both — this
  // asserts it rather than trusting it.
  it.each([
    ["view", true],
    ["edit", false],
  ])("renders tables, tasks, nested lists and footnotes in %s mode", async (_label, readOnly) => {
    const { container } = renderInRouter(
      <DocEditor
        value={RICH}
        onChange={vi.fn()}
        readOnly={readOnly}
        documentId={readOnly ? undefined : "doc-1"}
      />,
    );
    const el = await surface(container);

    // Table, with the column alignment the old renderer could not express.
    // prosemirror-tables writes the cell's `alignment` attribute out as an
    // inline text-align, so that is where it has to be asserted.
    const headers = el.querySelectorAll<HTMLTableCellElement>("th");
    expect(headers).toHaveLength(2);
    expect(headers[0]?.style.textAlign).toBe("left");
    expect(headers[1]?.style.textAlign).toBe("right");

    // Task checkboxes, as an attribute rather than literal "[ ]" text.
    const tasks = el.querySelectorAll('li[data-item-type="task"]');
    expect(tasks).toHaveLength(2);
    expect(Array.from(tasks).map((t) => t.getAttribute("data-checked"))).toEqual([
      "false",
      "true",
    ]);
    expect(el.textContent).not.toContain("[ ]");

    // Nested ordered and unordered lists.
    expect(el.querySelectorAll("ol ol")).toHaveLength(1);
    expect(el.querySelectorAll("ul ul ul").length).toBeGreaterThanOrEqual(1);

    // Footnotes.
    expect(el.querySelector('[data-type="footnote_definition"]')).not.toBeNull();
    expect(el.querySelector('[data-type="footnote_reference"]')).not.toBeNull();
    expect(el.textContent).toContain("The note.");
  });

  // Two halves of the same rule, and both are needed: asserting only the absence
  // would keep passing if the buttons were removed altogether, and asserting
  // only the presence would keep passing if they were shown on a document that
  // does not exist yet — where an upload has nothing to attach to.
  it("offers attachments once the document exists", async () => {
    const { container } = renderInRouter(
      <DocEditor value="hello" onChange={vi.fn()} documentId="doc-1" />,
    );
    await surface(container);

    expect(screen.getByTitle(/insert image/i)).toBeInTheDocument();
    expect(screen.getByTitle(/attach file/i)).toBeInTheDocument();
  });

  it("drops the attachment affordances while there is no document to own the bytes", async () => {
    const { container } = renderInRouter(<DocEditor value="hello" onChange={vi.fn()} />);
    await surface(container);

    expect(screen.queryByTitle(/insert image/i)).not.toBeInTheDocument();
    expect(screen.queryByTitle(/attach file/i)).not.toBeInTheDocument();
  });

  it("takes in a document changed from outside without being re-created", async () => {
    const { container, rerender } = renderInRouter(
      <DocEditor value={"# First"} onChange={vi.fn()} readOnly />,
    );
    const first = await surface(container);
    expect(first.textContent).toContain("First");

    rerender(
      <MemoryRouter>
        <DocEditor value={"# Second"} onChange={vi.fn()} readOnly />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(first.textContent).toContain("Second");
    });
    // Same ProseMirror instance: switching documents must not leak an editor.
    expect(container.querySelectorAll(".ProseMirror")).toHaveLength(1);
  });

  it("does not report a change back for a value that came from outside", async () => {
    const onChange = vi.fn();
    const { container, rerender } = renderInRouter(
      <DocEditor value={"# First"} onChange={onChange} />,
    );
    await surface(container);

    rerender(
      <MemoryRouter>
        <DocEditor value={"# Second"} onChange={onChange} />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(container.querySelector(".ProseMirror")?.textContent).toContain("Second");
    });

    // An echo here would mark the document dirty on open and have the autosave
    // write it straight back — a save the user never asked for.
    expect(onChange).not.toHaveBeenCalled();
  });
});
