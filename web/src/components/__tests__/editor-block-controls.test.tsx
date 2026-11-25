import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { DocEditor } from "@/components/doc-editor";
import { RichTextEditor } from "@/components/rich-text-editor";

vi.mock("@/lib/api", () => ({
  api: vi.fn(async () => ({ url: "https://example.com/presigned" })),
  getAccessToken: vi.fn(() => null),
  getMentionables: vi.fn(async () => []),
}));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));
vi.mock("@/lib/document-attachments", () => ({ uploadDocumentAttachment: vi.fn() }));
vi.mock("@/components/ui/toast", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

/**
 * The block-type picker and the table menu, in BOTH toolbars that carry them:
 * the Docs editor and the task description / comment editor. They are one
 * component wired twice, so each case runs against each host — a wiring
 * mistake in one host is exactly the failure this has to catch.
 *
 * Driven from the cursor's starting place: a fresh ProseMirror state puts it at
 * the start of the document, which is the first paragraph, or the first header
 * cell when the document opens with a table. jsdom cannot click a caret into a
 * contenteditable, so the body-row cases live with the commands themselves in
 * lib/milkdown/__tests__/block-state.test.ts.
 */

type Host = "doc" | "task";

function renderHost(host: Host, value: string, onChange: (v: string) => void) {
  return render(
    <MemoryRouter>
      {host === "doc" ? (
        <DocEditor value={value} onChange={onChange} documentId="doc-1" />
      ) : (
        <RichTextEditor value={value} onChange={onChange} />
      )}
    </MemoryRouter>,
  );
}

async function mounted(container: HTMLElement): Promise<HTMLElement> {
  return await waitFor(() => {
    const el = container.querySelector<HTMLElement>(".ProseMirror");
    if (!el) throw new Error("editor not mounted");
    return el;
  });
}

/** The newest markdown the editor reported, once it contains `expected`. */
async function savedMarkdown(
  onChange: ReturnType<typeof vi.fn>,
  check: (md: string) => boolean,
): Promise<string> {
  return await waitFor(
    () => {
      const md = onChange.mock.calls[onChange.mock.calls.length - 1]?.[0] as string | undefined;
      if (md === undefined || !check(md)) throw new Error(`not yet: ${md}`);
      return md;
    },
    { timeout: 2000 },
  );
}

const blockSelect = () => screen.getByRole("combobox", { name: "Block type" });
const tableTrigger = () => screen.getByRole("button", { name: /^Table options/ });

function openTableMenu(): HTMLElement {
  fireEvent.click(tableTrigger());
  return screen.getByRole("menu", { name: "Table options" });
}

/** Rows of the GFM table in `md`, delimiter row excluded. */
function tableRows(md: string): string[][] {
  return md
    .split("\n")
    .filter((l) => l.trim().startsWith("|"))
    .filter((l) => !/^\|\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|$/.test(l.trim()))
    .map((l) =>
      l
        .trim()
        .slice(1, -1)
        .split("|")
        .map((c) => c.trim()),
    );
}

const TABLE_3x3 = `| A | B | C |
| - | - | - |
| a1 | b1 | c1 |
| a2 | b2 | c2 |
`;

describe.each<[string, Host]>([
  ["Docs editor", "doc"],
  ["task description editor", "task"],
])("%s toolbar", (_name, host) => {
  beforeEach(() => vi.clearAllMocks());

  describe("block type", () => {
    it("shows the level under the cursor: Text on a paragraph, H2 on a heading", async () => {
      const first = renderHost(host, "Hello", vi.fn());
      await mounted(first.container);
      await waitFor(() => expect(blockSelect()).toHaveValue("0"));
      expect(within(blockSelect()).getByRole("option", { name: "Text" })).toBeInTheDocument();
      first.unmount();

      const second = renderHost(host, "## Section\n\nbody", vi.fn());
      await mounted(second.container);
      await waitFor(() => expect(blockSelect()).toHaveValue("2"));
      expect(
        (blockSelect() as HTMLSelectElement).selectedOptions[0]?.textContent,
      ).toBe("Heading 2");
    });

    it("turns a paragraph into H3, writing `### ` to markdown", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, "Hello", onChange);
      const pm = await mounted(container);
      await waitFor(() => expect(blockSelect()).toHaveValue("0"));

      fireEvent.change(blockSelect(), { target: { value: "3" } });

      const md = await savedMarkdown(onChange, (m) => m.includes("###"));
      expect(md.trim()).toBe("### Hello");
      expect(pm.querySelector("h3")?.textContent).toBe("Hello");
      await waitFor(() => expect(blockSelect()).toHaveValue("3"));
    });

    it("turns H3 back into a paragraph with Text", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, "### Hello", onChange);
      const pm = await mounted(container);
      await waitFor(() => expect(blockSelect()).toHaveValue("3"));

      fireEvent.change(blockSelect(), { target: { value: "0" } });

      const md = await savedMarkdown(onChange, (m) => !m.includes("#"));
      expect(md.trim()).toBe("Hello");
      expect(pm.querySelector("h3")).toBeNull();
      expect(pm.querySelector("p")?.textContent).toBe("Hello");
      await waitFor(() => expect(blockSelect()).toHaveValue("0"));
    });

    it("is disabled inside a table, where a cell holds a paragraph only", async () => {
      const { container } = renderHost(host, TABLE_3x3, vi.fn());
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());
      expect(blockSelect()).toBeDisabled();
    });
  });

  describe("table menu", () => {
    it("is offered only with the cursor in a table", async () => {
      const { container } = renderHost(host, "Hello", vi.fn());
      await mounted(container);
      await waitFor(() => expect(blockSelect()).toHaveValue("0"));
      expect(tableTrigger()).toBeDisabled();
      expect(tableTrigger()).toHaveAttribute(
        "title",
        "Table options — place the cursor in a table",
      );
    });

    it("deletes a row: 3 rows become 2, the table survives losing its header", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, TABLE_3x3, onChange);
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      // The cursor is in the header row: the hardest case, because GFM has no
      // table without a header. The first body row takes its place.
      fireEvent.click(within(openTableMenu()).getByRole("menuitem", { name: "Delete row" }));

      const md = await savedMarkdown(onChange, (m) => tableRows(m).length === 2);
      expect(tableRows(md)).toEqual([
        ["a1", "b1", "c1"],
        ["a2", "b2", "c2"],
      ]);
      expect(container.querySelectorAll("table tr")).toHaveLength(2);
      expect(container.querySelectorAll("table th")).toHaveLength(3);
    });

    it("refuses to delete the row that would leave a header-only table", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, "| A | B |\n| - | - |\n| a | b |\n", onChange);
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      const item = within(openTableMenu()).getByRole("menuitem", { name: "Delete row" });
      expect(item).toHaveAttribute("aria-disabled", "true");
      expect(item).toHaveAttribute("title", expect.stringMatching(/delete the table instead/));
      fireEvent.click(item);

      await new Promise((r) => setTimeout(r, 320));
      expect(onChange).not.toHaveBeenCalled();
      expect(container.querySelectorAll("table tr")).toHaveLength(2);
    });

    it("deletes a column: 3 columns become 2", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, TABLE_3x3, onChange);
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      fireEvent.click(within(openTableMenu()).getByRole("menuitem", { name: "Delete column" }));

      const md = await savedMarkdown(onChange, (m) => tableRows(m)[0]?.length === 2);
      expect(tableRows(md)).toEqual([
        ["B", "C"],
        ["b1", "c1"],
        ["b2", "c2"],
      ]);
    });

    it("deletes the whole table", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, `${TABLE_3x3}\nAfter`, onChange);
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      fireEvent.click(within(openTableMenu()).getByRole("menuitem", { name: "Delete table" }));

      const md = await savedMarkdown(onChange, (m) => !m.includes("|"));
      expect(md.trim()).toBe("After");
      expect(container.querySelector("table")).toBeNull();
      // The cursor left the table, so the menu has nothing left to act on.
      await waitFor(() => expect(tableTrigger()).toBeDisabled());
    });

    it("centres a column, writing `:---:` to the delimiter row", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, TABLE_3x3, onChange);
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      fireEvent.click(
        within(openTableMenu()).getByRole("menuitemradio", { name: "Align column center" }),
      );

      const md = await savedMarkdown(onChange, (m) => m.includes(":-"));
      const delimiter = md.split("\n").find((l) => /^\|\s*:?-/.test(l)) ?? "";
      const cols = delimiter.slice(1, -1).split("|").map((c) => c.trim());
      expect(cols[0]).toMatch(/^:-+:$/);
      expect(cols[1]).toMatch(/^:?-+$/);
      expect(cols[1]).not.toMatch(/-:$/);

      // The menu reports what it just did.
      expect(
        within(openTableMenu()).getByRole("menuitemradio", { name: "Align column center" }),
      ).toHaveAttribute("aria-checked", "true");
    });

    it("adds a row below and a column to the right", async () => {
      const onChange = vi.fn();
      const { container } = renderHost(host, TABLE_3x3, onChange);
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      fireEvent.click(within(openTableMenu()).getByRole("menuitem", { name: "Insert row below" }));
      await waitFor(() => expect(container.querySelectorAll("table tr")).toHaveLength(4));

      fireEvent.click(
        within(openTableMenu()).getByRole("menuitem", { name: "Insert column right" }),
      );
      const md = await savedMarkdown(onChange, (m) => tableRows(m)[0]?.length === 4);
      expect(tableRows(md)).toHaveLength(4);
      // The new column lands between A and B. Its empty cells are written as
      // `<br />` — Milkdown's long-standing serialisation of an empty cell, the
      // same as a freshly inserted table, not something this menu chooses.
      const header = tableRows(md)[0]!;
      expect([header[0], header[2], header[3]]).toEqual(["A", "B", "C"]);
      expect(header[1]).toMatch(/^(|<br \/>)$/);

      // "Row above" from the header would put a row over the header.
      expect(
        within(openTableMenu()).getByRole("menuitem", { name: "Insert row above" }),
      ).toHaveAttribute("aria-disabled", "true");
    });

    it("closes on Escape and hands focus back to its button", async () => {
      const { container } = renderHost(host, TABLE_3x3, vi.fn());
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      const menu = openTableMenu();
      const first = within(menu).getAllByRole("menuitem")[0]!;
      first.focus();
      fireEvent.keyDown(first, { key: "Escape" });

      expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
      expect(tableTrigger()).toHaveFocus();
    });

    it("closes on Escape when opened with the pointer and focus is still in the editor", async () => {
      // The trigger refuses focus to keep the editor's selection, so after a
      // click the keyboard is still in the editor — found walking it in a
      // browser, where the menu stayed open over the text.
      const { container } = renderHost(host, TABLE_3x3, vi.fn());
      const pm = await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());

      openTableMenu();
      fireEvent.keyDown(pm, { key: "Escape" });

      expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
    });
  });
});

/**
 * The same actions offered from the table itself — a right-click menu and "+"
 * strips on hover — for people who never find the toolbar button. Run against
 * both hosts for the same reason as the toolbar cases above.
 */
describe.each<[string, Host]>([
  ["Docs editor", "doc"],
  ["task description editor", "task"],
])("%s: table actions from the table itself", (_name, host) => {
  beforeEach(() => vi.clearAllMocks());

  const body = (pm: HTMLElement) => pm.closest<HTMLElement>(".mesh-doc-editor-body")!;
  const addRow = () => screen.queryByRole("button", { name: "Add a row at the end" });
  const addCol = () => screen.queryByRole("button", { name: "Add a column at the end" });
  const contextMenu = () => screen.getByRole("menu", { name: "Table options" });

  it("shows the + strips only while the pointer is over the table", async () => {
    const { container } = renderHost(host, `Intro\n\n${TABLE_3x3}`, vi.fn());
    const pm = await mounted(container);
    await waitFor(() => expect(blockSelect()).toBeEnabled());

    expect(addRow()).toBeNull();
    expect(addCol()).toBeNull();

    fireEvent.mouseOver(container.querySelectorAll("td")[4]!);
    expect(addRow()).toBeInTheDocument();
    expect(addCol()).toBeInTheDocument();

    // Onto the strip itself: still there, or it could never be clicked.
    fireEvent.mouseOver(addRow()!);
    expect(addRow()).toBeInTheDocument();

    // Over text outside the table, then out of the editor: gone.
    fireEvent.mouseOver(pm.querySelector("p")!);
    expect(addRow()).toBeNull();
    fireEvent.mouseOver(container.querySelectorAll("td")[0]!);
    expect(addRow()).toBeInTheDocument();
    fireEvent.mouseLeave(body(pm));
    expect(addRow()).toBeNull();
    expect(addCol()).toBeNull();
  });

  it("+ at the bottom appends a row after the last one, wherever the cursor is", async () => {
    const onChange = vi.fn();
    const { container } = renderHost(host, TABLE_3x3, onChange);
    await mounted(container);
    // The cursor starts in the header row; the row still goes to the end.
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    fireEvent.mouseOver(container.querySelectorAll("td")[0]!);
    fireEvent.click(addRow()!);

    const md = await savedMarkdown(onChange, (m) => tableRows(m).length === 4);
    const rows = tableRows(md);
    expect(rows.slice(0, 3)).toEqual([
      ["A", "B", "C"],
      ["a1", "b1", "c1"],
      ["a2", "b2", "c2"],
    ]);
    expect(rows[3]!.every((c) => /^(|<br \/>)$/.test(c))).toBe(true);
    expect(container.querySelectorAll("table tr")).toHaveLength(4);
  });

  it("+ on the right appends a column after the last one", async () => {
    const onChange = vi.fn();
    const { container } = renderHost(host, TABLE_3x3, onChange);
    await mounted(container);
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    fireEvent.mouseOver(container.querySelectorAll("td")[0]!);
    fireEvent.click(addCol()!);

    const md = await savedMarkdown(onChange, (m) => tableRows(m)[0]?.length === 4);
    const rows = tableRows(md);
    expect(rows.map((r) => r.slice(0, 3))).toEqual([
      ["A", "B", "C"],
      ["a1", "b1", "c1"],
      ["a2", "b2", "c2"],
    ]);
    expect(rows.every((r) => /^(|<br \/>)$/.test(r[3]!))).toBe(true);
  });

  it("right-click on a body cell opens the table menu for THAT row: delete removes it", async () => {
    const onChange = vi.fn();
    const { container } = renderHost(host, TABLE_3x3, onChange);
    await mounted(container);
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    // Cells: a1 b1 c1 a2 b2 c2 — a2 is in the last row; the cursor is in the header.
    const notPrevented = fireEvent.contextMenu(container.querySelectorAll("td")[3]!, {
      clientX: 40,
      clientY: 60,
    });
    expect(notPrevented).toBe(false);
    fireEvent.click(within(contextMenu()).getByRole("menuitem", { name: "Delete row" }));

    const md = await savedMarkdown(onChange, (m) => tableRows(m).length === 2);
    expect(tableRows(md)).toEqual([
      ["A", "B", "C"],
      ["a1", "b1", "c1"],
    ]);
    expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
  });

  it("right-click: delete column, and the refusal explained in visible text", async () => {
    const onChange = vi.fn();
    const { container } = renderHost(host, "| A | B |\n| - | - |\n| a | b |\n", onChange);
    await mounted(container);
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    fireEvent.contextMenu(container.querySelectorAll("td")[1]!);
    const menu = contextMenu();
    const delRow = within(menu).getByRole("menuitem", { name: "Delete row" });
    expect(delRow).toHaveAttribute("aria-disabled", "true");
    const reason = within(menu).getByText(/A table keeps its header and at least one row/);
    expect(reason).toBeVisible();
    expect(delRow).toHaveAttribute("aria-describedby", reason.id);
    fireEvent.click(delRow);
    expect(container.querySelectorAll("table tr")).toHaveLength(2);

    fireEvent.click(within(contextMenu()).getByRole("menuitem", { name: "Delete column" }));
    const md = await savedMarkdown(onChange, (m) => tableRows(m)[0]?.length === 1);
    expect(tableRows(md)).toEqual([["A"], ["a"]]);

    // One column left: the refusal is spelled out there too.
    fireEvent.contextMenu(container.querySelector("td")!);
    expect(
      within(contextMenu()).getByText("This is the only column — delete the table instead"),
    ).toBeVisible();
  });

  it("right-click: adds a row and a column next to the clicked cell", async () => {
    const onChange = vi.fn();
    const { container } = renderHost(host, TABLE_3x3, onChange);
    await mounted(container);
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    fireEvent.contextMenu(container.querySelectorAll("td")[0]!); // a1
    fireEvent.click(within(contextMenu()).getByRole("menuitem", { name: "Insert row above" }));
    const md = await savedMarkdown(onChange, (m) => tableRows(m).length === 4);
    expect(tableRows(md)[2]).toEqual(["a1", "b1", "c1"]);

    fireEvent.contextMenu(container.querySelectorAll("td")[3]!);
    fireEvent.click(within(contextMenu()).getByRole("menuitem", { name: "Insert column left" }));
    const md2 = await savedMarkdown(onChange, (m) => tableRows(m)[0]?.length === 4);
    const header = tableRows(md2)[0]!;
    expect(header.slice(1)).toEqual(["A", "B", "C"]);
    expect(header[0]).toMatch(/^(|<br \/>)$/);
  });

  it("Escape closes the right-click menu; the arrow keys walk it", async () => {
    const { container } = renderHost(host, TABLE_3x3, vi.fn());
    const pm = await mounted(container);
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    fireEvent.contextMenu(container.querySelectorAll("td")[0]!);
    const menu = contextMenu();
    fireEvent.keyDown(pm, { key: "ArrowDown" });
    const items = Array.from(menu.querySelectorAll<HTMLElement>("[role^=menuitem]"));
    expect(items[0]).toHaveFocus();
    fireEvent.keyDown(items[0]!, { key: "ArrowDown" });
    expect(items[1]).toHaveFocus();

    fireEvent.keyDown(items[1]!, { key: "Escape" });
    expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
    expect(pm).toHaveFocus();
  });

  it("the keyboard's menu key (contextmenu on the editor root) opens it at the caret in a table", async () => {
    const onChange = vi.fn();
    const { container } = renderHost(host, TABLE_3x3, onChange);
    const pm = await mounted(container);
    // The caret starts in the header row's first cell.
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    expect(fireEvent.contextMenu(pm)).toBe(false);
    fireEvent.click(within(contextMenu()).getByRole("menuitem", { name: "Delete column" }));
    const md = await savedMarkdown(onChange, (m) => tableRows(m)[0]?.length === 2);
    expect(tableRows(md)[0]).toEqual(["B", "C"]);
  });

  it("the menu key with the caret outside a table leaves the browser menu alone", async () => {
    const { container } = renderHost(host, `Intro\n\n${TABLE_3x3}`, vi.fn());
    const pm = await mounted(container);
    await waitFor(() => expect(blockSelect()).toBeEnabled());

    expect(fireEvent.contextMenu(pm)).toBe(true);
    expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
  });

  it("a touch long-press keeps the browser menu (select / paste on phones)", async () => {
    const { container } = renderHost(host, TABLE_3x3, vi.fn());
    await mounted(container);
    await waitFor(() => expect(tableTrigger()).toBeEnabled());

    const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "pointerType", { value: "touch" });
    expect(container.querySelectorAll("td")[0]!.dispatchEvent(ev)).toBe(true);
    expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
  });

  it("outside a table, and on a link in a cell, the browser keeps its own menu", async () => {
    const { container } = renderHost(
      host,
      "Intro\n\n| A | B |\n| - | - |\n| [site](https://example.com) | b |\n",
      vi.fn(),
    );
    const pm = await mounted(container);
    await waitFor(() => expect(blockSelect()).toBeEnabled());

    expect(fireEvent.contextMenu(pm.querySelector("p")!)).toBe(true);
    expect(fireEvent.contextMenu(pm.querySelector("td a")!)).toBe(true);
    expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
  });

  it("listens for no mousemove anywhere: hovering costs boundary events only", async () => {
    const docAdd = vi.spyOn(document, "addEventListener");
    const winAdd = vi.spyOn(window, "addEventListener");
    const elAdd = vi.spyOn(HTMLElement.prototype, "addEventListener");
    try {
      const { container } = renderHost(host, TABLE_3x3, vi.fn());
      await mounted(container);
      await waitFor(() => expect(tableTrigger()).toBeEnabled());
      fireEvent.mouseOver(container.querySelectorAll("td")[0]!);
      fireEvent.contextMenu(container.querySelectorAll("td")[0]!);
      expect(addRow()).toBeInTheDocument();

      // React's own root delegation registers every event type once, at
      // createRoot, whether anything uses it or not; those are not ours.
      const moves = [...docAdd.mock.calls, ...winAdd.mock.calls, ...elAdd.mock.calls].filter(
        ([type, listener]) =>
          (type === "mousemove" || type === "pointermove") &&
          !/^bound dispatch/.test((listener as { name?: string } | null)?.name ?? ""),
      );
      expect(moves).toEqual([]);
    } finally {
      docAdd.mockRestore();
      winAdd.mockRestore();
      elAdd.mockRestore();
    }
  });
});

describe("read-only document: nothing from the table", () => {
  it("shows no strips and keeps the browser's menu", async () => {
    const { container } = render(
      <MemoryRouter>
        <DocEditor value={TABLE_3x3} onChange={vi.fn()} readOnly />
      </MemoryRouter>,
    );
    await waitFor(() => expect(container.querySelector("table td")).not.toBeNull());
    const cell = container.querySelectorAll("td")[0]!;

    fireEvent.mouseOver(cell);
    expect(screen.queryByRole("button", { name: "Add a row at the end" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add a column at the end" })).toBeNull();
    expect(fireEvent.contextMenu(cell)).toBe(true);
    expect(screen.queryByRole("menu", { name: "Table options" })).toBeNull();
    expect(container.querySelector("[data-table-handles]")).toBeNull();
  });
});
