import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Bot, Edit2, Lock, Reply, Trash2, User } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { type MentionEntry } from "@/components/markdown-view";
import { MarkdownWithRelay } from "@/components/MarkdownWithRelay";
import { RichTextEditor } from "@/components/rich-text-editor";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";
import type {
  ActorType,
  Comment,
  CommentDeliveryOutcome,
  CreateCommentRequest,
  PaginatedResponse,
} from "@/types";

interface CommentListProps {
  taskId: string;
  projId: string;
  /**
   * A comment (root or reply) to scroll to and highlight on arrival — the id
   * named by a mention's `?comment=` query param (see task-panel.tsx). May
   * live on a page not yet loaded; CommentList paginates to find it.
   */
  focusCommentId?: string | null;
}

function ActorIcon({ type }: { type: ActorType }) {
  if (type === "agent") {
    return <Bot className="h-4 w-4 text-violet-500" />;
  }
  if (type === "system") {
    return <User className="h-4 w-4 text-muted-foreground" />;
  }
  return <User className="h-4 w-4 text-sky-500" />;
}

function ActorLabel({ type, name }: { type: ActorType; name?: string }) {
  const fallback = type === "agent" ? "Agent" : type === "system" ? "System" : "User";
  const displayName = name?.trim() || fallback;
  return (
    <span className="flex items-center gap-1.5 text-sm font-medium">
      <ActorIcon type={type} />
      {displayName}
    </span>
  );
}

/**
 * The delivery record for one comment: which handles it addressed, and what
 * became of each.
 *
 * Renders the API's own identifiers verbatim — `skipped`, `no_queue_path`,
 * `recipient_offline` — rather than prose. That is deliberate on two counts.
 * The values are a stable machine vocabulary shared with the REST payload and
 * the database, so a reader who sees one here can grep for it. And visible
 * product copy is gated on an explicit approval that this change does not
 * carry, so inventing friendlier sentences here would ship unapproved voice.
 *
 * Nothing renders when a comment addressed nobody, which is most comments.
 */
function DeliveryRecord({ rows }: { rows?: CommentDeliveryOutcome[] }) {
  if (!rows || rows.length === 0) return null;

  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-1">
      {rows.map((row) => (
        <Badge
          key={row.recipient_slug}
          variant="outline"
          className={cn(
            "gap-1 font-mono text-[10px] font-normal",
            row.outcome === "delivered" && "text-muted-foreground",
            // Not-delivered is the state worth seeing across a room, since the
            // whole defect being fixed is that it currently looks like success.
            row.outcome === "skipped" && "text-yellow-600",
            row.outcome === "failed" && "text-destructive",
          )}
          title={`@${row.recipient_slug} · ${row.outcome} · ${row.reason} · channel=${row.channel} · presence=${row.recipient_presence}`}
        >
          <span>@{row.recipient_slug}</span>
          <span aria-hidden="true">·</span>
          <span>{row.outcome}</span>
          <span aria-hidden="true">·</span>
          <span>{row.reason}</span>
        </Badge>
      ))}
    </div>
  );
}

interface CommentItemProps {
  comment: Comment;
  isReply?: boolean;
  replies: Comment[];
  onReply: (parentId: string) => void;
  onSave: (commentId: string, newBody: string) => Promise<void>;
  onDelete: (commentId: string) => void;
  projId?: string;
  mentionables?: Map<string, MentionEntry>;
  wsSlug?: string;
  focusCommentId?: string | null;
}

function CommentItem({
  comment,
  isReply,
  replies,
  onReply,
  onSave,
  onDelete,
  projId,
  mentionables,
  wsSlug,
  focusCommentId,
}: CommentItemProps) {
  const [hovering, setHovering] = useState(false);
  const [isEditing, setIsEditing] = useState(false);
  const [editBody, setEditBody] = useState(comment.body);
  const [saving, setSaving] = useState(false);
  const editRef = useRef<HTMLTextAreaElement>(null);
  const focused = focusCommentId != null && comment.id === focusCommentId;
  const focusRef = useRef<HTMLDivElement>(null);

  // Arrived via a mention's `?comment=` link: scroll this comment into view
  // and hold a highlight on it, the same treatment doc-comment-rail.tsx gives
  // an active thread — a rail that opens to its default scroll position
  // (top) does not, on its own, prove the right comment was ever reached.
  useEffect(() => {
    if (focused) focusRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [focused]);

  const handleEditClick = () => {
    setEditBody(comment.body);
    setIsEditing(true);
    setTimeout(() => editRef.current?.focus(), 0);
  };

  const handleSave = async () => {
    if (!editBody.trim() || saving) return;
    setSaving(true);
    try {
      await onSave(comment.id, editBody.trim());
      setIsEditing(false);
    } finally {
      setSaving(false);
    }
  };

  const handleCancelEdit = () => {
    setIsEditing(false);
    setEditBody(comment.body);
  };

  return (
    <div className={cn("group", isReply && "ml-8 border-l-2 border-border pl-4")}>
      <div
        ref={focusRef}
        data-comment-id={comment.id}
        className={cn(
          "rounded-lg border border-transparent p-3 transition-colors hover:bg-muted/50",
          focused && "border-yellow-400 bg-yellow-50/60 dark:bg-yellow-400/10",
        )}
        onMouseEnter={() => setHovering(true)}
        onMouseLeave={() => setHovering(false)}
      >
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2">
            <ActorLabel type={comment.author_type} name={comment.author_name} />
            <span className="text-xs text-muted-foreground">
              {formatRelative(comment.created_at)}
            </span>
            {comment.is_internal && (
              <Badge variant="outline" className="gap-1 text-[10px] text-yellow-600">
                <Lock className="h-2.5 w-2.5" />
                Internal
              </Badge>
            )}
          </div>
          {hovering && !isEditing && (
            <div className="flex items-center gap-1">
              {!isReply && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-6 w-6"
                  onClick={() => onReply(comment.id)}
                  title="Reply"
                >
                  <Reply className="h-3 w-3" />
                </Button>
              )}
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6"
                onClick={handleEditClick}
                title="Edit"
              >
                <Edit2 className="h-3 w-3" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-6 w-6 text-destructive"
                onClick={() => onDelete(comment.id)}
                title="Delete"
              >
                <Trash2 className="h-3 w-3" />
              </Button>
            </div>
          )}
        </div>

        {isEditing ? (
          <div className="mt-1.5 space-y-2">
            <RichTextEditor
              value={editBody}
              onChange={setEditBody}
              projId={projId}
              taskId={comment.task_id}
              minHeight="4rem"
              hint={null}
            />
            <div className="flex items-center gap-2 justify-end">
              <Button
                variant="ghost"
                size="sm"
                onClick={handleCancelEdit}
                disabled={saving}
              >
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={handleSave}
                disabled={!editBody.trim() || saving}
              >
                Save
              </Button>
            </div>
          </div>
        ) : (
          <MarkdownWithRelay
            content={comment.body}
            className="mt-1.5"
            projId={projId}
            mentionables={mentionables}
            wsSlug={wsSlug}
          />
        )}

        <DeliveryRecord rows={comment.delivery} />
      </div>

      {replies.length > 0 && (
        <div className="mt-1 space-y-1">
          {replies.map((reply) => (
            <CommentItem
              key={reply.id}
              comment={reply}
              isReply
              replies={[]}
              onReply={onReply}
              onSave={onSave}
              onDelete={onDelete}
              projId={projId}
              mentionables={mentionables}
              wsSlug={wsSlug}
              focusCommentId={focusCommentId}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function byNewestFirst(a: Comment, b: Comment): number {
  return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
}

/** Safety bound on how many older pages a focus-comment search will walk. */
const MAX_AUTO_FOCUS_PAGES = 25;

export function CommentList({ taskId, projId, focusCommentId }: CommentListProps) {
  const [comments, setComments] = useState<Comment[]>([]);
  const [loading, setLoading] = useState(true);
  const [body, setBody] = useState("");
  const [isInternal, setIsInternal] = useState(false);
  const [replyTo, setReplyTo] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const { currentWorkspace } = useWorkspaceStore();
  const { teamDirectory, fetchTeamDirectory } = useRulesStore();

  useEffect(() => {
    if (currentWorkspace && !teamDirectory) {
      void fetchTeamDirectory(currentWorkspace.id);
    }
  }, [currentWorkspace, teamDirectory, fetchTeamDirectory]);

  const mentionables = useMemo<Map<string, MentionEntry>>(() => {
    const map = new Map<string, MentionEntry>();
    if (!teamDirectory) return map;
    for (const agent of teamDirectory.agents) {
      if (agent.slug) map.set(agent.slug, { kind: "agent" });
    }
    return map;
  }, [teamDirectory]);

  const wsSlug = currentWorkspace?.slug;

  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);

  // The panel reuses one CommentList instance across every task the user
  // views in a session (task-panel.tsx switches `currentTask` without
  // unmounting) — so a fetch for a task the user has since navigated away
  // from can resolve AFTER the current task's own fetch already has, and
  // without this guard would overwrite the right comments with the wrong
  // task's. Mirrors the mountedRef pattern in useProjectTrIntegration and
  // the `cancelled` flag in MarkdownView, both touched by the same rewrite
  // that introduced this gap. See task c39ea2aa.
  const taskIdRef = useRef(taskId);
  taskIdRef.current = taskId;

  // sort_dir=desc requests the NEWEST page first — the unparameterized GET
  // this used to send defaults to oldest-first, so a thread with more than
  // one page silently showed only its earliest comments (the freshest
  // activity never got fetched at all, regardless of how the fetched items
  // were sorted for display). "Show earlier" pages further back in the same
  // order. See task 4222c17d (D4) / parent diagnosis 9855f866.
  const fetchComments = useCallback(
    async (pageNum: number, append: boolean) => {
      const requestedTaskId = taskId;
      try {
        const data = await api<PaginatedResponse<Comment>>(
          `/api/v1/tasks/${taskId}/comments`,
          { params: { sort_dir: "desc", page: pageNum } },
        );
        if (taskIdRef.current !== requestedTaskId) return;
        const items = data.items ?? [];
        setComments((prev) => (append ? [...prev, ...items] : items));
        setHasMore(data.has_more);
        setPage(data.page);
      } catch {
        // silently fail — will show empty list (only if still on-task)
      } finally {
        if (taskIdRef.current === requestedTaskId) {
          setLoading(false);
          setLoadingMore(false);
        }
      }
    },
    [taskId],
  );

  useEffect(() => {
    setLoading(true);
    void fetchComments(1, false);
  }, [fetchComments]);

  const handleLoadEarlier = async () => {
    setLoadingMore(true);
    await fetchComments(page + 1, true);
  };

  // A mention's `?comment=` id can name a comment on any page — sort_dir=desc
  // means page 1 is only the newest slice of the thread, so an older target
  // is invisible (and un-scrollable-to) until its page is fetched. Keep
  // asking for the next page until the target shows up or the server says
  // there is no more (`hasMore === false`) — the latter is the explicit
  // "not found" outcome: the target was deleted, or belongs to a different
  // task, and auto-loading stops rather than silently doing nothing forever.
  // Capped independently of `hasMore` in case a future response bug ever
  // reports more pages than truly exist.
  const autoPageCountRef = useRef(0);
  useEffect(() => {
    autoPageCountRef.current = 0;
  }, [taskId, focusCommentId]);

  useEffect(() => {
    if (!focusCommentId || loading || loadingMore || !hasMore) return;
    const target = comments.find((c) => c.id === focusCommentId);
    // A reply's own id can be in `comments` — the flat, unpaginated-by-thread
    // state — while its root sits on a page not yet fetched. It won't be
    // rendered until then: `topLevel`/`repliesByParent` below only nest a
    // reply under a root that's actually loaded, and replies never nest
    // further than one level (see CommentItem — the reply button only
    // appears on `!isReply` comments), so a defined parent id is always a
    // root, never another reply. Treating bare presence in `comments` as
    // "found" here stopped pagination one page too early and left the reply
    // permanently un-rendered — silently, with no highlight and no scroll.
    const found =
      target != null &&
      (!target.parent_comment_id || comments.some((c) => c.id === target.parent_comment_id));
    if (found) return;
    if (autoPageCountRef.current >= MAX_AUTO_FOCUS_PAGES) return;
    autoPageCountRef.current += 1;
    void handleLoadEarlier();
  }, [focusCommentId, comments, hasMore, loading, loadingMore]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!body.trim()) return;
    setSubmitting(true);

    try {
      const req: CreateCommentRequest = {
        body: body.trim(),
        is_internal: isInternal || undefined,
        parent_comment_id: replyTo ?? undefined,
      };
      const created = await api<Comment>(
        `/api/v1/tasks/${taskId}/comments`,
        { method: "POST", body: req },
      );
      setComments((prev) => [created, ...prev]);
      setReplyTo(null);
      setBody("");
      setIsInternal(false);
    } catch {
      // error handled by api layer
    } finally {
      setSubmitting(false);
    }
  };

  const handleSave = async (commentId: string, newBody: string) => {
    const updated = await api<Comment>(
      `/api/v1/comments/${commentId}`,
      { method: "PATCH", body: { body: newBody } },
    );
    setComments((prev) => prev.map((c) => (c.id === updated.id ? updated : c)));
  };

  const handleDelete = async (commentId: string) => {
    try {
      await api(`/api/v1/comments/${commentId}`, { method: "DELETE" });
      setComments((prev) => prev.filter((c) => c.id !== commentId));
    } catch {
      // error handled by api layer
    }
  };

  const handleReply = (parentId: string) => {
    setReplyTo(parentId);
    setBody("");
  };

  const handleCancelReply = () => {
    setReplyTo(null);
    setBody("");
  };

  if (loading) {
    return (
      <div className="flex-1 p-4 space-y-3">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
    );
  }

  const topLevel = comments
    .filter((c) => !c.parent_comment_id)
    .sort(byNewestFirst);

  const repliesByParent = comments.reduce<Record<string, Comment[]>>(
    (acc, c) => {
      if (c.parent_comment_id) {
        const existing = acc[c.parent_comment_id];
        if (existing) {
          existing.push(c);
        } else {
          acc[c.parent_comment_id] = [c];
        }
      }
      return acc;
    },
    {},
  );

  // Sort replies newest-first too
  for (const replies of Object.values(repliesByParent)) {
    replies.sort(byNewestFirst);
  }

  const replyToComment = replyTo
    ? comments.find((c) => c.id === replyTo)
    : null;

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      {/* Scrollable comments list */}
      <div className="flex-1 overflow-x-hidden overflow-y-auto p-4">
        {topLevel.length === 0 && (
          <p className="py-4 text-center text-sm text-muted-foreground">
            No comments yet. Be the first to comment.
          </p>
        )}

        <div className="space-y-1">
          {topLevel.map((comment) => (
            <CommentItem
              key={comment.id}
              comment={comment}
              replies={repliesByParent[comment.id] ?? []}
              onReply={handleReply}
              onSave={handleSave}
              onDelete={handleDelete}
              projId={projId}
              mentionables={mentionables}
              wsSlug={wsSlug}
              focusCommentId={focusCommentId}
            />
          ))}
        </div>

        {hasMore && (
          <div className="flex justify-center pt-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void handleLoadEarlier()}
              disabled={loadingMore}
            >
              {loadingMore ? "Loading..." : "Load more"}
            </Button>
          </div>
        )}
      </div>

      {/* Reply form — shrink-0 keeps it pinned at the bottom of the flex column */}
      <div className="shrink-0 border-t border-border bg-background px-3 pt-3 pb-safe">
        <form onSubmit={handleSubmit} className="relative space-y-2">
          {replyToComment && (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Reply className="h-3 w-3" />
              Replying to{" "}
              {replyToComment.author_name?.trim() || (replyToComment.author_type === "agent" ? "Agent" : replyToComment.author_type === "system" ? "System" : "User")}
              <button
                type="button"
                className="ml-1 text-primary hover:underline"
                onClick={handleCancelReply}
              >
                Cancel
              </button>
            </div>
          )}
          <RichTextEditor
            value={body}
            onChange={setBody}
            projId={projId}
            taskId={taskId}
            placeholder="Write a comment… @ to mention, [[ to link a document"
            minHeight="4.5rem"
            hint={null}
          />
          <div className="flex items-center justify-between">
            <label className="flex items-center gap-2 text-sm text-muted-foreground">
              <input
                type="checkbox"
                checked={isInternal}
                onChange={(e) => setIsInternal(e.target.checked)}
                className="rounded border-border"
              />
              <Lock className="h-3.5 w-3.5" />
              Internal note
            </label>
            <div className="flex items-center gap-2">
              <Button type="submit" size="sm" disabled={!body.trim() || submitting}>
                Comment
              </Button>
            </div>
          </div>
        </form>
      </div>
    </div>
  );
}
