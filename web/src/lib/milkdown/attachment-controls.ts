import type { EditorView } from "@milkdown/kit/prose/view";
import type { AttachmentSpan } from "@/lib/milkdown/attachment-spans";

/**
 * The small "remove this attachment" button drawn over every image and file
 * link a task description or comment references.
 *
 * Rendered as plain DOM nodes appended as SIBLINGS of ProseMirror's own root,
 * never as children of it and never by mutating a node ProseMirror rendered
 * (the way an `<img>`'s src is patched in artifact-links.ts' resolveArtifactImages
 * is a narrow, established exception for a presentational attribute; restructuring
 * the DOM around a node is a different order of risk — ProseMirror re-diffs that
 * subtree against the document on every update and does not expect a foreign
 * wrapper to have appeared inside it).
 *
 * Positioned with `position: absolute` against `container` (which must be
 * `position: relative` — rich-text-editor.tsx sets this) rather than
 * `position: fixed` against the viewport: an absolute child scrolls with its
 * positioned ancestor for free, so nothing here needs a scroll listener. It
 * does need recomputing whenever the document changes, which is why this is
 * called from the same effect (and on the same deps) as resolveArtifactImages.
 */
const BUTTON_CLASS = "mesh-attachment-delete-btn";

export interface AttachmentControlsOptions {
  onDelete: (span: AttachmentSpan) => void;
}

/** Remove every button this module has previously added to `container`. */
export function clearAttachmentControls(container: HTMLElement): void {
  container.querySelectorAll(`.${BUTTON_CLASS}`).forEach((el) => el.remove());
}

/**
 * Replace the delete buttons in `container` with one per span in `spans`.
 *
 * Always a full clear-and-rebuild, never an incremental diff: the editor
 * re-renders far less often than it repositions, and a stale button pointing at
 * a position the last edit shifted is worse than the cost of rebuilding a
 * handful of DOM nodes.
 */
export function syncAttachmentControls(
  container: HTMLElement,
  view: EditorView,
  spans: readonly AttachmentSpan[],
  options: AttachmentControlsOptions,
): void {
  clearAttachmentControls(container);
  if (spans.length === 0) return;

  const containerRect = container.getBoundingClientRect();

  for (const span of spans) {
    let right: number;
    let top: number;
    try {
      // `to` — the position right after the reference — puts the button at its
      // trailing edge: the top-right corner of an image, the tail of a file
      // link. `coordsAtPos` throws for a position the current doc no longer
      // has (the span list can be one render behind an in-flight edit); skip
      // that span rather than let one bad position drop every button.
      const coords = view.coordsAtPos(span.to);
      right = coords.right;
      top = coords.top;
    } catch {
      continue;
    }

    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = BUTTON_CLASS;
    btn.title = "Remove attachment";
    btn.setAttribute("aria-label", "Remove attachment");
    btn.textContent = "×";
    btn.style.left = `${right - containerRect.left - 8}px`;
    btn.style.top = `${top - containerRect.top - 8}px`;

    // mousedown, not just click: ProseMirror handles mousedown to move the
    // selection before a click ever fires, and without preventDefault here
    // that selection change lands first, then buildNodes/onDelete would delete
    // from wherever the selection ended up landing rather than from `span`.
    btn.addEventListener("mousedown", (e) => {
      e.preventDefault();
      e.stopPropagation();
    });
    btn.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      options.onDelete(span);
    });

    container.appendChild(btn);
  }
}
