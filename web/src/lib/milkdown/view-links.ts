import { isResolvableAttachmentPath } from "@/lib/artifact-links";

/**
 * How a link behaves on a read-only surface.
 *
 * Three kinds of anchor come out of the renderer and they need three different
 * things, none of which the markdown itself says:
 *
 *  - a link to another site should open in a new tab, and must carry
 *    `rel="noopener noreferrer"` when it does;
 *  - a link into this app should navigate in place (handled by the click
 *    handler in markdown-view.tsx) and must NOT get a target, or the click
 *    handler never sees it;
 *  - an internal attachment is opened by resolving a fresh presigned URL on
 *    click, so it must not be followed as an href at all.
 *
 * This is presentation, not dialect: it is applied to the rendered tree rather
 * than baked into the schema, so the markdown a task stores is exactly the
 * markdown a document stores. Milkdown's `link` mark has no target attribute
 * and never will — it serialises back to `[text](url)`, which has nowhere to
 * put one.
 *
 * Without this pass every outbound link in every task description and comment
 * would quietly start opening in the current tab, losing the reader's place —
 * the sort of regression that no error reports and everybody notices.
 */
export function applyLinkTargets(root: ParentNode): void {
  for (const a of Array.from(root.querySelectorAll("a"))) {
    const href = a.getAttribute("href") ?? "";
    // Refused by the href allow-list (`javascript:`, protocol-relative, …).
    // Left inert: the link text is the author's content and must survive, but
    // it points nowhere and must not be dressed up as a working link.
    if (!href) continue;
    if (isResolvableAttachmentPath(href)) continue;
    // Root-relative is a route of this app. `//host/x` is not — it is another
    // origin wearing a leading slash — but the allow-list has already emptied
    // that href, so it never reaches here.
    if (href.startsWith("/")) continue;
    a.setAttribute("target", "_blank");
    a.setAttribute("rel", "noopener noreferrer");
  }
}
