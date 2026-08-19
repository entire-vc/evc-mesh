import { type FormEvent, useEffect, useRef, useState } from "react";
import { Check, MessageSquare, Pencil, Trash2, Undo2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import type { AnchorStatus } from "@/lib/docs/anchor";
import type { CommentThread, DocumentComment } from "@/lib/document-comments";

/**
 * The comment surface: the panel beside a selection, and the thread tree under
 * the document.
 *
 * What a thread's anchor resolved to is passed in rather than computed here —
 * the editor owns the rendered text, and this owns how it is said. The two
 * outcomes that need saying are `edited` (the quoted words changed) and `lost`
 * (they are gone). A thread in either state is still shown, still answerable and
 * still resolvable; what it loses is its highlight, because highlighting text
 * that is not what was commented on is the one failure this unit exists to
 * prevent.
 */

/** How a thread's anchor resolved against the document as it is now. */
export type ThreadAnchorState = AnchorStatus;

export interface ThreadView extends CommentThread {
  state: ThreadAnchorState;
}

/** Threads whose anchor still points at their words. */
export function isAnchored(state: ThreadAnchorState): boolean {
  return state === "exact" || state === "moved";
}

/**
 * Whether a thread's range may be drawn over the document.
 *
 * The rule the whole unit turns on, so it is a function rather than a condition
 * buried in an effect: a thread is painted only while it still resolves to its
 * own words AND is still open. `edited` and `lost` are shown in the list and
 * flagged there — painting them would put a highlight over whatever text now
 * occupies those coordinates, which is indistinguishable, to a reader, from a
 * comment that was always about those words.
 */
export function shouldPaint(
  state: ThreadAnchorState,
  resolvedAt: string | null | undefined,
): boolean {
  return isAnchored(state) && !resolvedAt;
}

function actorLabel(comment: DocumentComment): string {
  return comment.author_type === "agent" ? "Agent" : "User";
}

function timestamp(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
}

// ---------------------------------------------------------------------------
// Composer — the panel beside the selection
// ---------------------------------------------------------------------------

export function CommentComposer({
  quote,
  top,
  left,
  busy,
  onSubmit,
  onCancel,
}: {
  quote: string;
  top: number;
  left: number;
  busy: boolean;
  onSubmit: (body: string) => void;
  onCancel: () => void;
}) {
  const [body, setBody] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    ref.current?.focus();
  }, []);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = body.trim();
    if (trimmed) onSubmit(trimmed);
  };

  return (
    <form
      onSubmit={submit}
      style={{ top, left }}
      className="absolute z-30 w-72 rounded-lg border border-border bg-popover p-2 shadow-lg"
      // A click inside must not clear the selection the anchor was built from.
      onMouseDown={(e) => e.stopPropagation()}
    >
      <p className="mb-1.5 line-clamp-2 border-l-2 border-primary pl-2 text-[11px] italic text-muted-foreground">
        {quote}
      </p>
      <textarea
        ref={ref}
        value={body}
        onChange={(e) => setBody(e.target.value)}
        rows={3}
        placeholder="Add a comment"
        aria-label="Comment"
        className="w-full resize-none rounded border border-input bg-background p-1.5 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
        onKeyDown={(e) => {
          if (e.key === "Escape") onCancel();
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) submit(e);
        }}
      />
      <div className="mt-1.5 flex justify-end gap-1.5">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-6 px-2 text-xs"
          onClick={onCancel}
        >
          Cancel
        </Button>
        <Button type="submit" size="sm" className="h-6 px-2 text-xs" disabled={!body.trim() || busy}>
          {busy ? "Saving..." : "Comment"}
        </Button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// The button that appears over a selection
// ---------------------------------------------------------------------------

export function SelectionCommentButton({
  top,
  left,
  onClick,
}: {
  top: number;
  left: number;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      style={{ top, left }}
      className="absolute z-30 flex h-7 items-center gap-1 rounded-md border border-border bg-popover px-2 text-xs text-foreground shadow-md hover:bg-accent"
      // mousedown, not click: the browser clears the selection on mousedown
      // elsewhere, and the anchor is built from that selection.
      onMouseDown={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onClick();
      }}
    >
      <MessageSquare className="h-3.5 w-3.5" />
      Comment
    </button>
  );
}

// ---------------------------------------------------------------------------
// One comment
// ---------------------------------------------------------------------------

function CommentBody({
  comment,
  canEdit,
  onEdit,
  onDelete,
}: {
  comment: DocumentComment;
  canEdit: boolean;
  onEdit: (body: string) => void;
  onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(comment.body);

  useEffect(() => {
    setDraft(comment.body);
  }, [comment.body]);

  if (editing) {
    return (
      <form
        onSubmit={(e) => {
          e.preventDefault();
          const trimmed = draft.trim();
          if (!trimmed) return;
          onEdit(trimmed);
          setEditing(false);
        }}
      >
        <textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          rows={2}
          aria-label="Edit comment"
          className="w-full resize-none rounded border border-input bg-background p-1.5 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
        />
        <div className="mt-1 flex justify-end gap-1.5">
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-6 px-2 text-xs"
            onClick={() => {
              setDraft(comment.body);
              setEditing(false);
            }}
          >
            Cancel
          </Button>
          <Button type="submit" size="sm" className="h-6 px-2 text-xs" disabled={!draft.trim()}>
            Save
          </Button>
        </div>
      </form>
    );
  }

  return (
    <div className="group/comment">
      <div className="flex items-baseline gap-2">
        <span className="text-xs font-medium">{actorLabel(comment)}</span>
        <span className="text-[10px] text-muted-foreground">{timestamp(comment.created_at)}</span>
        {canEdit && (
          <span className="ml-auto flex gap-1 opacity-0 transition-opacity group-hover/comment:opacity-100">
            <button
              type="button"
              title="Edit"
              aria-label="Edit comment"
              onClick={() => setEditing(true)}
              className="text-muted-foreground hover:text-foreground"
            >
              <Pencil className="h-3 w-3" />
            </button>
            <button
              type="button"
              title="Delete"
              aria-label="Delete comment"
              onClick={onDelete}
              className="text-muted-foreground hover:text-destructive"
            >
              <Trash2 className="h-3 w-3" />
            </button>
          </span>
        )}
      </div>
      <p className="whitespace-pre-wrap text-xs text-foreground">{comment.body}</p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// The thread tree under the document
// ---------------------------------------------------------------------------

/** What to say about a thread whose anchor no longer points at its words. */
function anchorNotice(state: ThreadAnchorState): string | null {
  if (isAnchored(state)) return null;
  return state === "edited"
    ? "The commented text has been edited since."
    : "The commented text is no longer in this document.";
}

function Thread({
  thread,
  currentActorId,
  onReply,
  onEdit,
  onDelete,
  onToggleResolved,
  onFocus,
}: {
  thread: ThreadView;
  currentActorId: string | null;
  onReply: (body: string) => void;
  onEdit: (commentId: string, body: string) => void;
  onDelete: (commentId: string) => void;
  onToggleResolved: (resolved: boolean) => void;
  onFocus: () => void;
}) {
  const [reply, setReply] = useState("");
  const resolved = !!thread.root.resolved_at;
  const notice = anchorNotice(thread.state);

  return (
    <li
      className={cn(
        "rounded-lg border border-border p-2.5",
        resolved && "opacity-60",
      )}
    >
      <div className="mb-1.5 flex items-start gap-2">
        <button
          type="button"
          onClick={onFocus}
          disabled={!isAnchored(thread.state)}
          className={cn(
            "flex-1 truncate border-l-2 pl-2 text-left text-[11px] italic",
            isAnchored(thread.state)
              ? "border-primary text-muted-foreground hover:text-foreground"
              : "border-muted text-muted-foreground",
          )}
          // A detached thread has no place to scroll to, and pretending otherwise
          // would take the reader somewhere arbitrary.
          title={isAnchored(thread.state) ? "Go to this text" : undefined}
        >
          {thread.root.anchor?.exact ?? ""}
        </button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-6 shrink-0 gap-1 px-2 text-[11px]"
          onClick={() => onToggleResolved(!resolved)}
        >
          {resolved ? <Undo2 className="h-3 w-3" /> : <Check className="h-3 w-3" />}
          {resolved ? "Reopen" : "Resolve"}
        </Button>
      </div>

      {notice && (
        <p className="mb-1.5 rounded bg-muted/60 px-2 py-1 text-[11px] text-muted-foreground">
          {notice}
        </p>
      )}

      <div className="space-y-2">
        <CommentBody
          comment={thread.root}
          canEdit={thread.root.author_id === currentActorId}
          onEdit={(body) => onEdit(thread.root.id, body)}
          onDelete={() => onDelete(thread.root.id)}
        />
        {thread.replies.length > 0 && (
          <ul className="space-y-2 border-l border-border pl-3">
            {thread.replies.map((r) => (
              <li key={r.id}>
                <CommentBody
                  comment={r}
                  canEdit={r.author_id === currentActorId}
                  onEdit={(body) => onEdit(r.id, body)}
                  onDelete={() => onDelete(r.id)}
                />
              </li>
            ))}
          </ul>
        )}
      </div>

      {!resolved && (
        <form
          className="mt-2 flex gap-1.5"
          onSubmit={(e) => {
            e.preventDefault();
            const trimmed = reply.trim();
            if (!trimmed) return;
            onReply(trimmed);
            setReply("");
          }}
        >
          <input
            value={reply}
            onChange={(e) => setReply(e.target.value)}
            placeholder="Reply"
            aria-label={`Reply to comment ${thread.root.id}`}
            className="h-7 flex-1 rounded border border-input bg-background px-2 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
          />
          <Button type="submit" size="sm" className="h-7 px-2 text-xs" disabled={!reply.trim()}>
            Reply
          </Button>
        </form>
      )}
    </li>
  );
}

export function CommentThreadList({
  threads,
  currentActorId,
  onReply,
  onEdit,
  onDelete,
  onToggleResolved,
  onFocusThread,
}: {
  threads: readonly ThreadView[];
  currentActorId: string | null;
  onReply: (rootId: string, body: string) => void;
  onEdit: (commentId: string, body: string) => void;
  onDelete: (commentId: string) => void;
  onToggleResolved: (rootId: string, resolved: boolean) => void;
  onFocusThread: (rootId: string) => void;
}) {
  const [showResolved, setShowResolved] = useState(false);
  const open = threads.filter((t) => !t.root.resolved_at);
  const done = threads.filter((t) => t.root.resolved_at);
  const shown = showResolved ? [...open, ...done] : open;

  if (threads.length === 0) return null;

  return (
    <section className="border-t border-border px-4 py-3">
      <div className="mb-2 flex items-center gap-2">
        <h2 className="text-xs font-semibold">
          Comments
          <span className="ml-1.5 font-normal text-muted-foreground">{open.length} open</span>
        </h2>
        {done.length > 0 && (
          <button
            type="button"
            onClick={() => setShowResolved((v) => !v)}
            className="ml-auto text-[11px] text-muted-foreground hover:text-foreground"
          >
            {showResolved ? "Hide" : "Show"} {done.length} resolved
          </button>
        )}
      </div>
      <ul className="space-y-2">
        {shown.map((thread) => (
          <Thread
            key={thread.root.id}
            thread={thread}
            currentActorId={currentActorId}
            onReply={(body) => onReply(thread.root.id, body)}
            onEdit={onEdit}
            onDelete={onDelete}
            onToggleResolved={(resolved) => onToggleResolved(thread.root.id, resolved)}
            onFocus={() => onFocusThread(thread.root.id)}
          />
        ))}
      </ul>
    </section>
  );
}
