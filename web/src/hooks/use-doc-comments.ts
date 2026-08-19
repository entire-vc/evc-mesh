import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  type DocAnchor,
  makeRangeAnchor,
  projectionOf,
  resolveRangeAnchor,
} from "@/lib/docs/anchor";
import {
  COMMENT_HIGHLIGHT,
  COMMENT_HIGHLIGHT_ACTIVE,
  blocksOfElement,
  domRangeFor,
  paintHighlights,
  projectionRangeOfSelection,
} from "@/lib/docs/dom-range";
import {
  type CommentThread,
  type DocumentComment,
  anchorPayload,
  createDocumentComment,
  deleteDocumentComment,
  groupIntoThreads,
  listDocumentComments,
  positionOf,
  setDocumentCommentResolved,
  updateDocumentComment,
} from "@/lib/document-comments";
import { type ThreadView, shouldPaint } from "@/components/doc-comments";

/**
 * Everything a document's comments need from the rendered text.
 *
 * It lives outside the editor because it needs exactly one thing from it — the
 * surface — and because the alternative is the editor growing a prop per comment
 * feature until it has an opinion about all of them.
 *
 * The three jobs, and the rule each obeys:
 *
 *  1. **Resolve.** Every thread's anchor is resolved against the text as it is
 *     NOW, on every body change. Quote-first, refusing to guess (anchor.ts).
 *  2. **Paint.** Only threads that still resolve to their words are highlighted.
 *     An `edited` or `lost` thread is shown in the list, flagged, and NOT drawn
 *     over the document: highlighting text that is not what was commented on is
 *     the exact failure this whole scheme exists to prevent.
 *  3. **Select.** A reader's selection becomes an anchor over the same
 *     projection, so what they picked is what gets quoted.
 */

/** Where a floating control should sit, in coordinates relative to the surface. */
export interface SelectionSpot {
  top: number;
  left: number;
}

export interface PendingComment {
  anchor: DocAnchor;
  spot: SelectionSpot;
}

interface UseDocComments {
  threads: ThreadView[];
  /** A live selection worth offering a "Comment" button for. */
  selection: (PendingComment & { quote: string }) | null;
  /** The selection the reader chose to comment on, or null. */
  pending: (PendingComment & { quote: string }) | null;
  busy: boolean;
  error: string | null;

  beginComment: () => void;
  cancelComment: () => void;
  submitComment: (body: string) => Promise<void>;
  reply: (rootId: string, body: string) => Promise<void>;
  edit: (commentId: string, body: string) => Promise<void>;
  remove: (commentId: string) => Promise<void>;
  setResolved: (rootId: string, resolved: boolean) => Promise<void>;
  focusThread: (rootId: string) => void;
}

function message(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
}

export function useDocComments(
  documentId: string | undefined,
  /** The rendered document body. Null while the viewer is not mounted. */
  surface: HTMLElement | null,
  /** The markdown currently on screen — resolution has to re-run when it changes. */
  body: string,
  /** Comments are a read-only-mode affordance; editing the page suspends them. */
  active: boolean,
): UseDocComments {
  const [comments, setComments] = useState<DocumentComment[]>([]);
  const [selection, setSelection] = useState<(PendingComment & { quote: string }) | null>(null);
  const [pending, setPending] = useState<(PendingComment & { quote: string }) | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [focused, setFocused] = useState<string | null>(null);

  // ---- Load -----------------------------------------------------------------
  useEffect(() => {
    if (!documentId) {
      setComments([]);
      return;
    }
    let cancelled = false;
    listDocumentComments(documentId)
      .then((items) => {
        if (!cancelled) setComments(items);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(message(err, "Failed to load comments"));
      });
    return () => {
      cancelled = true;
    };
  }, [documentId]);

  // ---- Resolve --------------------------------------------------------------
  // Keyed on `body` as well as the surface: the anchors have to be re-resolved
  // against the text as it is now, not as it was when the page opened.
  const resolved = useMemo(() => {
    const grouped = groupIntoThreads(comments);
    if (!surface) {
      // No rendered text to resolve against. `lost` would be a claim about the
      // document; this is a claim about us, so nothing is painted and nothing is
      // asserted — the list still shows every thread.
      return grouped.map((t) => ({ ...t, state: "exact" as const, range: null }));
    }
    const blocks = blocksOfElement(surface);
    const text = projectionOf(blocks);
    return grouped.map((thread: CommentThread) => {
      // An orphan is a quote the API already knows it cannot place — no offsets.
      // Re-deriving a verdict for it would be inventing one; it is `lost`, and
      // the list says so.
      const anchor = positionOf(thread.root.anchor);
      if (!anchor) return { ...thread, state: "lost" as const, range: null };
      const match = resolveRangeAnchor(text, anchor);
      return {
        ...thread,
        state: match.status,
        range:
          match.start === null
            ? null
            : domRangeFor(surface, blocks, match.start, match.end!),
      };
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [comments, surface, body]);

  const threads: ThreadView[] = useMemo(
    () => resolved.map(({ range: _range, ...rest }) => rest),
    [resolved],
  );

  // ---- Paint ----------------------------------------------------------------
  useEffect(() => {
    if (!active) {
      paintHighlights(COMMENT_HIGHLIGHT, []);
      paintHighlights(COMMENT_HIGHLIGHT_ACTIVE, []);
      return;
    }
    // The rule itself lives in shouldPaint, next to the component that states it
    // in words, so the two cannot drift: a resolved thread has been dealt with,
    // and a detached one has no words of its own to point at.
    const paintable = resolved.filter((t) => t.range && shouldPaint(t.state, t.root.resolved_at));
    paintHighlights(
      COMMENT_HIGHLIGHT,
      paintable.filter((t) => t.root.id !== focused).map((t) => t.range!),
    );
    paintHighlights(
      COMMENT_HIGHLIGHT_ACTIVE,
      paintable.filter((t) => t.root.id === focused).map((t) => t.range!),
    );
    return () => {
      paintHighlights(COMMENT_HIGHLIGHT, []);
      paintHighlights(COMMENT_HIGHLIGHT_ACTIVE, []);
    };
  }, [resolved, focused, active]);

  // ---- Select ---------------------------------------------------------------
  useEffect(() => {
    if (!surface || !active) {
      setSelection(null);
      return;
    }
    const onSelectionChange = () => {
      // While the composer is open the reader is typing into it, and the
      // document selection is stale — offering a second button would replace the
      // anchor they are already writing against.
      if (pending) return;
      const blocks = blocksOfElement(surface);
      const span = projectionRangeOfSelection(surface, blocks, window.getSelection());
      if (!span) {
        setSelection(null);
        return;
      }
      const text = projectionOf(blocks);
      const anchor = makeRangeAnchor(text, span.start, span.end);
      const range = domRangeFor(surface, blocks, span.start, span.end);
      if (!anchor || !range) {
        setSelection(null);
        return;
      }
      const rect = range.getBoundingClientRect();
      const host = surface.getBoundingClientRect();
      setSelection({
        anchor,
        quote: anchor.exact,
        // Beside the selection, on its left — the position the spec asks for,
        // clamped so a selection at the left margin does not push it off-screen.
        spot: {
          top: rect.top - host.top,
          left: Math.max(0, rect.left - host.left - 96),
        },
      });
    };
    document.addEventListener("selectionchange", onSelectionChange);
    return () => document.removeEventListener("selectionchange", onSelectionChange);
  }, [surface, active, pending]);

  // ---- Write ----------------------------------------------------------------
  const refresh = useCallback(async () => {
    if (!documentId) return;
    setComments(await listDocumentComments(documentId));
  }, [documentId]);

  const run = useCallback(
    async (fn: () => Promise<unknown>, fallback: string) => {
      setBusy(true);
      setError(null);
      try {
        await fn();
        await refresh();
      } catch (err) {
        setError(message(err, fallback));
      } finally {
        setBusy(false);
      }
    },
    [refresh],
  );

  const beginComment = useCallback(() => {
    setPending(selection);
  }, [selection]);

  const cancelComment = useCallback(() => {
    setPending(null);
    setSelection(null);
  }, []);

  const submitComment = useCallback(
    async (commentBody: string) => {
      if (!documentId || !pending) return;
      await run(
        () =>
          createDocumentComment(documentId, {
            body: commentBody,
            anchor: anchorPayload(pending.anchor),
          }),
        "Failed to add the comment",
      );
      setPending(null);
      setSelection(null);
    },
    [documentId, pending, run],
  );

  const reply = useCallback(
    async (rootId: string, commentBody: string) => {
      if (!documentId) return;
      await run(
        () => createDocumentComment(documentId, { body: commentBody, parent_comment_id: rootId }),
        "Failed to reply",
      );
    },
    [documentId, run],
  );

  const edit = useCallback(
    async (commentId: string, commentBody: string) =>
      run(() => updateDocumentComment(commentId, commentBody), "Failed to save the edit"),
    [run],
  );

  const remove = useCallback(
    async (commentId: string) =>
      run(() => deleteDocumentComment(commentId), "Failed to delete the comment"),
    [run],
  );

  const setResolved = useCallback(
    async (rootId: string, isResolved: boolean) =>
      run(
        () => setDocumentCommentResolved(rootId, isResolved),
        isResolved ? "Failed to resolve" : "Failed to reopen",
      ),
    [run],
  );

  const focusThread = useCallback(
    (rootId: string) => {
      setFocused(rootId);
      const found = resolved.find((t) => t.root.id === rootId);
      const rect = found?.range?.getBoundingClientRect();
      if (!rect) return;
      // scrollIntoView needs an element; the range's own start container is the
      // closest thing a text range has to one.
      const node = found!.range!.startContainer;
      const el = node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
      el?.scrollIntoView({ block: "center", behavior: "smooth" });
    },
    [resolved],
  );

  // A focused thread is a transient, like the paragraph-link highlight: it says
  // how the reader got here, not what state the document is in.
  const focusTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    if (!focused) return;
    if (focusTimer.current) clearTimeout(focusTimer.current);
    focusTimer.current = setTimeout(() => setFocused(null), 3500);
    return () => {
      if (focusTimer.current) clearTimeout(focusTimer.current);
    };
  }, [focused]);

  return {
    threads,
    selection: pending ? null : selection,
    pending,
    busy,
    error,
    beginComment,
    cancelComment,
    submitComment,
    reply,
    edit,
    remove,
    setResolved,
    focusThread,
  };
}
