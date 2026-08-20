/**
 * Painting comment highlights over the rendered document.
 *
 * The CSS Custom Highlight API rather than `<mark>` wrappers: the document body
 * is a ProseMirror view, which observes its own subtree and is entitled to undo
 * elements it did not put there. A Highlight is a set of Ranges the engine
 * paints; the DOM is untouched, so there is nothing for ProseMirror to disagree
 * with and nothing to unwind when a thread is resolved.
 *
 * Where the API is missing (jsdom under test, Firefox before 140) every function
 * here is a no-op and the rail still works — the highlight is the affordance
 * that degrades, not the feature.
 */

export const HIGHLIGHT_NAME = "mesh-doc-comment";
export const ACTIVE_HIGHLIGHT_NAME = "mesh-doc-comment-active";

interface HighlightRegistry {
  set(name: string, highlight: unknown): void;
  delete(name: string): void;
}

type HighlightCtor = new (...ranges: Range[]) => unknown;

function registry(): HighlightRegistry | null {
  const css = (globalThis as { CSS?: { highlights?: HighlightRegistry } }).CSS;
  return css?.highlights ?? null;
}

function ctor(): HighlightCtor | null {
  return (globalThis as { Highlight?: HighlightCtor }).Highlight ?? null;
}

/** True when this browser can paint highlights at all. */
export function highlightsSupported(): boolean {
  return registry() !== null && ctor() !== null;
}

/**
 * Replace the painted highlights.
 *
 * Both sets are written on every call, including when empty, so a resolved
 * thread's highlight disappears rather than lingering as a range nothing points
 * at any more.
 */
export function paintHighlights(ranges: Range[], activeRanges: Range[]): void {
  const highlights = registry();
  const Ctor = ctor();
  if (!highlights || !Ctor) return;

  if (ranges.length > 0) {
    highlights.set(HIGHLIGHT_NAME, new Ctor(...ranges));
  } else {
    highlights.delete(HIGHLIGHT_NAME);
  }

  if (activeRanges.length > 0) {
    highlights.set(ACTIVE_HIGHLIGHT_NAME, new Ctor(...activeRanges));
  } else {
    highlights.delete(ACTIVE_HIGHLIGHT_NAME);
  }
}

/** Drop every highlight this module owns. Called when leaving the document. */
export function clearHighlights(): void {
  const highlights = registry();
  if (!highlights) return;
  highlights.delete(HIGHLIGHT_NAME);
  highlights.delete(ACTIVE_HIGHLIGHT_NAME);
}
