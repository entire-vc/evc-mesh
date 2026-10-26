import { describe, expect, it } from "vitest";
import { renderMarkdownFragment } from "@/lib/milkdown/render";

async function html(markdown: string, breaks = true): Promise<string> {
  const host = document.createElement("div");
  host.append(await renderMarkdownFragment(markdown, breaks));
  return host.innerHTML;
}

describe("renderMarkdownFragment with breaks", () => {
  it("draws a typed newline as a line break", async () => {
    const out = await html("line one\nReply with the single word ok.");
    expect(out).toMatch(/^<p>line one<br[^>]*>Reply with the single word ok\.<\/p>$/);
  });

  it("keeps a newline inside emphasis and leaves paragraphs and lists alone", async () => {
    expect(await html("**a\nb**")).toMatch(/<strong>a<br[^>]*>b<\/strong>/);
    expect(await html("a\n\nb")).toBe("<p>a</p><p>b</p>");
    const list = await html("- a\n- b");
    expect(list).not.toContain("<br");
    expect(list.match(/<li/g)).toHaveLength(2);
  });

  it("draws code exactly as without breaks: fenced, fence-like lines inside it, inline", async () => {
    for (const md of ["```\nx\n```~\n```js\ny\n```", "see `a\nb` here", "    x\n    y"]) {
      const out = await html(md);
      expect(out, md).not.toContain("<br");
      expect(out, md).toBe(await html(md, false));
    }
  });

  it("is off by default: the shared engine keeps a soft break inline", async () => {
    const out = await html("line one\nline two", false);
    expect(out).not.toContain("<br");
    expect(out).toContain('data-is-inline="true"');
  });
});
