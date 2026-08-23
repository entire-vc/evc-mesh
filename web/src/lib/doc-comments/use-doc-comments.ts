import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import { useAuthStore } from "@/stores/auth";
import {
  toThreads,
  useDocumentCommentStore,
  type DocumentCommentThread,
} from "@/stores/document-comment";
import type { DocumentComment, DocumentCommentAnchor } from "@/types";
import {
  anchorHintFraction,
  buildAnchorFromSelection,
  locateQuoteInRendered,
  type CharSpan,
} from "./anchor";
import {
  flattenText,
  indexFromPoint,
  rangeFromSpan,
  spanFromRange,
  type FlatText,
} from "./dom-text";
import { clearHighlights, paintHighlights } from "./highlight";

/**
 * Where a thread sits relative to the text on screen.
 *
 * `orphaned` is the server's word: the anchor kept its quote and lost its
 * offsets, because a save could not find the quote in the markdown any more.
 * `detached` is ours: the quote is not in the rendered text on this screen.
 * Both are shown, and neither is highlighted, because there is nothing truthful
 * to highlight.
 *
 * They answer different questions, which is why both exist and why a missing
 * position does not settle ours. The server asks "is this quote still in the
 * markdown"; we ask "is it on the screen the reader is looking at". The two can
 * disagree in one direction — an edit that wraps the quoted words in syntax the
 * markdown scan will not step over leaves them plainly visible while the server
 * can no longer locate them — and in that case the reader should still see the
 * highlight they had yesterday. So the quote is looked for either way, and the
 * position is used as what it has always been here: a hint for choosing between
 * repeats, absent when the server has none to offer.
 */
export type ThreadPlacement = "anchored" | "detached" | "orphaned" | "page";

export interface PlacedThread extends DocumentCommentThread {
  placement: ThreadPlacement;
  /** Where the quote is in the rendered text. Only set when anchored. */
  span: CharSpan | null;
  resolved: boolean;
}

/**
 * Decide where one thread sits, from its anchor and the text on screen.
 *
 * Pure and exported so it can be tested: it is the branch that decides whether a
 * reader keeps the highlight they had yesterday, and it used to live inside a
 * useMemo where nothing could reach it.
 *
 * The quote is looked for whether or not the anchor carries a position. The
 * position is a hint for choosing between repeats — that is all it has ever been
 * on this path — and its absence is the absence of a hint, not an instruction to
 * stop looking. Short-circuiting on it would hand a reader's highlight to the
 * server's answer to a different question: the server orphans an anchor when the
 * quote cannot be found in the saved MARKDOWN, and an edit that wraps the quoted
 * words in syntax the markdown scan will not step over leaves them plainly
 * visible on screen while the server can no longer locate them.
 */
export function placeAnchor(
  anchor: DocumentCommentAnchor | null | undefined,
  rendered: string,
  source: string,
): { placement: ThreadPlacement; span: CharSpan | null } {
  if (!anchor || !anchor.exact) {
    return { placement: "page", span: null };
  }
  const span = rendered
    ? locateQuoteInRendered(rendered, anchor, anchorHintFraction(source, anchor))
    : null;
  if (span) {
    return { placement: "anchored", span };
  }
  // Not on screen. Which of the two "not attached" states to report is the
  // server's call when it has made one: it nulls the offsets only after failing
  // to find the quote in the saved markdown, which is a stronger statement than
  // ours and the one an agent reading the API is being shown too.
  const orphaned = anchor.start === null || anchor.start === undefined;
  return { placement: orphaned ? "orphaned" : "detached", span: null };
}

/** A selection waiting to become a comment. */
export interface PendingSelection {
  span: CharSpan;
  exact: string;
  /** Position of the selection's end, relative to the document container. */
  rect: { top: number; left: number };
}

export interface DraftThread {
  /** Null for a comment on the document as a whole — no quote, no span. */
  anchor: NonNullable<import("@/types").CreateDocumentCommentRequest["anchor"]> | null;
  span: CharSpan | null;
  /** The quote could not be placed in the markdown; it will save orphaned. */
  unplaceable: boolean;
}

export interface DocCommentsController {
  containerRef: RefObject<HTMLDivElement | null>;

  threads: PlacedThread[];
  isLoading: boolean;
  error: string | null;
  /** Threads whose root is not resolved. What the actions-row count shows. */
  unresolvedCount: number;
  totalCount: number;

  /** True while the reader is editing — commenting is off, see the rail. */
  readOnly: boolean;

  activeThreadId: string | null;
  focusThread: (id: string | null) => void;

  pendingSelection: PendingSelection | null;
  /** Turn the current selection into a draft thread. */
  startDraft: () => void;
  /** Start a draft with no quote and no span — a comment on the page as a whole. */
  startPageDraft: () => void;
  draft: DraftThread | null;
  cancelDraft: () => void;
  submitDraft: (body: string) => Promise<void>;

  reply: (rootId: string, body: string) => Promise<void>;
  edit: (id: string, body: string) => Promise<void>;
  setResolved: (id: string, resolved: boolean) => Promise<void>;
  remove: (id: string) => Promise<void>;

  /** Whether the signed-in reader may edit or delete this comment. */
  canModify: (comment: DocumentComment) => boolean;
}

const SELECTION_DEBOUNCE_MS = 120;

export interface UseDocCommentsOptions {
  documentId: string | undefined;
  /** The document's markdown, as the API holds it. */
  source: string;
  /** False while the body is being edited — see the note on `readOnly`. */
  enabled: boolean;
  /** Called when a comment is created, so the rail can be opened for it. */
  onCommentCreated?: () => void;
}

/**
 * Everything the Docs page needs to show, place and manage inline comments.
 *
 * ## Commenting is a view-mode action
 *
 * While the body is being edited the rail is read-only: no new comment, no
 * reply, no resolve, no edit, no delete, and no highlights are painted.
 *
 * That is not caution, it is the anchor model. `anchor.start` is a byte offset
 * into the *saved* markdown, and the editor's buffer is autosaved on a two
 * second debounce — so for most of an editing session the text on screen is not
 * the text the offsets would be measured against, and a save can fail and leave
 * them measured against a revision that never existed. Highlights have the same
 * problem in reverse: every keystroke above a highlight moves it, and repainting
 * a stale range over live text is worse than painting nothing.
 *
 * The threads stay visible throughout, because reading the review you are
 * responding to is the entire reason you opened the editor.
 */
export function useDocComments({
  documentId,
  source,
  enabled,
  onCommentCreated,
}: UseDocCommentsOptions): DocCommentsController {
  const containerRef = useRef<HTMLDivElement | null>(null);

  const comments = useDocumentCommentStore((s) => s.comments);
  const isLoading = useDocumentCommentStore((s) => s.isLoading);
  const error = useDocumentCommentStore((s) => s.error);
  const fetchComments = useDocumentCommentStore((s) => s.fetchComments);
  const createComment = useDocumentCommentStore((s) => s.createComment);
  const updateComment = useDocumentCommentStore((s) => s.updateComment);
  const setResolvedInStore = useDocumentCommentStore((s) => s.setResolved);
  const deleteComment = useDocumentCommentStore((s) => s.deleteComment);
  const resetStore = useDocumentCommentStore((s) => s.reset);

  const user = useAuthStore((s) => s.user);

  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [pendingSelection, setPendingSelection] = useState<PendingSelection | null>(null);
  const [draft, setDraft] = useState<DraftThread | null>(null);

  // Bumped whenever the rendered body changes shape, so the flattened text and
  // every derived range are recomputed. Milkdown paints asynchronously after
  // `source` changes, so keying off `source` alone would measure an empty
  // container on first render.
  const [renderTick, setRenderTick] = useState(0);

  // ---- Load ----------------------------------------------------------------
  useEffect(() => {
    if (!documentId) {
      resetStore();
      return;
    }
    void fetchComments(documentId);
    setActiveThreadId(null);
    setDraft(null);
    setPendingSelection(null);
  }, [documentId, fetchComments, resetStore]);

  // ---- Watch the rendered body --------------------------------------------
  useEffect(() => {
    const container = containerRef.current;
    if (!container || typeof MutationObserver === "undefined") return;

    let timer: ReturnType<typeof setTimeout> | null = null;
    const observer = new MutationObserver(() => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => setRenderTick((n) => n + 1), 50);
    });
    observer.observe(container, {
      childList: true,
      subtree: true,
      characterData: true,
    });
    // The first measurement, for a body that is already painted.
    setRenderTick((n) => n + 1);

    return () => {
      if (timer) clearTimeout(timer);
      observer.disconnect();
    };
  }, [documentId, enabled]);

  const flatRef = useRef<FlatText | null>(null);
  const flat = useMemo<FlatText | null>(() => {
    void renderTick;
    void source;
    const container = containerRef.current;
    if (!container) return null;
    return flattenText(container);
  }, [renderTick, source]);
  flatRef.current = flat;

  // ---- Place the threads ---------------------------------------------------
  const threads = useMemo<PlacedThread[]>(
    () =>
      toThreads(comments).map((thread) => ({
        ...thread,
        ...placeAnchor(thread.root.anchor, flat?.text ?? "", source),
        resolved: thread.root.resolved_at !== null,
      })),
    [comments, flat, source],
  );

  const unresolvedCount = threads.filter((t) => !t.resolved).length;

  // ---- Paint ---------------------------------------------------------------
  useEffect(() => {
    if (!enabled || !flat) {
      clearHighlights();
      return;
    }

    const ranges: Range[] = [];
    const active: Range[] = [];

    for (const thread of threads) {
      // A resolved thread is one somebody put away. It keeps its place in the
      // rail and loses its mark on the page.
      if (thread.resolved || !thread.span) continue;
      const range = rangeFromSpan(flat, thread.span);
      if (!range) continue;
      if (thread.root.id === activeThreadId) active.push(range);
      else ranges.push(range);
    }

    // A page draft has no span — nothing on screen to paint for it.
    if (draft?.span) {
      const range = rangeFromSpan(flat, draft.span);
      if (range) active.push(range);
    }

    paintHighlights(ranges, active);
    return () => clearHighlights();
  }, [threads, flat, activeThreadId, draft, enabled]);

  useEffect(() => clearHighlights, []);

  // ---- Selection -----------------------------------------------------------
  useEffect(() => {
    if (!enabled || !documentId) {
      setPendingSelection(null);
      return;
    }

    let timer: ReturnType<typeof setTimeout> | null = null;

    const read = () => {
      const container = containerRef.current;
      const currentFlat = flatRef.current;
      if (!container || !currentFlat) return;

      const selection = window.getSelection();
      if (!selection || selection.isCollapsed || selection.rangeCount === 0) {
        setPendingSelection(null);
        return;
      }
      const range = selection.getRangeAt(0);
      if (!container.contains(range.commonAncestorContainer)) {
        setPendingSelection(null);
        return;
      }
      const span = spanFromRange(currentFlat, range);
      if (!span || span.end <= span.start) {
        setPendingSelection(null);
        return;
      }
      const text = currentFlat.text.slice(span.start, span.end);
      if (!text.trim()) {
        setPendingSelection(null);
        return;
      }

      // The LAST client rect, not the bounding box: a selection spanning three
      // lines has a bounding box whose bottom-right corner is empty space, and
      // the button would float away from the words it belongs to.
      const rects = range.getClientRects();
      const last = rects[rects.length - 1] ?? range.getBoundingClientRect();
      const box = container.getBoundingClientRect();
      setPendingSelection({
        span,
        exact: text,
        rect: { top: last.bottom - box.top, left: Math.max(0, last.right - box.left) },
      });
    };

    const onSelectionChange = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(read, SELECTION_DEBOUNCE_MS);
    };

    document.addEventListener("selectionchange", onSelectionChange);
    return () => {
      if (timer) clearTimeout(timer);
      document.removeEventListener("selectionchange", onSelectionChange);
    };
  }, [enabled, documentId]);

  // ---- Click a highlight to focus its thread -------------------------------
  useEffect(() => {
    const container = containerRef.current;
    if (!container || !enabled) return;

    const onClick = (event: MouseEvent) => {
      const currentFlat = flatRef.current;
      if (!currentFlat) return;
      const index = indexFromPoint(currentFlat, event.clientX, event.clientY);
      if (index === null) return;
      const hit = threads.find(
        (t) => !t.resolved && t.span && index >= t.span.start && index < t.span.end,
      );
      if (hit) setActiveThreadId(hit.root.id);
    };

    container.addEventListener("click", onClick);
    return () => container.removeEventListener("click", onClick);
  }, [threads, enabled]);

  // ---- Actions -------------------------------------------------------------
  const startDraft = useCallback(() => {
    const currentFlat = flatRef.current;
    if (!currentFlat || !pendingSelection) return;
    const built = buildAnchorFromSelection(
      source,
      currentFlat.text,
      pendingSelection.span,
    );
    if (!built) return;
    setDraft({
      anchor: built.anchor,
      span: pendingSelection.span,
      unplaceable: built.unplaceable,
    });
    setActiveThreadId(null);
    setPendingSelection(null);
    window.getSelection()?.removeAllRanges();
  }, [pendingSelection, source]);

  /**
   * Start a comment on the document as a whole — reachable without a text
   * selection, unlike `startDraft`. No anchor, no span: `buildAnchorFromSelection`
   * is deliberately not on this path, because there is no selection to build one
   * from.
   */
  const startPageDraft = useCallback(() => {
    setDraft({ anchor: null, span: null, unplaceable: false });
    setActiveThreadId(null);
    setPendingSelection(null);
    window.getSelection()?.removeAllRanges();
  }, []);

  const cancelDraft = useCallback(() => setDraft(null), []);

  const submitDraft = useCallback(
    async (body: string) => {
      if (!documentId || !draft) return;
      // `anchor` must be OMITTED for a page-level comment, not sent as null —
      // the request type only allows leaving the key out (see
      // CreateDocumentCommentRequest), matching how a reply already goes
      // through with no anchor key at all.
      const created = await createComment(
        documentId,
        draft.anchor ? { body, anchor: draft.anchor } : { body },
      );
      setDraft(null);
      setActiveThreadId(created.id);
      onCommentCreated?.();
    },
    [documentId, draft, createComment, onCommentCreated],
  );

  const reply = useCallback(
    async (rootId: string, body: string) => {
      if (!documentId) return;
      // No anchor: the server refuses a reply that carries one, and the reply
      // inherits its parent's.
      await createComment(documentId, { body, parent_comment_id: rootId });
      setActiveThreadId(rootId);
    },
    [documentId, createComment],
  );

  const edit = useCallback(
    async (id: string, body: string) => {
      await updateComment(id, body);
    },
    [updateComment],
  );

  const setResolved = useCallback(
    async (id: string, resolved: boolean) => {
      await setResolvedInStore(id, resolved);
    },
    [setResolvedInStore],
  );

  const remove = useCallback(
    async (id: string) => {
      await deleteComment(id);
      setActiveThreadId((current) => (current === id ? null : current));
    },
    [deleteComment],
  );

  /**
   * Edit and delete are author-only, and the server compares both halves of the
   * identity. A signed-in person is a `user`, so an agent's comment is never
   * theirs to change even if the uuids somehow collided.
   */
  const canModify = useCallback(
    (comment: DocumentComment) =>
      !!user && comment.author_type === "user" && comment.author_id === user.id,
    [user],
  );

  return {
    containerRef,
    threads,
    isLoading,
    error,
    unresolvedCount,
    totalCount: threads.length,
    readOnly: !enabled,
    activeThreadId,
    focusThread: setActiveThreadId,
    pendingSelection,
    startDraft,
    startPageDraft,
    draft,
    cancelDraft,
    submitDraft,
    reply,
    edit,
    setResolved,
    remove,
    canModify,
  };
}
