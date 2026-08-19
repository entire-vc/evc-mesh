/**
 * docs-layout-storage.ts — persists the width the reader dragged the Docs
 * document tree to, so it survives a reload and navigating away and back
 * (opening a task from a link and coming back to Docs, say).
 *
 * Same shape as board-view-storage.ts: a load/save pair that swallows every
 * storage failure, because a column width is never worth breaking a page over.
 *
 * Unlike the board filters this is deliberately NOT keyed per project. How wide
 * the tree wants to be is a property of the reader's screen and of how long
 * people around them name documents, not of one project. Keying it per project
 * would mean re-dragging it in every project, which is the complaint this
 * feature exists to answer rather than a feature of its own.
 */

const STORAGE_KEY = "mesh_docs_tree_width";

/** Fits "ADR-004 Documentation…" without truncating, which the old 16rem did not. */
export const DOC_TREE_DEFAULT_WIDTH = 280;

/** Below this the tree is narrower than its own "Documents / New" header row. */
export const DOC_TREE_MIN_WIDTH = 180;

/**
 * Above this the table of contents starts crowding the page it is a table of
 * contents for. A fixed ceiling rather than a fraction of the viewport: the
 * tree only exists at >= 768px (`md`), where 520px still leaves the document
 * a usable column, and a fraction would silently re-scale a width the reader
 * chose deliberately every time they resized the window.
 */
export const DOC_TREE_MAX_WIDTH = 520;

/** The only place the bounds are enforced — every entry point goes through it. */
export function clampDocTreeWidth(width: number): number {
  if (!Number.isFinite(width)) return DOC_TREE_DEFAULT_WIDTH;
  return Math.min(
    DOC_TREE_MAX_WIDTH,
    Math.max(DOC_TREE_MIN_WIDTH, Math.round(width)),
  );
}

/** The stored width, or null when there is nothing usable stored. */
export function loadDocTreeWidth(): number | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = Number.parseInt(raw, 10);
    if (!Number.isFinite(parsed)) return null;
    // Clamped on the way out too: the bounds can change between releases, and a
    // width written by an older build must not survive as an unreachable one.
    return clampDocTreeWidth(parsed);
  } catch {
    return null;
  }
}

export function saveDocTreeWidth(width: number): void {
  try {
    localStorage.setItem(STORAGE_KEY, String(clampDocTreeWidth(width)));
  } catch {
    // localStorage unavailable (private browsing quota, disabled, etc.) — the
    // width simply won't outlive the session, which is a safe degradation.
  }
}
