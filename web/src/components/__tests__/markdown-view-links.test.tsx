import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { MarkdownView } from "@/components/markdown-view";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));

/**
 * Links into this app must not leave it.
 *
 * Measured on the deleted renderer: `[Runbook](/w/acme/p/demo/docs/doc-1)`
 * rendered with `target="_blank"` and nothing intercepted the click, so
 * following a document link from a task description meant a new browser tab and
 * a full page load — the SPA's state, scroll position and open panels all gone.
 *
 * Carried over from markdown-renderer-internal-links.test.tsx when task and
 * comment bodies moved onto the Docs editor's renderer. The contract is the
 * same; only the component that has to honour it changed, which is exactly why
 * these assertions had to come with it.
 */

async function renderMd(content: string) {
  const view = render(
    <MemoryRouter>
      <MarkdownView content={content} />
    </MemoryRouter>,
  );
  // Parsing is async (the shared engine is created on first use), so every
  // assertion has to wait for the anchor rather than read an empty box.
  const a = await waitFor(() => {
    const found = view.container.querySelector("a");
    if (!found) throw new Error("no anchor rendered");
    return found;
  });
  return { ...view, a };
}

const DOC_LINK = "[Runbook](/w/acme/p/demo/docs/doc-1)";

describe("internal links", () => {
  it("do not open in a new tab", async () => {
    const { a } = await renderMd(`See ${DOC_LINK} first.`);

    expect(a.getAttribute("href")).toBe("/w/acme/p/demo/docs/doc-1");
    expect(a.getAttribute("target")).toBeNull();
  });

  it("navigate in place", async () => {
    mockedNavigate.mockClear();
    const { a } = await renderMd(DOC_LINK);

    fireEvent.click(a, { button: 0 });

    expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs/doc-1");
  });

  it("still let the reader ask for a new tab themselves", async () => {
    // Ctrl/Cmd-click is a deliberate request. Swallowing it would be a worse
    // bug than the one being fixed, and an infuriating one.
    mockedNavigate.mockClear();
    const { a } = await renderMd(DOC_LINK);

    fireEvent.click(a, { button: 0, metaKey: true });

    expect(mockedNavigate).not.toHaveBeenCalled();
  });
});

describe("external links are untouched", () => {
  it("keep opening in a new tab", async () => {
    // The negative control, and the one that matters most here: Milkdown's link
    // mark has no target attribute of its own, so a migration that simply
    // dropped the old renderer would pass every test above while quietly making
    // every outbound link in the product open in the current tab.
    const { a } = await renderMd("[Docs](https://example.com/guide)");

    expect(a.getAttribute("target")).toBe("_blank");
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("do not navigate in place when clicked", async () => {
    mockedNavigate.mockClear();
    const { a } = await renderMd("[Docs](https://example.com/guide)");

    fireEvent.click(a, { button: 0 });

    expect(mockedNavigate).not.toHaveBeenCalled();
  });

  it("refuse a protocol-relative URL instead of routing to it", async () => {
    // `//example.com/x` starts with a slash and is another origin. The old
    // renderer opened it in a new tab; the shared href allow-list refuses it
    // outright, which is a deliberate tightening that came with the Docs
    // editor. Either way the load-bearing property is the same and is what is
    // asserted here: it must never be handed to the router.
    mockedNavigate.mockClear();
    const { a } = await renderMd("[Elsewhere](//example.com/x)");

    expect(a.getAttribute("href")).toBe("");
    expect(a.getAttribute("target")).toBeNull();

    fireEvent.click(a, { button: 0 });
    expect(mockedNavigate).not.toHaveBeenCalled();
  });
});
