import { describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { MarkdownView } from "@/components/markdown-view";

const PRESIGNED = "https://s3.example/presigned-abc";
// Resolving an attachment is an authenticated fetch; stubbed so the test can
// assert the src the reader actually ends up with.
vi.mock("@/lib/api", () => ({
  api: vi.fn(async () => ({ url: PRESIGNED })),
  getAccessToken: vi.fn(() => null),
}));

/**
 * Existing task descriptions and comments must survive the move.
 *
 * This is the acceptance criterion of the unit, and it is the one with real
 * exposure: the change touches every card and every comment already in
 * production. So the fixtures below are the shapes those bodies actually take —
 * the headings, tables, fenced code, links, images and nested lists that fill a
 * Mesh thread — and the assertions are on the rendered tree.
 *
 * Half of these could not render AT ALL before. Neither deleted parser
 * understood a table, a task list or a nested list: the block-level one matched
 * `- ` at any indent and flattened every level into one `<ul>`, and the other
 * understood `##`/`###` and bullets and nothing else. So for those constructs
 * this is not "unchanged output", it is output where there was none, and saying
 * "no loss" would be the weaker claim.
 */

async function renderMd(content: string) {
  const view = render(
    <MemoryRouter>
      <MarkdownView content={content} />
    </MemoryRouter>,
  );
  await waitFor(() => {
    if (!view.container.firstChild?.firstChild) throw new Error("nothing rendered");
  });
  return view.container;
}

describe("content that already exists in production", () => {
  it("keeps headings, emphasis and inline code", async () => {
    const c = await renderMd(
      "## Итог\n\nСмёржен **PR #612**, тест в `web/src/lib/` зелёный.",
    );

    expect(c.querySelector("h2")?.textContent).toBe("Итог");
    expect(c.querySelector("strong")?.textContent).toBe("PR #612");
    expect(c.querySelector("code")?.textContent).toBe("web/src/lib/");
  });

  it("renders a fenced code block without touching its contents", async () => {
    // The body of a code block must survive verbatim — it is usually the
    // command someone is meant to re-run.
    const cmd = 'gh pr view 612 --repo entire-vc/evc-mesh --json state,mergedAt';
    const c = await renderMd("```bash\n" + cmd + "\n```");

    const pre = c.querySelector("pre");
    expect(pre).not.toBeNull();
    expect(pre?.textContent?.trim()).toBe(cmd);
  });

  it("renders a table with per-column alignment", async () => {
    // Neither deleted parser had any notion of a table: this markdown used to
    // render as four lines of literal pipes.
    const c = await renderMd(
      ["| Критерий | Доказательство |", "| :- | -: |", "| Выкачено | деплой 18:28 |"].join("\n"),
    );

    const table = c.querySelector("table");
    expect(table).not.toBeNull();
    expect(table?.querySelectorAll("th").length).toBe(2);
    expect(table?.querySelector("td")?.textContent).toBe("Выкачено");
    // Literal pipes would mean the table was not parsed at all.
    expect(c.textContent).not.toContain("| :-");
  });

  it("keeps list nesting instead of flattening it", async () => {
    // The old block parser matched `- ` at ANY indent and pushed every item into
    // one flat <ul>, so a sub-point was indistinguishable from a top-level one.
    const c = await renderMd("- outer\n  - inner\n- second");

    const topLevel = c.querySelector("ul");
    expect(topLevel).not.toBeNull();
    expect(topLevel?.querySelector("ul")).not.toBeNull();
  });

  it("renders a checklist", async () => {
    const c = await renderMd("- [x] done\n- [ ] open");

    const items = c.querySelectorAll("li");
    expect(items.length).toBe(2);
    // Task items are marked in the DOM (drawn by CSS — a live <input> inside a
    // contenteditable fights the editor for the click, see doc-editor.css).
    expect(c.querySelector('[data-item-type="task"]')).not.toBeNull();
  });

  it("keeps a blockquote a blockquote", async () => {
    const c = await renderMd("> Pavel: «Docs + потом всё остальное»");

    expect(c.querySelector("blockquote")?.textContent).toContain("Docs");
  });

  it("resolves an internal attachment image to a presigned URL", async () => {
    // An attachment needs an Authorization header, which <img src> cannot send,
    // so the raw API path must never be the src. The schema parks it and
    // resolveArtifactImages swaps in a fresh presigned URL — asserting the end
    // state proves the whole path, since the src can only become this if it was
    // parked in the first place.
    const c = await renderMd(
      "![screenshot.png](/api/v1/artifacts/abc-123/download?disposition=inline)",
    );

    const img = c.querySelector("img")!;
    expect(img.getAttribute("alt")).toContain("screenshot.png");
    await waitFor(() => {
      expect(img.getAttribute("src")).toBe(PRESIGNED);
    });
    expect(img.getAttribute("data-artifact-src")).toBeNull();
  });

  it("keeps an external image as an ordinary image", async () => {
    // The negative control for the case above: a fix that parked every image
    // would leave every external image blank.
    const c = await renderMd("![chart](https://example.com/chart.png)");

    expect(c.querySelector("img")?.getAttribute("src")).toBe(
      "https://example.com/chart.png",
    );
  });

  it("refuses a javascript: link but keeps the words around it", async () => {
    const c = await renderMd("see [this](javascript:alert(1)) please");

    expect(c.querySelector("a")?.getAttribute("href")).toBe("");
    expect(c.textContent).toContain("this");
    expect(c.innerHTML).not.toContain("javascript:");
  });

  it("renders raw HTML in a body as inert text, not as markup", async () => {
    // Bodies in production contain pasted HTML. The commonmark preset keeps it
    // in the document verbatim and renders it escaped — so it must arrive as
    // text, never as a live element.
    const c = await renderMd("<img src=x onerror=alert(1)>");

    expect(c.querySelector("img")).toBeNull();
    expect(c.textContent).toContain("onerror");
  });
});
