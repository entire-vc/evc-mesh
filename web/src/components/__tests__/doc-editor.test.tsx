import { beforeAll, beforeEach, describe, it, expect, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { DocEditor } from "@/components/doc-editor";
import { buildBlocks, makeAnchor } from "@/lib/docs/anchor";

// The editor resolves internal attachment links through the API helper and
// navigates internal links through the router.
vi.mock("@/lib/api", () => ({
  api: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));

vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

vi.mock("@/lib/document-attachments", () => ({
  uploadDocumentAttachment: vi.fn(),
}));

import { api } from "@/lib/api";
import { toast } from "@/components/ui/toast";
import { uploadDocumentAttachment } from "@/lib/document-attachments";

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
  beforeEach(() => {
    vi.mocked(api).mockReset();
    vi.mocked(uploadDocumentAttachment).mockReset();
    vi.mocked(toast.error).mockReset();
  });

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

  // The reported bug, in the form the user meets it: the picker opens, a file
  // is chosen, the upload is refused by the server — and the document is
  // unchanged with nothing on screen to say why. Every failure in this path was
  // swallowed (an unhandled rejection off `void insertUploaded(...)`), so
  // "nothing happens" was the whole of the user-visible behaviour.
  it("tells the user when a picked file fails to upload", async () => {
    vi.mocked(uploadDocumentAttachment).mockRejectedValue(new Error("insufficient permissions"));
    const onChange = vi.fn();
    const { container } = renderInRouter(
      <DocEditor value="hello" onChange={onChange} documentId="doc-1" />,
    );
    await surface(container);

    const picker = container.querySelector<HTMLInputElement>(
      'input[type="file"][accept="image/*"]',
    );
    if (!picker) throw new Error("image picker missing");
    fireEvent.change(picker, {
      target: { files: [new File(["x"], "pic.png", { type: "image/png" })] },
    });

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalled();
    });
    expect(String(vi.mocked(toast.error).mock.calls[0]?.[0])).toMatch(/pic\.png/);
    // The failure must not leave the "Uploading..." indicator behind either.
    expect(screen.queryByText(/Uploading/i)).not.toBeInTheDocument();
  });

  // An attachment link cannot be followed as an href — the download endpoint
  // needs an Authorization header, and the path is not an app route. Before the
  // fix the click fell through to the router, which navigated the whole SPA
  // away from the document the user was reading.
  it("opens an attached file rather than navigating the app to the API path", async () => {
    vi.mocked(api).mockResolvedValue({ url: "https://s3.example/presigned" });
    const openSpy = vi.fn();
    vi.stubGlobal("open", openSpy);

    const { container } = renderInRouter(
      <DocEditor
        value={"[spec.pdf](/api/v1/document-attachments/att-9/download?disposition=inline)"}
        onChange={vi.fn()}
        readOnly
      />,
    );
    const el = await surface(container);
    const link = within(el).getByText("spec.pdf").closest("a");
    if (!link) throw new Error("attachment link missing");

    fireEvent.click(link);

    await waitFor(() => {
      expect(api).toHaveBeenCalledWith(
        "/api/v1/document-attachments/att-9/download?disposition=inline",
      );
    });
    await waitFor(() => {
      expect(openSpy).toHaveBeenCalledWith("https://s3.example/presigned", "_blank");
    });
    vi.unstubAllGlobals();
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

// ---------------------------------------------------------------------------
// Paragraph anchors
// ---------------------------------------------------------------------------

/**
 * The pure anchor logic is tested in lib/docs/__tests__ without a browser. What
 * is tested here is the wiring that only exists once there is a real editor: a
 * right-click has to name the paragraph under the pointer, and a resolved
 * anchor has to reveal *that* element and no other. Both are the kind of thing
 * that fails by being one paragraph off, which looks like it works.
 */

const ANCHOR_DOC = `First paragraph, the introduction.

Second paragraph, the one that gets linked.

Third paragraph, the conclusion.
`;

beforeAll(() => {
  // jsdom has no layout, so it has no scrollIntoView.
  Element.prototype.scrollIntoView = vi.fn();
});

/** The Nth top-level block element of the rendered document. */
async function blockAt(container: HTMLElement, index: number) {
  const pm = await surface(container);
  const el = pm.children[index];
  if (!(el instanceof HTMLElement)) throw new Error(`no block ${index}`);
  return el;
}

describe("DocEditor paragraph anchors", () => {
  it("offers a link to the paragraph the pointer is on, and hands back its text", async () => {
    const onCopyAnchor = vi.fn();
    const { container } = renderInRouter(
      <DocEditor
        value={ANCHOR_DOC}
        onChange={vi.fn()}
        readOnly
        onCopyAnchor={onCopyAnchor}
      />,
    );
    const second = await blockAt(container, 1);

    // The native menu IS suppressed here — which is what makes the "left alone
    // on a link" test below a real assertion rather than a tautology.
    const notCancelled = fireEvent.contextMenu(second, { clientX: 40, clientY: 60 });
    expect(notCancelled).toBe(false);

    fireEvent.click(await screen.findByRole("menuitem"));

    expect(onCopyAnchor).toHaveBeenCalledTimes(1);
    const anchor = onCopyAnchor.mock.calls[0]![0];
    expect(anchor.exact).toBe("Second paragraph, the one that gets linked.");
    // The neighbours travel with it — they are what locates the paragraph once
    // its own text has been edited away.
    expect(anchor.prefix).toContain("introduction");
    expect(anchor.suffix).toContain("Third paragraph");
  });

  it("names a different paragraph for a different right-click", async () => {
    // The negative half of the test above: without this, a handler that always
    // returned block 1 would pass.
    const onCopyAnchor = vi.fn();
    const { container } = renderInRouter(
      <DocEditor
        value={ANCHOR_DOC}
        onChange={vi.fn()}
        readOnly
        onCopyAnchor={onCopyAnchor}
      />,
    );

    fireEvent.contextMenu(await blockAt(container, 2), { clientX: 10, clientY: 10 });
    fireEvent.click(await screen.findByRole("menuitem"));

    expect(onCopyAnchor.mock.calls[0]![0].exact).toBe(
      "Third paragraph, the conclusion.",
    );
  });

  it("leaves the browser's own menu alone on a link", async () => {
    const { container } = renderInRouter(
      <DocEditor
        value={"A [link](https://example.com) in a paragraph."}
        onChange={vi.fn()}
        readOnly
        onCopyAnchor={vi.fn()}
      />,
    );
    const pm = await surface(container);
    const link = pm.querySelector("a");
    expect(link).not.toBeNull();

    const prevented = !fireEvent.contextMenu(link!, { clientX: 5, clientY: 5 });

    expect(prevented).toBe(false);
    expect(screen.queryByRole("menuitem")).toBeNull();
  });

  it("offers nothing while the document is being edited", async () => {
    const { container } = renderInRouter(
      <DocEditor value={ANCHOR_DOC} onChange={vi.fn()} onCopyAnchor={vi.fn()} />,
    );

    fireEvent.contextMenu(await blockAt(container, 1), { clientX: 5, clientY: 5 });

    expect(screen.queryByRole("menuitem")).toBeNull();
  });

  it("reveals the linked paragraph, and only it", async () => {
    const anchor = makeAnchor(
      buildBlocks([
        "First paragraph, the introduction.",
        "Second paragraph, the one that gets linked.",
        "Third paragraph, the conclusion.",
      ]),
      1,
    )!;
    const onAnchorResolved = vi.fn();
    const { container } = renderInRouter(
      <DocEditor
        value={ANCHOR_DOC}
        onChange={vi.fn()}
        readOnly
        anchor={anchor}
        onAnchorResolved={onAnchorResolved}
      />,
    );

    await waitFor(() => {
      expect(onAnchorResolved).toHaveBeenCalledWith({ status: "exact", index: 1 });
    });
    expect((await blockAt(container, 1)).className).toContain("mesh-doc-anchor-hit");
    expect((await blockAt(container, 0)).className).not.toContain(
      "mesh-doc-anchor-hit",
    );
    expect((await blockAt(container, 2)).className).not.toContain(
      "mesh-doc-anchor-hit",
    );
  });

  it("follows the paragraph after text is inserted above it", async () => {
    const anchor = makeAnchor(
      buildBlocks([
        "First paragraph, the introduction.",
        "Second paragraph, the one that gets linked.",
        "Third paragraph, the conclusion.",
      ]),
      1,
    )!;
    const onAnchorResolved = vi.fn();
    const { container } = renderInRouter(
      <DocEditor
        value={`A paragraph added later.\n\n${ANCHOR_DOC}`}
        onChange={vi.fn()}
        readOnly
        anchor={anchor}
        onAnchorResolved={onAnchorResolved}
      />,
    );

    await waitFor(() => {
      expect(onAnchorResolved).toHaveBeenCalledWith({ status: "moved", index: 2 });
    });
    const revealed = await blockAt(container, 2);
    expect(revealed.textContent).toContain("gets linked");
    expect(revealed.className).toContain("mesh-doc-anchor-hit");
  });

  it("highlights nothing when the paragraph is gone, and says so", async () => {
    const anchor = makeAnchor(
      buildBlocks([
        "First paragraph, the introduction.",
        "Second paragraph, the one that gets linked.",
        "Third paragraph, the conclusion.",
      ]),
      1,
    )!;
    const onAnchorResolved = vi.fn();
    const { container } = renderInRouter(
      <DocEditor
        value={"Something else entirely.\n\nAnd another unrelated thing.\n"}
        onChange={vi.fn()}
        readOnly
        anchor={anchor}
        onAnchorResolved={onAnchorResolved}
      />,
    );

    await waitFor(() => {
      expect(onAnchorResolved).toHaveBeenCalledWith({ status: "lost", index: null });
    });
    const pm = await surface(container);
    expect(pm.querySelector(".mesh-doc-anchor-hit")).toBeNull();
  });
});
