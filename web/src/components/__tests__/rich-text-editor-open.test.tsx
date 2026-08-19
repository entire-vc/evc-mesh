import { describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { editorViewCtx } from "@milkdown/kit/core";
import { RichTextEditor } from "@/components/rich-text-editor";
import { makeDocEditor } from "@/lib/milkdown/editor";

vi.mock("@/lib/api", () => ({
  api: vi.fn(async () => ({ url: "https://example.com/presigned" })),
  getAccessToken: vi.fn(() => null),
  getMentionables: vi.fn(async () => []),
}));
vi.mock("@/hooks/useProjectTrIntegration", () => ({
  useProjectTrIntegration: () => ({ enabled: false }),
}));

/**
 * Opening the editor must not, by itself, change the value.
 *
 * The task panel autosaves a changed description two seconds after the draft
 * moves (task-panel.tsx, `flushDescription`). So an editor that pushes a
 * re-serialised value up on mount rewrites the description of every task
 * somebody merely clicked Edit on — no keystroke required, silently, across
 * every card in production.
 *
 * The fixtures are the constructs that DO get normalised when the writer
 * genuinely edits: `---` serialises back as `***` (remark refuses `-` for both
 * bullet and thematic break, and bullets win), table cells get padded, `~` is
 * escaped because GFM spends it on strikethrough, and a bare address becomes an
 * explicit autolink. Every one of those is a real difference in the saved text,
 * which is exactly why the no-rewrite-on-open property has to be pinned rather
 * than assumed: the two are one keystroke apart.
 *
 * Measured over 120 real production task descriptions and comments before this
 * test was written: 0 of 120 emitted anything on mount, and all 120 rendered to
 * identical visible text after a round trip — the normalisation is cosmetic in
 * the source and invisible to the reader.
 */

const BODIES: [string, string][] = [
  ["a thematic break", "before\n\n---\n\nafter"],
  [
    "a table",
    "| Критерий | Значение |\n| :- | -: |\n| Выкачено | да |",
  ],
  ["a tilde", "Замер ~02:45 и ~1055 установок"],
  ["a bare address", "писать на team-relay@entire.vc, там ждут"],
  ["an empty inline code span", "- agent: ``"],
  ["a checklist and nested list", "- [x] done\n- outer\n  - inner"],
  ["a fenced block", "```bash\ngh pr view 631 --json state\n```"],
];

describe("opening a body in the editor", () => {
  it.each(BODIES)("does not rewrite %s", async (_name, body) => {
    const onChange = vi.fn();
    render(
      <MemoryRouter>
        <RichTextEditor value={body} onChange={onChange} />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(document.querySelector(".mesh-doc-prose")).not.toBeNull();
    });
    // Milkdown's listener debounces 200ms. Asserting sooner reads "quiet" off a
    // working editor and off a dead one alike — the control that makes this
    // assertion mean anything.
    await new Promise((resolve) => setTimeout(resolve, 320));

    expect(onChange).not.toHaveBeenCalled();
  });

  it("still reports a real edit", async () => {
    // The positive control, and this test is worthless without it: a suite that
    // only ever asserts "onChange was NOT called" passes just as happily on an
    // editor whose listener is not wired at all.
    //
    // Driven through makeDocEditor rather than the React component because a
    // ProseMirror view cannot be reached from the DOM (pmViewDesc carries no
    // reference back to it) and jsdom cannot type into a contenteditable. This
    // is the same factory, the same onMarkdown wiring and the same debounce the
    // component uses — only the transaction is dispatched directly.
    const seen: string[] = [];
    const root = document.createElement("div");
    document.body.appendChild(root);
    const editor = makeDocEditor({
      root,
      defaultValue: "before",
      editable: true,
      editorClassName: "control",
      onMarkdown: (md) => seen.push(md),
    });
    await editor.create();

    const view = editor.ctx.get(editorViewCtx);
    view.dispatch(view.state.tr.insertText(" and after", view.state.doc.content.size - 1));
    await new Promise((resolve) => setTimeout(resolve, 320));

    expect(seen.length).toBeGreaterThan(0);
    expect(seen[seen.length - 1]).toContain("and after");
    await editor.destroy();
  });
});
