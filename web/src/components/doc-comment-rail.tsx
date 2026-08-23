import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import {
  Bot,
  Check,
  CornerDownRight,
  Link2Off,
  MessageSquare,
  MessageSquarePlus,
  Pencil,
  RotateCcw,
  Trash2,
  User,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { MentionPickerMenu } from "@/components/mention-menu";
import { MentionText } from "@/components/mention-text";
import { useMentionPicker } from "@/hooks/use-mention-picker";
import { useMentionDirectory } from "@/hooks/use-mention-directory";
import { useWorkspaceStore } from "@/stores/workspace";
import { apiErrorMessage } from "@/lib/api-error";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import type {
  DocCommentsController,
  PlacedThread,
  ThreadPlacement,
} from "@/lib/doc-comments/use-doc-comments";
import type { ActorType, DocumentComment } from "@/types";

/** What the body renderer needs to tell a mention from an @ in prose. */
type MentionDirectory = ReturnType<typeof useMentionDirectory>;
import "@/components/doc-comments.css";

// ---------------------------------------------------------------------------
// Small pieces
// ---------------------------------------------------------------------------

function ActorLabel({ type, name }: { type: ActorType; name?: string | null }) {
  const Icon = type === "agent" ? Bot : User;
  const fallback = type === "agent" ? "Agent" : type === "system" ? "System" : "User";
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-xs font-medium">
      <Icon
        className={cn(
          "h-3.5 w-3.5 shrink-0",
          type === "agent" ? "text-violet-500" : "text-sky-500",
        )}
      />
      <span className="truncate">{name?.trim() || fallback}</span>
    </span>
  );
}

/**
 * The quote the thread was written about.
 *
 * Shown for every anchored thread, not only the broken ones: the rail is beside
 * the document, not inside it, and "this is the sentence I meant" is the whole
 * content of an inline comment's context.
 */
function Quote({
  text,
  placement,
}: {
  text: string;
  placement: ThreadPlacement;
}) {
  const broken = placement === "orphaned" || placement === "detached";
  return (
    <blockquote
      className={cn(
        "mb-2 border-l-2 pl-2 text-xs italic",
        broken
          ? "border-muted-foreground/40 text-muted-foreground line-through decoration-muted-foreground/50"
          : "border-yellow-400 text-muted-foreground",
      )}
    >
      {text}
    </blockquote>
  );
}

/**
 * Says, in the rail, that a thread has lost its place in the text.
 *
 * These are deliberately loud rather than hidden. A thread nobody can find is
 * still a thread somebody wrote, and dropping it silently — which is what
 * filtering it out would be — loses the discussion along with the position.
 */
function PlacementNotice({ placement }: { placement: ThreadPlacement }) {
  if (placement === "anchored") return null;

  const text =
    placement === "page"
      ? "On the page as a whole"
      : placement === "orphaned"
        ? "No longer attached — the position was lost"
        : "No longer attached — this text is not in the page any more";

  return (
    <p className="mb-2 flex items-start gap-1.5 text-[11px] text-muted-foreground">
      <Link2Off className="mt-0.5 h-3 w-3 shrink-0" />
      <span>{text}</span>
    </p>
  );
}

// ---------------------------------------------------------------------------
// Composer
// ---------------------------------------------------------------------------

function Composer({
  placeholder,
  initialValue = "",
  submitLabel,
  autoFocus,
  onSubmit,
  onCancel,
}: {
  placeholder: string;
  initialValue?: string;
  submitLabel: string;
  autoFocus?: boolean;
  onSubmit: (body: string) => Promise<void>;
  onCancel: () => void;
}) {
  const [value, setValue] = useState(initialValue);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);

  // Wired here rather than at each of the three call sites, because all three —
  // a new thread, a reply and an edit — go through this one component.
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);
  const mentions = useMentionPicker(currentWorkspace?.id, value, setValue, ref);

  useEffect(() => {
    if (autoFocus) ref.current?.focus();
  }, [autoFocus]);

  const submit = async () => {
    const body = value.trim();
    if (!body || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onSubmit(body);
      setValue("");
    } catch (err) {
      // The field detail, not the generic "Validation failed" the server puts
      // in `message`: a refused @-mention names the slug that resolved to
      // nobody and says how to write an @ that is not a mention, and that
      // sentence is the entire remedy for the failure this feature exists to
      // make visible.
      setError(apiErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // The mention menu gets the key first when it is open, so Enter picks a name
    // instead of submitting and Escape closes the menu instead of throwing away
    // the draft. It returns false for every key it did not handle.
    if (mentions.onKeyDown(e)) return;
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      void submit();
    }
    if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    }
  };

  return (
    <div className="relative space-y-2">
      <MentionPickerMenu picker={mentions} />
      <Textarea
        ref={ref}
        rows={3}
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          setValue(e.target.value);
          mentions.onValueChange(
            e.target.value,
            e.target.selectionStart ?? e.target.value.length,
          );
        }}
        onKeyDown={onKeyDown}
        className="min-h-[64px] text-sm"
      />
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div className="flex items-center gap-2">
        <Button
          type="button"
          size="sm"
          className="h-7 px-2 text-xs"
          disabled={!value.trim() || busy}
          onClick={() => void submit()}
        >
          {busy ? "Saving..." : submitLabel}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 px-2 text-xs"
          onClick={onCancel}
          disabled={busy}
        >
          Cancel
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// One comment
// ---------------------------------------------------------------------------

function CommentBody({
  comment,
  controller,
  directory,
  isReply,
}: {
  comment: DocumentComment;
  controller: DocCommentsController;
  directory: MentionDirectory;
  isReply?: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const mine = controller.canModify(comment);
  const edited = comment.updated_at !== comment.created_at;

  return (
    <div className={cn(isReply && "mt-2 border-l-2 border-border pl-2.5")}>
      <div className="flex items-baseline justify-between gap-2">
        <ActorLabel type={comment.author_type} name={comment.author_name} />
        <span className="shrink-0 text-[11px] text-muted-foreground">
          {formatRelative(comment.created_at)}
          {edited && " · edited"}
        </span>
      </div>

      {editing ? (
        <div className="mt-1.5">
          <Composer
            placeholder="Edit your comment"
            initialValue={comment.body}
            submitLabel="Save"
            autoFocus
            onSubmit={async (body) => {
              await controller.edit(comment.id, body);
              setEditing(false);
            }}
            onCancel={() => setEditing(false)}
          />
        </div>
      ) : (
        <MentionText
          text={comment.body}
          mentionables={directory.mentionables}
          wsSlug={directory.wsSlug}
          className="mt-1 block whitespace-pre-wrap break-words text-sm"
        />
      )}

      {/* Edit and delete are author-only — the server refuses them for anybody
          else, so offering them would be offering a 403. */}
      {mine && !editing && (
        <div className="mt-1 flex items-center gap-1">
          {confirmingDelete ? (
            <>
              <span className="text-[11px] text-muted-foreground">Delete?</span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 px-1.5 text-[11px] text-destructive"
                onClick={() => void controller.remove(comment.id)}
              >
                Yes
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 px-1.5 text-[11px]"
                onClick={() => setConfirmingDelete(false)}
              >
                No
              </Button>
            </>
          ) : (
            <>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
                onClick={() => setEditing(true)}
                disabled={controller.readOnly}
              >
                <Pencil className="h-3 w-3" />
                Edit
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
                onClick={() => setConfirmingDelete(true)}
                disabled={controller.readOnly}
              >
                <Trash2 className="h-3 w-3" />
                Delete
              </Button>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// One thread
// ---------------------------------------------------------------------------

export function Thread({
  thread,
  controller,
  directory,
}: {
  thread: PlacedThread;
  controller: DocCommentsController;
  directory: MentionDirectory;
}) {
  const [replying, setReplying] = useState(false);
  const ref = useRef<HTMLElement>(null);
  const active = controller.activeThreadId === thread.root.id;

  useEffect(() => {
    if (active) ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [active]);

  return (
    <article
      ref={ref}
      data-testid="doc-comment-thread"
      data-thread-id={thread.root.id}
      data-placement={thread.placement}
      data-resolved={thread.resolved ? "true" : "false"}
      onClick={() => controller.focusThread(thread.root.id)}
      className={cn(
        "cursor-default rounded-lg border p-3 transition-colors",
        active ? "border-yellow-400 bg-yellow-50/60 dark:bg-yellow-400/10" : "border-border",
        thread.resolved && "opacity-70",
      )}
    >
      {thread.resolved && (
        <p className="mb-2 flex items-center gap-1.5 text-[11px] font-medium text-success">
          <Check className="h-3 w-3" />
          Resolved
          {thread.root.resolved_by_name ? ` by ${thread.root.resolved_by_name}` : ""}
        </p>
      )}

      <PlacementNotice placement={thread.placement} />
      {thread.root.anchor?.exact && (
        <Quote text={thread.root.anchor.exact} placement={thread.placement} />
      )}

      <CommentBody comment={thread.root} controller={controller} directory={directory} />

      {thread.replies.map((reply) => (
        <CommentBody
          key={reply.id}
          comment={reply}
          controller={controller}
          directory={directory}
          isReply
        />
      ))}

      {!controller.readOnly && (
        <div className="mt-2 flex items-center gap-1">
          {replying ? null : (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
              onClick={() => setReplying(true)}
            >
              <CornerDownRight className="h-3 w-3" />
              Reply
            </Button>
          )}
          {/* Resolve is open to any workspace member — putting a thread away is
              part of taking part in it, not an ownership power. */}
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-6 gap-1 px-1.5 text-[11px] text-muted-foreground"
            onClick={() => void controller.setResolved(thread.root.id, !thread.resolved)}
          >
            {thread.resolved ? (
              <>
                <RotateCcw className="h-3 w-3" />
                Unresolve
              </>
            ) : (
              <>
                <Check className="h-3 w-3" />
                Resolve
              </>
            )}
          </Button>
        </div>
      )}

      {replying && (
        <div className="mt-2">
          <Composer
            placeholder="Reply… @ to mention"
            submitLabel="Reply"
            autoFocus
            onSubmit={async (body) => {
              await controller.reply(thread.root.id, body);
              setReplying(false);
            }}
            onCancel={() => setReplying(false)}
          />
        </div>
      )}
    </article>
  );
}

// ---------------------------------------------------------------------------
// The rail
// ---------------------------------------------------------------------------

export interface DocCommentRailProps {
  controller: DocCommentsController;
  showResolved: boolean;
  onShowResolvedChange: (next: boolean) => void;
}

export function DocCommentRail({
  controller,
  showResolved,
  onShowResolvedChange,
}: DocCommentRailProps) {
  const { threads, draft, isLoading, error } = controller;
  // Once for the whole rail, not once per comment: fetchTeamDirectory is
  // unconditional, so a hook inside CommentBody would issue one request per
  // comment on screen.
  const directory = useMentionDirectory();
  const visible = showResolved ? threads : threads.filter((t) => !t.resolved);
  const resolvedCount = threads.length - threads.filter((t) => !t.resolved).length;

  return (
    <aside
      aria-label="Comments"
      data-testid="doc-comment-rail"
      className="w-full shrink-0 border-t border-border pt-4 lg:w-80 lg:border-l lg:border-t-0 lg:pl-5 lg:pt-0"
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

      {controller.readOnly && (
        <p className="mb-3 rounded-md bg-muted px-2.5 py-2 text-[11px] text-muted-foreground">
          Comments are read-only while you are editing. A comment is pinned to a
          position in the saved page, and the page in front of you is not saved
          yet. Choose Done to comment again.
        </p>
      )}

      {draft && (
        <div className="mb-3 rounded-lg border border-yellow-400 bg-yellow-50/60 p-3 dark:bg-yellow-400/10">
          {draft.anchor ? (
            <>
              <Quote
                text={draft.anchor.exact}
                placement={draft.unplaceable ? "orphaned" : "anchored"}
              />
              {draft.unplaceable && (
                <p className="mb-2 flex items-start gap-1.5 text-[11px] text-muted-foreground">
                  <Link2Off className="mt-0.5 h-3 w-3 shrink-0" />
                  <span>
                    This selection could not be pinned to a position in the page
                    source. The comment will be saved with the quote only, and
                    will show as no longer attached.
                  </span>
                </p>
              )}
            </>
          ) : (
            // No quote to show — this draft has no anchor at all. Reuses the
            // same wording a page-placed thread already carries once saved
            // (see PlacementNotice), so the composer doesn't introduce new
            // copy of its own.
            <PlacementNotice placement="page" />
          )}
          <Composer
            placeholder="Add a comment… @ to mention"
            submitLabel="Comment"
            autoFocus
            onSubmit={controller.submitDraft}
            onCancel={controller.cancelDraft}
          />
        </div>
      )}

      {error ? (
        <p className="text-xs text-destructive">{error}</p>
      ) : isLoading && threads.length === 0 ? (
        <p className="text-xs text-muted-foreground">Loading comments...</p>
      ) : visible.length === 0 && !draft ? (
        <div className="flex flex-col items-start gap-1 text-xs text-muted-foreground">
          <MessageSquare className="h-5 w-5" />
          <p>
            {threads.length === 0
              ? "No comments yet."
              : "No open comments. Turn on “Show resolved” to see the rest."}
          </p>
          {threads.length === 0 && !controller.readOnly && (
            <p>Select some text in the page to start one.</p>
          )}
        </div>
      ) : (
        <div className="space-y-2 pb-8">
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
    </aside>
  );
}

// ---------------------------------------------------------------------------
// The affordance beside the selection
// ---------------------------------------------------------------------------

/**
 * The "Comment" button that appears at the end of a selection.
 *
 * Absolutely positioned inside the document container, which must therefore be
 * `relative`. onMouseDown is prevented: a mousedown anywhere else collapses the
 * selection, and the button would then act on nothing.
 */
export function DocCommentAffordance({
  controller,
}: {
  controller: DocCommentsController;
}) {
  const pending = controller.pendingSelection;
  if (!pending || controller.readOnly) return null;

  return (
    <div
      className="absolute z-20"
      style={{ top: `${pending.rect.top + 6}px`, left: `${pending.rect.left}px` }}
    >
      <Button
        type="button"
        size="sm"
        data-testid="doc-comment-affordance"
        className="h-7 gap-1 px-2 text-xs shadow-md"
        onMouseDown={(e) => e.preventDefault()}
        onClick={controller.startDraft}
      >
        <MessageSquarePlus className="h-3.5 w-3.5" />
        Comment
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// The entry point that needs no selection
// ---------------------------------------------------------------------------

/**
 * Starts a comment on the document as a whole — the only entry that does not
 * require selecting text first (`DocCommentAffordance` above does).
 *
 * Lives in the page's own action row, not inside the rail or the tree: those
 * two are mutually exclusive (`#a9df0f4a`), and a button mounted inside
 * either one would only be reachable while that particular surface happens to
 * be on screen. This one is not gated on `railOpen` at all — the click opens
 * the rail itself (`onOpenRail`) so the composer this starts always has
 * somewhere to render, then starts the draft.
 *
 * Icon-only for now: a visible label is copy under §1r.A review on
 * `#03acfaae`, and `web/**` deploys to prod on merge with no human step in
 * between (see `#41d01325`) — so the button ships with no visible text at
 * all rather than an unapproved placeholder. `aria-label`/`title` are exempt
 * from that gate (they exist for crawlers and screen readers, not as
 * product copy) and carry the button's only description until the approved
 * label lands as a follow-up commit.
 */
export function DocCommentPageEntry({
  controller,
  onOpenRail,
}: {
  controller: DocCommentsController;
  onOpenRail: () => void;
}) {
  if (controller.readOnly) return null;

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      data-testid="doc-comment-page-entry"
      className="h-7 w-7"
      title="Comment on the whole document"
      aria-label="Comment on the whole document"
      onClick={() => {
        onOpenRail();
        controller.startPageDraft();
      }}
    >
      <MessageSquarePlus className="h-3.5 w-3.5" />
    </Button>
  );
}

// ---------------------------------------------------------------------------
// The count in the actions row
// ---------------------------------------------------------------------------

export function DocCommentToggle({
  controller,
  open,
  onToggle,
}: {
  controller: DocCommentsController;
  open: boolean;
  onToggle: () => void;
}) {
  const { unresolvedCount, totalCount } = controller;

  return (
    <Button
      type="button"
      variant={open ? "secondary" : "ghost"}
      size="sm"
      data-testid="doc-comment-toggle"
      aria-pressed={open}
      className="h-7 gap-1.5 px-2 text-xs"
      onClick={onToggle}
    >
      <MessageSquare className="h-3.5 w-3.5" />
      Comments
      {/* The badge counts what is still OPEN, never the total. A document with
          one resolved thread and nothing outstanding showing "1" reads as one
          comment waiting for you, which is the opposite of what it means — so
          that case gets a tick rather than a number. */}
      {unresolvedCount > 0 ? (
        <span
          className="rounded bg-yellow-400/25 px-1 text-[11px] font-medium tabular-nums text-yellow-900 dark:text-yellow-200"
          title={`${unresolvedCount} open of ${totalCount}`}
        >
          {unresolvedCount}
        </span>
      ) : totalCount > 0 ? (
        <Check
          className="h-3.5 w-3.5 text-success"
          aria-label={`All ${totalCount} resolved`}
        />
      ) : null}
    </Button>
  );
}
