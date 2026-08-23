import { useMemo, useState } from "react";
import { MessageSquare } from "lucide-react";
import { useMentionDirectory } from "@/hooks/use-mention-directory";
import { Composer, Thread } from "@/components/doc-comment-rail";
import type {
  DocCommentsController,
  PlacedThread,
} from "@/lib/doc-comments/use-doc-comments";

/**
 * The discussion, laid out under the page it is about.
 *
 * ## Why this exists next to the rail
 *
 * The rail is the *working* surface: narrow, beside the text, collapsible, and
 * the thing you use while a selection is live. This is the *reading* surface —
 * the whole discussion in one column under the document, always present, in the
 * order the anchors appear in the page rather than the order they were written.
 *
 * Both were asked for, in one sentence, as two things: a panel by the selection,
 * and the same threads shown as a tree at the bottom of the document.
 *
 * ## Two constraints that are easy to get wrong
 *
 * **It must render OUTSIDE `controller.containerRef`.** That element is what the
 * comment layer flattens into "the text of this document" in order to find each
 * quote. Mount the discussion inside it and every comment body becomes part of
 * the haystack: a quote would start matching the text of a comment quoting it,
 * and anchors would resolve to the wrong place — silently, and more often the
 * more people talked. The tree therefore sits after the measured container, as
 * a sibling.
 *
 * **It shares state with the rail rather than keeping its own.** Same
 * controller, same `showResolved` flag, same `Thread` component. Resolve a
 * thread in either place and both change, because there is only one of it. A
 * second thread renderer here is what would let the two surfaces disagree, and
 * "the tree at the bottom shows the same thing" is the acceptance criterion, not
 * a nicety.
 *
 * ## The composer at the bottom is always mounted (Pavel, 2026-08-23, `#744ae979`)
 *
 * A prior change (`e2e40de`, `#a9df0f4a`) mounted this section only while the
 * rail was closed, to stop the two surfaces painting the same thread twice.
 * Pavel's read: hiding the tree behind the rail was the wrong fix for that —
 * the tree is where a comment gets *started* as much as where it gets *read*,
 * so it stays on screen with the rail open or closed, and a reader who opens
 * the rail is not supposed to lose the surface they were writing in. The
 * duplicate-render bug this used to guard against is real, but the fix for it
 * is scoping a query to one surface at a time (`within(rail)` /
 * `within(tree)`), not un-mounting one of them — see `docs.test.tsx`.
 *
 * The section itself therefore no longer returns `null` on an empty page: the
 * composer needs somewhere to render even before the first comment exists.
 */

/**
 * Document order, with everything that has no place in the document after it.
 *
 * `threads` arrives in the order the API returned, which is the order the
 * comments were written. Under the page that reads as noise — the reader is
 * looking at the text, so the discussion should follow the text. Threads with no
 * span (on the page as a whole, or no longer attached) have no position to sort
 * by and keep their relative order at the end, where they are not claiming to
 * be about the paragraph they happen to land next to.
 */
export function inDocumentOrder(threads: PlacedThread[]): PlacedThread[] {
  const placed: PlacedThread[] = [];
  const unplaced: PlacedThread[] = [];
  for (const t of threads) (t.span ? placed : unplaced).push(t);
  placed.sort((a, b) => {
    const byStart = (a.span?.start ?? 0) - (b.span?.start ?? 0);
    // A stable tie-break, so two comments on the same words do not swap places
    // between renders: same start -> the shorter selection first, then oldest.
    if (byStart !== 0) return byStart;
    const byLength = (a.span?.end ?? 0) - (b.span?.end ?? 0);
    if (byLength !== 0) return byLength;
    return a.root.created_at.localeCompare(b.root.created_at);
  });
  return [...placed, ...unplaced];
}

export interface DocCommentTreeProps {
  controller: DocCommentsController;
  showResolved: boolean;
  onShowResolvedChange: (next: boolean) => void;
}

export function DocCommentTree({
  controller,
  showResolved,
  onShowResolvedChange,
}: DocCommentTreeProps) {
  const { threads, readOnly } = controller;
  // Once for the whole tree, not once per comment — see the hook's own note.
  const directory = useMentionDirectory();

  // Remounts the composer on Cancel, which is the only way to clear it: it
  // keeps its own typed text (see doc-comment-rail.tsx), and unlike the
  // rail's draft box there is no `draft` state here whose absence would take
  // the box away — this one has to stay on screen.
  const [composerKey, setComposerKey] = useState(0);

  const ordered = useMemo(() => inDocumentOrder(threads), [threads]);
  const visible = showResolved ? ordered : ordered.filter((t) => !t.resolved);
  const resolvedCount = ordered.filter((t) => t.resolved).length;

  return (
    <section
      aria-label="Comments"
      data-testid="doc-comment-tree"
      className="mt-10 border-t border-border pt-5"
    >
      <div className="flex items-center justify-between gap-2 pb-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Comments
        </h2>
        {resolvedCount > 0 && (
          <label className="flex cursor-pointer items-center gap-1.5 text-[11px] text-muted-foreground">
            <input
              type="checkbox"
              className="h-3 w-3 accent-current"
              checked={showResolved}
              onChange={(e) => onShowResolvedChange(e.target.checked)}
            />
            Show resolved ({resolvedCount})
          </label>
        )}
      </div>

      {ordered.length === 0 ? (
        <div className="flex flex-col items-start gap-1 pb-4 text-xs text-muted-foreground">
          <MessageSquare className="h-5 w-5" />
          <p>No comments yet.</p>
        </div>
      ) : (
        <div className="space-y-3 pb-4">
          {visible.map((thread) => (
            <Thread
              key={thread.root.id}
              thread={thread}
              controller={controller}
              directory={directory}
            />
          ))}
        </div>
      )}

      {/* The one entry point that needs no selection and no prior click — see
          the header note. Absent while read-only, matching every other way to
          start a comment (`DocCommentAffordance`, `DocCommentPageEntry`). */}
      {!readOnly && (
        <div className="border-t border-border pt-4">
          <Composer
            key={composerKey}
            placeholder="Add a comment… @ to mention"
            submitLabel="Comment"
            onSubmit={controller.submitPageComment}
            onCancel={() => setComposerKey((n) => n + 1)}
          />
        </div>
      )}
    </section>
  );
}
