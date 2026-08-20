import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { MarkdownRenderer } from "@/components/markdown-renderer";

const mockedNavigate = vi.fn();
vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mockedNavigate };
});

vi.mock("@/lib/api", () => ({ api: vi.fn(), getAccessToken: vi.fn(() => null) }));

/**
 * Links into this app must not leave it.
 *
 * Measured before the fix: `[Runbook](/w/acme/p/demo/docs/doc-1)` rendered as
 *
 *   <a href="/w/…" target="_blank" rel="noopener noreferrer" …>
 *
 * and nothing intercepted the click, so following a document link from a task
 * description meant a new browser tab and a full page load — the SPA's state,
 * scroll position and open panels all gone. That is why a document link could
 * not simply BE a link until now, and why the alternative on the table was
 * inventing a `doc://` pseudo-scheme with a component to recognise it.
 */

function renderMd(content: string) {
  return render(
    <MemoryRouter>
      <MarkdownRenderer content={content} />
    </MemoryRouter>,
  );
}

const DOC_LINK = "[Runbook](/w/acme/p/demo/docs/doc-1)";

describe("internal links", () => {
  it("do not open in a new tab", () => {
    const { container } = renderMd(`See ${DOC_LINK} first.`);
    const a = container.querySelector("a")!;

    expect(a.getAttribute("href")).toBe("/w/acme/p/demo/docs/doc-1");
    expect(a.getAttribute("target")).toBeNull();
  });

  it("navigate in place", () => {
    mockedNavigate.mockClear();
    const { container } = renderMd(DOC_LINK);

    fireEvent.click(container.querySelector("a")!, { button: 0 });

    expect(mockedNavigate).toHaveBeenCalledWith("/w/acme/p/demo/docs/doc-1");
  });

  it("still let the reader ask for a new tab themselves", () => {
    // Ctrl/Cmd-click is a deliberate request. Swallowing it would be a worse
    // bug than the one being fixed, and an infuriating one.
    mockedNavigate.mockClear();
    const { container } = renderMd(DOC_LINK);

    fireEvent.click(container.querySelector("a")!, { button: 0, metaKey: true });

    expect(mockedNavigate).not.toHaveBeenCalled();
  });
});

describe("external links are untouched", () => {
  it("keep opening in a new tab", () => {
    // The negative control. A fix that made everything navigate in place would
    // pass every test above and break every external link in the product.
    const { container } = renderMd("[Docs](https://example.com/guide)");
    const a = container.querySelector("a")!;

    expect(a.getAttribute("target")).toBe("_blank");
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("do not navigate in place when clicked", () => {
    mockedNavigate.mockClear();
    const { container } = renderMd("[Docs](https://example.com/guide)");

    fireEvent.click(container.querySelector("a")!, { button: 0 });

    expect(mockedNavigate).not.toHaveBeenCalled();
  });

  it("treat a protocol-relative URL as external, not as a route", () => {
    // `//example.com/x` starts with a slash and is another origin. Handing it to
    // the router would render a route that does not exist, on a path the reader
    // cannot get back from.
    const { container } = renderMd("[Elsewhere](//example.com/x)");
    const a = container.querySelector("a")!;

    expect(a.getAttribute("target")).toBe("_blank");
    expect(a.className).not.toContain("internal-link");
  });
});
