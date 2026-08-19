import { afterEach, describe, expect, it } from "vitest";
import { makeRangeAnchor, projectionOf, resolveRangeAnchor } from "@/lib/docs/anchor";
import {
  blocksOfElement,
  domRangeFor,
  projectionRangeOfSelection,
} from "@/lib/docs/dom-range";

/**
 * The conversion between anchor coordinates and the text on screen.
 *
 * This is where a comment highlight can end up a few characters off, and an
 * off-by-a-few highlight is indistinguishable from a correct one to everyone
 * except the person who wrote the comment. So the assertions are on the STRING
 * the range covers, never on the offsets that produced it: an offset test would
 * pass on a mapping that is consistently wrong in both directions.
 */

function mount(html: string): HTMLElement {
  const root = document.createElement("div");
  root.innerHTML = html;
  document.body.appendChild(root);
  return root;
}

afterEach(() => {
  document.body.innerHTML = "";
});

const DOC_HTML = `
  <h1>Deploy runbook</h1>
  <p>The migration is applied
     before the image swap, never after.</p>
  <p>Rollback is: <strong>revert the image</strong> first, then migrate.</p>
  <ul><li>check the gate</li><li>read the log</li></ul>
`;

describe("blocks of a rendered document", () => {
  it("collapses the whitespace a wrapped source line leaves behind", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);

    expect(blocks).toHaveLength(4);
    // The paragraph is wrapped across two lines in the markup and reads as one.
    expect(blocks[1]!.text).toBe(
      "The migration is applied before the image swap, never after.",
    );
  });
});

describe("projection offsets to a DOM range", () => {
  it("covers exactly the quoted words, across an inline element", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);
    const text = projectionOf(blocks);
    const phrase = "revert the image first";
    const start = text.indexOf(phrase);

    const range = domRangeFor(root, blocks, start, start + phrase.length);

    require(range);
    // <strong> splits the phrase across two text nodes; the range has to span
    // them, which is the case a naive single-node mapping gets wrong.
    expect(range!.toString()).toBe(phrase);
  });

  it("covers a phrase that a wrapped source line broke in half", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);
    const text = projectionOf(blocks);
    const phrase = "applied before the image";
    const start = text.indexOf(phrase);

    const range = domRangeFor(root, blocks, start, start + phrase.length);

    require(range);
    // The rendered text has a newline and indentation in the middle of this;
    // toString gives it back, which is the honest answer — what matters is that
    // the range starts and ends on the right words.
    expect(range!.toString().replace(/\s+/g, " ")).toBe(phrase);
  });

  it("covers a whole block, including one made of list items", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);
    const list = blocks[3]!;

    const range = domRangeFor(root, blocks, list.start, list.end);

    require(range);
    expect(range!.toString().replace(/\s+/g, " ")).toBe("check the gateread the log");
  });

  it("refuses an offset outside the document rather than clamping", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);
    const beyond = projectionOf(blocks).length + 50;

    expect(domRangeFor(root, blocks, beyond, beyond + 5)).toBeNull();
  });
});

describe("a reader's selection back to projection offsets", () => {
  function selectWithin(root: HTMLElement, phrase: string): Selection {
    // Find the text node containing the phrase and select exactly it.
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const text = node as Text;
      const at = text.data.indexOf(phrase);
      if (at === -1) continue;
      const range = document.createRange();
      range.setStart(text, at);
      range.setEnd(text, at + phrase.length);
      const selection = window.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);
      return selection;
    }
    throw new Error(`phrase not rendered: ${phrase}`);
  }

  it("round-trips: select words, build an anchor, resolve it, get the same words", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);
    const text = projectionOf(blocks);

    const selection = selectWithin(root, "revert the image");
    const span = projectionRangeOfSelection(root, blocks, selection);

    require(span);
    // The whole point, end to end: what the reader selected is what the anchor
    // quotes, and what the anchor resolves back to.
    const anchor = makeRangeAnchor(text, span!.start, span!.end)!;
    expect(anchor.exact).toBe("revert the image");

    const back = resolveRangeAnchor(text, anchor);
    expect(back.status).toBe("exact");
    const painted = domRangeFor(root, blocks, back.start!, back.end!);
    require(painted);
    expect(painted!.toString()).toBe("revert the image");
  });

  it("ignores a collapsed selection", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);
    const selection = window.getSelection()!;
    selection.removeAllRanges();

    expect(projectionRangeOfSelection(root, blocks, selection)).toBeNull();
  });

  it("ignores a selection made outside the document", () => {
    const root = mount(DOC_HTML);
    const blocks = blocksOfElement(root);
    const outside = mount("<p>Some other text entirely.</p>");
    const selection = selectWithin(outside, "other text");

    // Not "returns something odd" — a selection in the sidebar must not become a
    // comment on the document.
    expect(projectionRangeOfSelection(root, blocks, selection)).toBeNull();
  });
});

// A tiny assertion helper so a null slips loudly rather than becoming
// `null!.toString()` further down.
function require(value: unknown): void {
  expect(value).not.toBeNull();
}
