import { beforeEach, describe, expect, it } from "vitest";
import {
  flattenText,
  indexOfPosition,
  rangeFromSpan,
  spanFromRange,
} from "@/lib/doc-comments/dom-text";

/**
 * The bridge between the anchoring code, which works on one flat string, and
 * the browser, which works on text nodes. Everything the highlight draws and
 * every selection the composer reads passes through here.
 */

function mount(html: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("flattenText", () => {
  it("concatenates the text of a single paragraph", () => {
    const host = mount("<p>Hello world</p>");
    expect(flattenText(host).text).toBe("Hello world");
  });

  it("separates adjacent blocks so their words do not run together", () => {
    // Without the separator this reads "One paragraph.Another one." and a quote
    // of "paragraph.Another" would match text that is on no screen.
    const host = mount("<p>One paragraph.</p><p>Another one.</p>");
    expect(flattenText(host).text).toBe("One paragraph.\nAnother one.");
  });

  it("does not separate inline runs inside one block", () => {
    const host = mount("<p>Deploys are <strong>blocked</strong> until Friday.</p>");
    expect(flattenText(host).text).toBe("Deploys are blocked until Friday.");
  });

  it("keeps Cyrillic text intact", () => {
    const host = mount("<h1>Кириллица</h1><p>Первый абзац.</p>");
    expect(flattenText(host).text).toBe("Кириллица\nПервый абзац.");
  });

  it("walks list items as separate blocks", () => {
    const host = mount("<ul><li>alpha</li><li>beta</li></ul>");
    expect(flattenText(host).text).toBe("alpha\nbeta");
  });
});

describe("rangeFromSpan", () => {
  it("produces a Range whose text is the span", () => {
    const host = mount("<p>Deploys are blocked until Friday.</p>");
    const flat = flattenText(host);
    const start = flat.text.indexOf("blocked");
    const range = rangeFromSpan(flat, { start, end: start + "blocked".length });
    expect(range?.toString()).toBe("blocked");
  });

  it("spans an inline element boundary", () => {
    const host = mount("<p>Deploys are <strong>blocked</strong> until Friday.</p>");
    const flat = flattenText(host);
    const start = flat.text.indexOf("are blocked until");
    const range = rangeFromSpan(flat, {
      start,
      end: start + "are blocked until".length,
    });
    expect(range?.toString()).toBe("are blocked until");
  });

  it("spans a Cyrillic quote across two blocks", () => {
    const host = mount("<p>Первый абзац.</p><p>Второй абзац.</p>");
    const flat = flattenText(host);
    const start = flat.text.indexOf("абзац.\nВторой");
    const range = rangeFromSpan(flat, { start, end: start + "абзац.\nВторой".length });
    // The synthetic separator belongs to no node, so the Range covers the text
    // on both sides of it.
    expect(range?.toString()).toBe("абзац.Второй");
  });

  it("returns null for an empty span rather than a collapsed Range", () => {
    const host = mount("<p>Hello</p>");
    const flat = flattenText(host);
    expect(rangeFromSpan(flat, { start: 2, end: 2 })).toBeNull();
  });
});

describe("spanFromRange", () => {
  it("round-trips a Range back to the span it came from", () => {
    const host = mount("<p>Deploys are <em>blocked</em> until Friday.</p>");
    const flat = flattenText(host);
    const span = { start: 8, end: 25 };
    const range = rangeFromSpan(flat, span);
    expect(range).not.toBeNull();
    expect(spanFromRange(flat, range!)).toEqual(span);
  });

  it("normalises a backwards range", () => {
    const host = mount("<p>Hello world</p>");
    const flat = flattenText(host);
    const node = host.querySelector("p")!.firstChild!;
    const range = document.createRange();
    range.setStart(node, 6);
    range.setEnd(node, 6);
    // Build it backwards by hand: setEnd before setStart is what a
    // right-to-left drag produces once the browser reports it.
    const backwards = document.createRange();
    backwards.setStart(node, 6);
    backwards.setEnd(node, 11);
    expect(spanFromRange(flat, backwards)).toEqual({ start: 6, end: 11 });
  });

  it("returns null for a position outside the container", () => {
    const host = mount("<p>Inside</p>");
    const outside = mount("<p>Outside</p>");
    const flat = flattenText(host);
    const node = outside.querySelector("p")!.firstChild!;
    expect(indexOfPosition(flat, node, 0)).toBeNull();
  });
});
