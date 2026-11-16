import { parserCtx } from "@milkdown/kit/core";
import { Fragment, Slice } from "@milkdown/kit/prose/model";
import { Plugin, PluginKey } from "@milkdown/kit/prose/state";
import type { EditorView } from "@milkdown/kit/prose/view";
import { $prose } from "@milkdown/kit/utils";

/**
 * Paste of markdown *source* becomes structure.
 *
 * ProseMirror's default turns `text/plain` into paragraphs of literal text, so
 * a `.md` file pasted into a document stayed `### Title`, `- item`,
 * `| a | b |` on screen. Here plain text is run through the editor's own
 * markdown parser — the one that reads stored documents — so a paste becomes
 * exactly what opening the same file would show, sanitised by the same schema
 * (safe links, attachment-aware images, inert raw HTML).
 *
 * Deliberately narrow, so that nothing which used to paste well changes:
 *
 * - Rendered HTML (a browser selection, Google Docs, Word) is left to
 *   ProseMirror. Its `text/plain` flavour is the *rendered* text, which carries
 *   no markdown; parsing it would turn a page's literal `*` into emphasis.
 * - HTML that is only a styled rendering of characters — what code editors put
 *   next to `text/plain` (VS Code, JetBrains, Sublime: monospace `<div>`/`<pre>`
 *   and `<span>`s, no headings, lists, links or tables) — is treated as plain
 *   text, since the `text/plain` beside it is the source.
 * - VS Code says which language it copied from. Anything but markdown is code,
 *   not markdown, and keeps the old path.
 * - Inside a code block, and when files are on the clipboard (uploads own that
 *   paste), nothing happens here.
 *
 * Milkdown's own `@milkdown/plugin-clipboard` was not used: it also replaces
 * the copy serializer and turns every VS Code paste — markdown included — into
 * a code block, both of which would change behaviour this card must not touch.
 */

/** Elements a code editor's clipboard HTML is built from. Anything else is real markup. */
const PLAIN_HTML_TAGS = new Set([
  "html",
  "head",
  "body",
  "meta",
  "style",
  "title",
  "div",
  "span",
  "pre",
  "code",
  "br",
  "font",
]);

const MONOSPACE_HINT =
  /white-space\s*:\s*pre|monospace|menlo|monaco|consolas|courier|jetbrains|fira\s*code|source\s*code|sf\s*mono/i;

/**
 * True when `html` is a styled rendering of plain characters rather than
 * markup: built only from layout tags and either wrapped in `<pre>` or styled
 * as monospace / pre-formatted.
 */
export function isPlainTextHtml(html: string): boolean {
  const template = document.createElement("template");
  template.innerHTML = html;
  const elements = template.content.querySelectorAll("*");
  let monospace = false;
  for (const el of Array.from(elements)) {
    const tag = el.tagName.toLowerCase();
    if (!PLAIN_HTML_TAGS.has(tag)) return false;
    if (tag === "pre" || tag === "code") monospace = true;
    const style = el.getAttribute("style") ?? "";
    const face = el.getAttribute("face") ?? "";
    if (MONOSPACE_HINT.test(style) || MONOSPACE_HINT.test(face)) monospace = true;
  }
  return monospace;
}

/** The language VS Code tagged the copy with, or null when it is not VS Code. */
function vscodeMode(data: DataTransfer): string | null {
  const raw = data.getData("vscode-editor-data");
  if (!raw) return null;
  try {
    const mode = (JSON.parse(raw) as { mode?: unknown }).mode;
    return typeof mode === "string" ? mode : "";
  } catch {
    return "";
  }
}

/** Decides whether this clipboard is markdown source to be parsed. */
export function isMarkdownSourcePaste(data: DataTransfer): boolean {
  if (data.files && data.files.length > 0) return false;
  const text = data.getData("text/plain");
  if (!text.trim()) return false;

  const mode = vscodeMode(data);
  if (mode !== null) return mode === "markdown";

  const html = data.getData("text/html");
  if (!html) return true;
  return isPlainTextHtml(html);
}

/**
 * Markdown trims a paragraph's edge spaces; a word pasted mid-sentence needs
 * them ("bold " must not glue onto the next word). Only an inline paste — one
 * paragraph — gets them back; block pastes keep the parser's output.
 */
function keepEdgeSpaces(content: Fragment, text: string): Fragment {
  if (content.childCount !== 1) return content;
  const para = content.firstChild!;
  if (para.type.name !== "paragraph" || para.content.size === 0) return content;
  const schema = para.type.schema;
  const lead = /^[ \t]+/.exec(text)?.[0] ?? "";
  const trail = /[ \t]+$/.exec(text)?.[0] ?? "";
  if (!lead && !trail) return content;
  let inner = para.content;
  if (lead) inner = Fragment.from(schema.text(lead)).append(inner);
  if (trail) inner = inner.append(Fragment.from(schema.text(trail)));
  return Fragment.from(para.copy(inner));
}

export const markdownPasteKey = new PluginKey("MESH_MARKDOWN_PASTE");

export const markdownPaste = $prose((ctx) => {
  return new Plugin({
    key: markdownPasteKey,
    props: {
      handlePaste: (view: EditorView, event: ClipboardEvent) => {
        if (!view.editable) return false;
        const data = event.clipboardData;
        if (!data) return false;

        const { $from } = view.state.selection;
        if ($from.parent.type.spec.code) return false;
        if (!isMarkdownSourcePaste(data)) return false;

        const text = data.getData("text/plain").replace(/\r\n?/g, "\n");
        const parse = ctx.get(parserCtx);
        let doc;
        try {
          doc = parse(text);
        } catch {
          // A parser failure must never cost the user their paste.
          return false;
        }
        if (!doc || typeof doc === "string" || doc.content.size === 0) {
          return false;
        }

        // Open on both sides: a single paragraph merges into the paragraph the
        // cursor is in, as a plain-text paste would; blocks split it.
        const slice = Slice.maxOpen(keepEdgeSpaces(doc.content, text));
        view.dispatch(
          view.state.tr
            .replaceSelection(slice)
            .scrollIntoView()
            .setMeta("paste", true)
            .setMeta("uiEvent", "paste"),
        );
        return true;
      },
    },
  });
});
