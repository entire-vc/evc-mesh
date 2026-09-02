import type { Node as ProseNode } from "@milkdown/kit/prose/model";
import { artifactIdFromDownloadPath } from "@/lib/artifact-links";

/**
 * One inline reference, in a task description or comment, to a task artifact —
 * an uploaded image or an "Attach file" link — located by document position
 * rather than by DOM node.
 *
 * Positions, not DOM elements: rich-text-editor already owns an EditorView, and
 * ProseMirror's own coordsAtPos/posAtCoords convert between a position and a
 * screen rect without ever reverse-engineering a DOM node back into a position
 * (the fragile direction — see attachment-controls.ts).
 */
export interface AttachmentSpan {
  /** Position immediately before the reference. */
  from: number;
  /** Position immediately after it. */
  to: number;
  /** The task artifact this reference points at — what DELETE targets. */
  artifactId: string;
}

/**
 * Every attachment reference in `doc`: an `image` node whose src is a task
 * artifact's download path, or a text run carrying a `link` mark to one (the
 * shape `buildNodes` in rich-text-editor.tsx inserts for a non-image upload).
 *
 * A text node is one leaf per call to `descendants`, so `pos`/`pos + nodeSize`
 * already bound exactly the run that mark was created on — no separate
 * "expand while the same mark holds" scan is needed the way it would be for a
 * mark applied by hand over an arbitrary selection. That scan would in any case
 * be unsound here: two attachment links sitting next to each other carry
 * different hrefs (different artifact ids), so they are never joinable into one
 * text node, and nothing merges them.
 */
export function findAttachmentSpans(doc: ProseNode): AttachmentSpan[] {
  const spans: AttachmentSpan[] = [];

  doc.descendants((node, pos) => {
    if (node.type.name === "image") {
      const src = typeof node.attrs.src === "string" ? node.attrs.src : "";
      const artifactId = artifactIdFromDownloadPath(src);
      if (artifactId) {
        spans.push({ from: pos, to: pos + node.nodeSize, artifactId });
      }
      return false; // leaf — nothing to descend into
    }

    if (node.isText) {
      const link = node.marks.find((m) => m.type.name === "link");
      const href = typeof link?.attrs.href === "string" ? link.attrs.href : "";
      const artifactId = link ? artifactIdFromDownloadPath(href) : null;
      if (artifactId) {
        spans.push({ from: pos, to: pos + node.nodeSize, artifactId });
      }
    }

    return true;
  });

  return spans;
}
