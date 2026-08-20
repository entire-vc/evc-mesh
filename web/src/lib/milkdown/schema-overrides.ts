import { imageSchema, linkSchema } from "@milkdown/kit/preset/commonmark";
import { isResolvableAttachmentPath } from "@/lib/artifact-links";
import { sanitizeHref } from "@/lib/milkdown/safe-href";

/**
 * Schema overrides layered on top of the commonmark preset.
 *
 * `extendSchema` re-registers a schema under the same id, and Milkdown upserts
 * by id, so `.use(commonmark).use(safeLinkSchema)` replaces the preset's version
 * in place rather than adding a second one. Both must be `.use`d, and in that
 * order.
 */

// ---------------------------------------------------------------------------
// link — the href protocol allow-list
// ---------------------------------------------------------------------------

/**
 * The commonmark `link` mark with our allow-list applied at both ends.
 *
 * parseMarkdown is the important half: an href that never enters the document
 * cannot be re-serialised into the saved body, cannot reach `getHTML()`, and
 * cannot become an anchor by some future route that forgets to sanitize. toDOM
 * is the belt to that braces — it covers a document built from ProseMirror JSON
 * or pasted HTML, which does not go through the markdown parser at all.
 */
export const safeLinkSchema = linkSchema.extendSchema((prev) => (ctx) => {
  const base = prev(ctx);
  return {
    ...base,
    // Pasted HTML does not go through the markdown parser, so the allow-list has
    // to sit on this route too.
    parseDOM: (base.parseDOM ?? []).map((rule) => {
      const original = rule.getAttrs as
        | ((dom: never) => Record<string, unknown> | false | null)
        | undefined;
      if (!original) return rule;
      return {
        ...rule,
        getAttrs: (dom: never) => {
          const attrs = original(dom);
          if (!attrs) return attrs;
          return { ...attrs, href: sanitizeHref(attrs.href) };
        },
      };
    }) as typeof base.parseDOM,
    toDOM: (mark, inline) => {
      const out = base.toDOM?.(mark, inline);
      // Every commonmark mark spec returns the ['a', attrs] array form; the
      // fallback exists so a future upstream change degrades to "no link"
      // rather than to "unsanitized link".
      if (!Array.isArray(out)) return ["a", { href: "" }, 0];
      const [tag, attrs, ...rest] = out as [string, Record<string, unknown>, ...unknown[]];
      return [tag, { ...attrs, href: sanitizeHref(mark.attrs.href) }, ...rest] as never;
    },
    parseMarkdown: {
      ...base.parseMarkdown,
      runner: (state, node, markType) => {
        const safe = { ...node, url: sanitizeHref(node.url) };
        base.parseMarkdown.runner(state, safe, markType);
      },
    },
  };
});

// ---------------------------------------------------------------------------
// image — internal attachments cannot be a plain src
// ---------------------------------------------------------------------------

/**
 * The commonmark `image` node, taught about Mesh's internal attachment paths.
 *
 * A document attachment lives behind `/api/v1/document-attachments/:id/download`,
 * which needs an Authorization header — something `<img src>` cannot send (see
 * artifact-links.ts). So for those paths the src is withheld and the path is
 * parked on `data-artifact-src`, exactly as the old renderer did, and
 * `resolveArtifactImages` swaps in a fresh presigned URL after each render.
 *
 * External images keep a real src, but only over http(s): the same rule the old
 * renderer applied, so a `javascript:`/`data:` src cannot ride in on an image.
 */
export const attachmentAwareImageSchema = imageSchema.extendSchema(
  (prev) => (ctx) => {
    const base = prev(ctx);
    return {
      ...base,
      toDOM: (node) => {
        const out = base.toDOM?.(node);
        if (!Array.isArray(out)) return ["img", {}];
        const [tag, attrs] = out as [string, Record<string, unknown>];
        const src = typeof node.attrs.src === "string" ? node.attrs.src : "";

        if (isResolvableAttachmentPath(src)) {
          const { src: _dropped, ...rest } = attrs;
          return [
            tag,
            { ...rest, "data-artifact-src": src, class: "mesh-artifact-img" },
          ] as never;
        }

        const external = /^https?:\/\//i.test(src);
        return [tag, { ...attrs, src: external ? src : "" }] as never;
      },
    };
  },
);
